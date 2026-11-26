package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// This file declares the RF5 ticket tools: create_ticket, list_my_tickets and
// add_ticket_note for every resolved role, plus list_tickets and transition_ticket
// for the internal team.
//
// The registry is the single source of truth for both halves of RF2: what
// tools/list announces and what tools/call executes. Declaring Access here is
// therefore the whole authorization contract of the transport; the use cases
// re-assert it independently (AGENTS.md double RBAC).
//
// The S2 criterion drives the split: a client-role caller sees the tools of the
// client channel — open a ticket, read my own, append a message to my open one —
// and NEVER the internal queue (list_tickets) or the state machine
// (transition_ticket). Both are not only hidden from its tools/list, they are
// refused before a handler runs.

const (
	// createTicketToolName is the RF5 creation: "todo contacto con motivo de
	// negocio genera un ticket".
	createTicketToolName = "create_ticket"

	// listMyTicketsToolName is the client view ("my tickets"); an internal caller
	// uses it to see one client's tickets.
	listMyTicketsToolName = "list_my_tickets"

	// addTicketNoteToolName is the note / re-engagement message of RF5: a client
	// coming back about an open ticket appends here instead of opening a new one.
	addTicketNoteToolName = "add_ticket_note"

	// listTicketsToolName is the internal working queue (RF8's staff/admin/owner
	// visibility, F1: the open tickets of the business).
	listTicketsToolName = "list_tickets"

	// transitionTicketToolName is the staff transition. It is the tool RF9's
	// Telegram flow will call in F2.
	transitionTicketToolName = "transition_ticket"
)

// The argument names of the five tools, shared by the schemas and the decoders so
// a rename cannot desynchronize them.
const (
	argMotive        = "motive"
	argClientID      = "client_id"
	argTicketID      = "ticket_id"
	argText          = "text"
	argState         = "state"
	argIncludeClosed = "include_closed"
)

// The ticket wire schemas.
//
// additionalProperties is false in all five for the same reason as in
// register_client: an unknown key is an attempt to smuggle a second contract in —
// an "actor", a "state" on creation, a "client_id" where the identity must come
// from the request — and it is refused, never ignored.
//
// The bounds are declared twice on purpose and must stay equal: the JSON schema
// says what the agent is promised (maxLength 120 / 2000, limit 1..50) and the
// decoders below enforce it before a use case runs (review
// R3-schema-decoder-bounds). The declared numbers mirror usecase.MaxTicketMotiveLen,
// usecase.MaxTicketNoteLen and usecase.MaxTicketListLimit, and the use cases
// re-enforce them independently.
var (
	createTicketInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "motive": {
      "type": "string",
      "maxLength": 120,
      "description": "Motivo de negocio del contacto, en las palabras de la persona (por ejemplo \"consulta por corte de pelo\" o \"me duele la espalda desde el lunes\"). Es obligatorio: un mensaje sin motivo de negocio no genera ticket."
    },
    "client_id": {
      "type": "string",
      "description": "Solo para el equipo interno: identificador interno (UUID) del cliente para el que se abre el ticket. Un cliente siempre abre su propio ticket: no envíes este campo, la llamada se rechaza."
    }
  },
  "required": ["motive"],
  "additionalProperties": false
}`)

	ticketListInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "client_id": {
      "type": "string",
      "description": "Filtra los tickets de un cliente (identificador interno). Para un cliente este campo se ignora: siempre ve los suyos."
    },
    "include_closed": {
      "type": "boolean",
      "description": "Incluye los tickets cerrados. Por defecto false: la lista muestra las conversaciones abiertas."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "description": "Cuántos tickets devolver como máximo. Por defecto 20."
    },
    "offset": {
      "type": "integer",
      "minimum": 0,
      "description": "Cuántos tickets saltear, para pedir la página siguiente. Por defecto 0."
    }
  },
  "additionalProperties": false
}`)

	addTicketNoteInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "ticket_id": {
      "type": "string",
      "description": "Identificador interno (UUID) del ticket abierto al que se agrega el mensaje."
    },
    "text": {
      "type": "string",
      "maxLength": 2000,
      "description": "El mensaje o la nota que se agrega al historial del ticket."
    }
  },
  "required": ["ticket_id", "text"],
  "additionalProperties": false
}`)

	transitionTicketInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "ticket_id": {
      "type": "string",
      "description": "Identificador interno (UUID) del ticket a mover."
    },
    "state": {
      "type": "string",
      "enum": ["nuevo", "atendido", "cerrado"],
      "description": "Estado destino del ticket."
    }
  },
  "required": ["ticket_id", "state"],
  "additionalProperties": false
}`)

	ticketEventOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "integer", "description": "Número de orden del evento en el historial."},
    "actor": {"type": "string", "description": "Quién actuó, como identificador interno del negocio o del cliente."},
    "kind": {"type": "string", "description": "creado, nota o transicion."},
    "payload": {"type": "object", "description": "Datos del evento (motivo, texto de la nota, estado de origen y destino)."},
    "created_at": {"type": "string", "description": "Cuándo ocurrió, en ISO-8601 UTC."}
  },
  "required": ["id", "actor", "kind", "payload", "created_at"]
}`)

	ticketOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "Identificador interno (UUID) del ticket."},
    "ticket_type_id": {"type": "string", "description": "Tipo de ticket. En F1 siempre es la consulta."},
    "client_id": {"type": "string", "description": "Identificador interno (UUID) del cliente."},
    "state": {"type": "string", "enum": ["nuevo", "atendido", "cerrado"], "description": "Estado actual del ticket."},
    "assigned_to": {"type": "string", "description": "Identificador interno de quien tiene asignado el ticket, o vacío si está sin asignar."},
    "created_at": {"type": "string", "description": "Cuándo se abrió, en ISO-8601 UTC."},
    "updated_at": {"type": "string", "description": "Cuándo fue la última actividad, en ISO-8601 UTC."},
    "events": {
      "type": "array",
      "description": "Historial auditable del ticket, del más viejo al más nuevo.",
      "items": ` + string(ticketEventOutputSchema) + `
    }
  },
  "required": ["id", "ticket_type_id", "client_id", "state", "assigned_to", "created_at", "updated_at", "events"]
}`)

	ticketListOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "tickets": {
      "type": "array",
      "description": "Tickets que coinciden, del más reciente al más viejo por actividad.",
      "items": ` + string(ticketOutputSchema) + `
    },
    "limit": {"type": "integer", "description": "Tamaño de página aplicado."},
    "offset": {"type": "integer", "description": "Tickets salteados."}
  },
  "required": ["tickets", "limit", "offset"]
}`)
)

// ticketEventOutput is the wire carrier of one audited event. It is a carrier: no
// behavior.
type ticketEventOutput struct {
	ID    int64  `json:"id"`
	Actor string `json:"actor"`
	Kind  string `json:"kind"`
	// Payload is embedded as a JSON object, not as a string: the agent reads the
	// audit directly instead of parsing a second document out of a string.
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// ticketOutput is the wire carrier of one ticket plus its history.
type ticketOutput struct {
	ID           string              `json:"id"`
	TicketTypeID string              `json:"ticket_type_id"`
	ClientID     string              `json:"client_id"`
	State        string              `json:"state"`
	AssignedTo   string              `json:"assigned_to"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
	Events       []ticketEventOutput `json:"events"`
}

// ticketListOutput is the wire carrier of a page of tickets.
type ticketListOutput struct {
	Tickets []ticketOutput `json:"tickets"`
	Limit   int            `json:"limit"`
	Offset  int            `json:"offset"`
}

// newTicketEventOutput carries one event to the wire.
func newTicketEventOutput(record dto.TicketEventRecord) ticketEventOutput {
	return ticketEventOutput{
		ID:        record.ID,
		Actor:     record.Actor,
		Kind:      record.Kind,
		Payload:   eventPayloadDocument(record.Payload),
		CreatedAt: record.CreatedAt,
	}
}

// eventPayloadDocument embeds the stored payload as a JSON object. A payload that
// does not parse cannot happen (the column CHECKs json_valid), and if it ever did,
// the empty object is a truthful "no data" instead of a broken response.
func eventPayloadDocument(payload string) json.RawMessage {
	if payload == "" || !json.Valid([]byte(payload)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(payload)
}

// newTicketOutput carries one ticket and its history to the wire.
func newTicketOutput(record dto.TicketRecord, events []dto.TicketEventRecord) ticketOutput {
	output := ticketOutput{
		ID:           record.ID,
		TicketTypeID: record.TicketTypeID,
		ClientID:     record.ClientID,
		State:        string(record.State),
		AssignedTo:   record.AssignedTo,
		CreatedAt:    record.CreatedAt,
		UpdatedAt:    record.UpdatedAt,
		Events:       make([]ticketEventOutput, 0, len(events)),
	}
	for _, event := range events {
		output.Events = append(output.Events, newTicketEventOutput(event))
	}
	return output
}

// newTicketListOutput carries a page of tickets to the wire.
func newTicketListOutput(result dto.TicketListResult) ticketListOutput {
	output := ticketListOutput{
		Tickets: make([]ticketOutput, 0, len(result.Tickets)),
		Limit:   result.Limit,
		Offset:  result.Offset,
	}
	for _, view := range result.Tickets {
		output.Tickets = append(output.Tickets, newTicketOutput(view.Ticket, view.Events))
	}
	return output
}

// createTicketTool declares the RF5 creation.
//
// Access: every RESOLVED role. A client opening the ticket of its own contact is
// the product's main flow, and a staff member opening the ticket a phone call
// produced is the internal counterpart — the use case decides WHO the ticket
// belongs to (the caller for a client, the named client for an internal role),
// never the transport. A client that NAMES a client is refused here, before the
// use case: the owner of a ticket is never an argument (review
// R1-ticket-clientid-trust).
func createTicketTool(create TicketCreate) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: createTicketToolName,
			Description: "Abre un ticket para el contacto en curso. Llamala cuando el mensaje tiene un motivo de negocio (consulta, reclamo, pedido), " +
				"aunque la persona todavía no compre ni solicite nada: el contacto queda registrado en el historial. " +
				"Si la persona ya tiene un ticket ABIERTO de la misma consulta, no abras otro: agregá su mensaje con add_ticket_note.",
			InputSchema:  createTicketInputSchema,
			OutputSchema: ticketOutputSchema,
		},
		Access: Access{Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff, auth.RoleClient}},
		Handler: func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			caller, err := resolvedCaller(ctx, createTicketToolName)
			if err != nil {
				return nil, err
			}

			command, err := decodeCreateTicketArgs(rawArguments(req), caller.Role)
			if err != nil {
				return nil, err
			}

			result, err := create.Execute(ctx, caller, command)
			if err != nil {
				return nil, err
			}
			return encodedResult(newTicketOutput(result.Ticket, result.Events))
		},
	}
}

// listMyTicketsTool declares the client view.
//
// Access: every RESOLVED role. For a client the use case overwrites the client
// filter with its own identity, so this tool can never be used to read somebody
// else's tickets; for an internal caller it is the per-client view.
func listMyTicketsTool(list TicketListMine) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: listMyTicketsToolName,
			Description: "Lista los tickets de la persona con la que estás conversando, con su historial. " +
				"Usala para saber si ya hay un ticket abierto antes de abrir uno nuevo o de agregar un mensaje.",
			InputSchema:  ticketListInputSchema,
			OutputSchema: ticketListOutputSchema,
		},
		Access:  Access{Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff, auth.RoleClient}},
		Handler: ticketListHandler(list, listMyTicketsToolName),
	}
}

// addTicketNoteTool declares the note / re-engagement message.
//
// Access: every RESOLVED role. A client appends to its OWN open ticket (the use
// case answers "no existe" for anyone else's, so the tool is not an oracle), and
// the internal team annotates any ticket. A closed ticket rejects the note: RF5
// opens a new ticket instead of reopening a closed conversation.
func addTicketNoteTool(add TicketAddNote) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: addTicketNoteToolName,
			Description: "Agrega un mensaje o una nota al historial de un ticket abierto. " +
				"Es el camino del re-engagement: si la persona vuelve sobre lo mismo y su ticket sigue abierto, su mensaje se agrega acá y NO se abre un ticket nuevo. " +
				"Si el ticket está cerrado, hay que abrir uno nuevo.",
			InputSchema:  addTicketNoteInputSchema,
			OutputSchema: ticketOutputSchema,
		},
		Access: Access{Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff, auth.RoleClient}},
		Handler: func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			caller, err := resolvedCaller(ctx, addTicketNoteToolName)
			if err != nil {
				return nil, err
			}

			note, err := decodeAddTicketNoteArgs(rawArguments(req))
			if err != nil {
				return nil, err
			}

			result, err := add.Execute(ctx, caller, note)
			if err != nil {
				return nil, err
			}
			return encodedResult(newTicketOutput(result.Ticket, result.Events))
		},
	}
}

// listTicketsTool declares the internal queue.
//
// Access: the three internal roles. For a client the tool is not announced at all
// and its invocation is refused by the registry before a handler runs; the use case
// re-asserts the same rule.
func listTicketsTool(list TicketListAll) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: listTicketsToolName,
			Description: "Lista los tickets del negocio con su historial, del más reciente al más viejo por actividad. " +
				"Por defecto muestra las conversaciones abiertas; usá include_closed para ver también las cerradas, y client_id para las de un cliente.",
			InputSchema:  ticketListInputSchema,
			OutputSchema: ticketListOutputSchema,
		},
		Access:  Access{Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff}},
		Handler: ticketListHandler(list, listTicketsToolName),
	}
}

// transitionTicketTool declares the staff transition.
//
// Access: the three internal roles (RF9: "internos operan tickets"). A client
// asking for a state change is refused by the registry and, independently, by the
// use case.
func transitionTicketTool(transition TicketTransition) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: transitionTicketToolName,
			Description: "Cambia el estado de un ticket. Los estados son nuevo, atendido y cerrado. " +
				"Cada transición queda auditada con quién la hizo, cuándo y desde/hacia qué estado. " +
				"Un ticket cerrado no acepta más notas: si el cliente vuelve, se abre uno nuevo.",
			InputSchema:  transitionTicketInputSchema,
			OutputSchema: ticketOutputSchema,
		},
		Access: Access{Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff}},
		Handler: func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			caller, err := resolvedCaller(ctx, transitionTicketToolName)
			if err != nil {
				return nil, err
			}

			command, err := decodeTransitionTicketArgs(rawArguments(req))
			if err != nil {
				return nil, err
			}

			result, err := transition.Execute(ctx, caller, command)
			if err != nil {
				return nil, err
			}
			return encodedResult(newTicketOutput(result.Ticket, result.Events))
		},
	}
}

// ticketListHandler adapts a list use case to the tool handler.
func ticketListHandler(list ticketLister, toolName string) gomcp.ToolHandler {
	return func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		caller, err := resolvedCaller(ctx, toolName)
		if err != nil {
			return nil, err
		}

		query, err := decodeTicketListArgs(rawArguments(req), toolName)
		if err != nil {
			return nil, err
		}

		result, err := list.Execute(ctx, caller, query)
		if err != nil {
			return nil, err
		}
		return encodedResult(newTicketListOutput(result))
	}
}

// decodeCreateTicketArgs validates the creation arguments for the role that
// sends them.
//
// The role is a parameter because client_id is not a neutral field: for an
// internal caller it names the client the ticket is opened for, and for a client
// it is an attempt to choose ownership. The use case already overwrites it with the
// resolved identity (a body field never decides who a client is), and refusing it
// here is the second, independent barrier of defense in depth (review
// R1-ticket-clientid-trust): the request does not travel one hop with a contract
// violation in it.
func decodeCreateTicketArgs(raw json.RawMessage, role auth.Role) (dto.CreateTicketCommand, error) {
	arguments, err := argumentObject(raw, createTicketToolName)
	if err != nil {
		return dto.CreateTicketCommand{}, err
	}

	var command dto.CreateTicketCommand
	for key, value := range arguments {
		switch key {
		case argMotive:
			motive, err := decodeBoundedText(value, argMotive, usecase.MaxTicketMotiveLen)
			if err != nil {
				return dto.CreateTicketCommand{}, err
			}
			command.Motive = motive
		case argClientID:
			if !role.IsInternal() {
				return dto.CreateTicketCommand{}, invalidArguments(
					"un cliente abre siempre su propio ticket: client_id no se acepta.")
			}
			if err := json.Unmarshal(value, &command.ClientID); err != nil {
				return dto.CreateTicketCommand{}, invalidArguments("client_id debe ser un texto.")
			}
		default:
			return dto.CreateTicketCommand{}, invalidArguments(createTicketToolName + " no acepta parámetros adicionales.")
		}
	}
	return command, nil
}

// decodeTicketListArgs validates the arguments shared by both list tools.
func decodeTicketListArgs(raw json.RawMessage, toolName string) (dto.TicketQuery, error) {
	arguments, err := argumentObject(raw, toolName)
	if err != nil {
		return dto.TicketQuery{}, err
	}

	query := dto.TicketQuery{}
	for key, value := range arguments {
		switch key {
		case argClientID:
			if err := json.Unmarshal(value, &query.ClientID); err != nil {
				return dto.TicketQuery{}, invalidArguments("client_id debe ser un texto.")
			}
		case argIncludeClosed:
			includeClosed, err := decodeOptionalBool(value, argIncludeClosed)
			if err != nil {
				return dto.TicketQuery{}, err
			}
			if includeClosed != nil {
				query.IncludeClosed = *includeClosed
			}
		case argLimit:
			limit, err := decodePageLimit(value)
			if err != nil {
				return dto.TicketQuery{}, err
			}
			if limit != 0 {
				query.Limit = limit
			}
		case argOffset:
			offset, err := decodeOptionalInt(value, argOffset)
			if err != nil {
				return dto.TicketQuery{}, err
			}
			if offset != nil && *offset < 0 {
				return dto.TicketQuery{}, invalidArguments("offset no puede ser negativo.")
			}
			if offset != nil {
				query.Offset = *offset
			}
		default:
			return dto.TicketQuery{}, invalidArguments(toolName + " no acepta parámetros adicionales.")
		}
	}
	return query, nil
}

// decodeAddTicketNoteArgs validates the note arguments.
func decodeAddTicketNoteArgs(raw json.RawMessage) (dto.TicketNote, error) {
	arguments, err := argumentObject(raw, addTicketNoteToolName)
	if err != nil {
		return dto.TicketNote{}, err
	}

	var note dto.TicketNote
	for key, value := range arguments {
		switch key {
		case argTicketID:
			if err := json.Unmarshal(value, &note.TicketID); err != nil {
				return dto.TicketNote{}, invalidArguments("ticket_id debe ser un texto.")
			}
		case argText:
			text, err := decodeBoundedText(value, argText, usecase.MaxTicketNoteLen)
			if err != nil {
				return dto.TicketNote{}, err
			}
			note.Text = text
		default:
			return dto.TicketNote{}, invalidArguments(addTicketNoteToolName + " no acepta parámetros adicionales.")
		}
	}
	return note, nil
}

// decodeTransitionTicketArgs validates the transition arguments.
func decodeTransitionTicketArgs(raw json.RawMessage) (dto.TransitionCommand, error) {
	arguments, err := argumentObject(raw, transitionTicketToolName)
	if err != nil {
		return dto.TransitionCommand{}, err
	}

	var command dto.TransitionCommand
	for key, value := range arguments {
		switch key {
		case argTicketID:
			if err := json.Unmarshal(value, &command.TicketID); err != nil {
				return dto.TransitionCommand{}, invalidArguments("ticket_id debe ser un texto.")
			}
		case argState:
			var state string
			if err := json.Unmarshal(value, &state); err != nil {
				return dto.TransitionCommand{}, invalidArguments("state debe ser un texto.")
			}
			command.State = dto.TicketState(state)
		default:
			return dto.TransitionCommand{}, invalidArguments(transitionTicketToolName + " no acepta parámetros adicionales.")
		}
	}
	return command, nil
}

// decodeBoundedText decodes a required text argument and enforces the rune bound
// its schema declares with maxLength.
//
// The bound is measured in runes (the unit the use case uses, so a two-byte
// character is one character for both) on the value as sent: the schema's
// maxLength is a promise about the request, and a request that breaks it is a
// protocol error answered with -32602 and Spanish prose, never an echo of the
// offending value.
func decodeBoundedText(raw json.RawMessage, name string, maxRunes int) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", invalidArguments(name + " debe ser un texto.")
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return "", invalidArguments(fmt.Sprintf("%s no puede superar los %d caracteres.", name, maxRunes))
	}
	return value, nil
}

// decodePageLimit decodes the page size and enforces the window the list schema
// declares: at least 1 and at most usecase.MaxTicketListLimit.
//
// An absent or null limit answers 0, which is "the caller sent none" and lets the
// use case apply its default. An oversized page is REFUSED instead of being
// silently capped: the agent asked for something the contract does not offer, and
// a capped answer would look like a complete one.
func decodePageLimit(raw json.RawMessage) (int, error) {
	limit, err := decodeOptionalInt(raw, argLimit)
	if err != nil {
		return 0, err
	}
	if limit == nil {
		return 0, nil
	}
	if *limit < 1 {
		return 0, invalidArguments("limit debe ser un entero mayor que cero.")
	}
	if *limit > usecase.MaxTicketListLimit {
		return 0, invalidArguments(fmt.Sprintf("limit no puede superar los %d tickets por página.", usecase.MaxTicketListLimit))
	}
	return *limit, nil
}

// decodeOptionalBool decodes an optional boolean argument. A JSON null (or an
// absent key) is nil; anything that is not a boolean is refused instead of being
// coerced, because "false" and "not sent" are different requests.
func decodeOptionalBool(raw json.RawMessage, label string) (*bool, error) {
	if isNull(raw) {
		return nil, nil
	}

	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, invalidArguments(label + " debe ser true o false.")
	}
	return &value, nil
}

// ticketLister is the shape both list use cases share, so one handler adapts
// either of them.
type ticketLister interface {
	Execute(ctx context.Context, caller auth.Caller, query dto.TicketQuery) (dto.TicketListResult, error)
}

// The RF5 use case ports (satisfied by the *usecase types of the same name).
type (
	// TicketCreate is the creation use case port (create_ticket).
	TicketCreate interface {
		Execute(ctx context.Context, caller auth.Caller, cmd dto.CreateTicketCommand) (dto.CreateTicketResult, error)
	}

	// TicketListMine is the client-view port (list_my_tickets).
	TicketListMine interface {
		Execute(ctx context.Context, caller auth.Caller, query dto.TicketQuery) (dto.TicketListResult, error)
	}

	// TicketListAll is the internal-queue port (list_tickets).
	TicketListAll interface {
		Execute(ctx context.Context, caller auth.Caller, query dto.TicketQuery) (dto.TicketListResult, error)
	}

	// TicketAddNote is the note port (add_ticket_note).
	TicketAddNote interface {
		Execute(ctx context.Context, caller auth.Caller, note dto.TicketNote) (dto.TicketNoteResult, error)
	}

	// TicketTransition is the transition port (transition_ticket).
	TicketTransition interface {
		Execute(ctx context.Context, caller auth.Caller, cmd dto.TransitionCommand) (dto.TransitionTicketResult, error)
	}
)

// TicketDependencies are the five RF5 ticket ports, wired as a whole.
type TicketDependencies struct {
	// Create backs create_ticket (every resolved role).
	Create TicketCreate

	// ListMine backs list_my_tickets (every resolved role).
	ListMine TicketListMine

	// ListAll backs list_tickets (internal roles).
	ListAll TicketListAll

	// AddNote backs add_ticket_note (every resolved role).
	AddNote TicketAddNote

	// Transition backs transition_ticket (internal roles).
	Transition TicketTransition
}

// errTicketPartiallyWired is the construction refusal of a partial ticket set: a
// handler that could list tickets but not transition them would announce a tool
// set the composition root never intended, and the gap would only surface per
// request.
var errTicketPartiallyWired = errors.New(
	"transporte MCP: los tickets se declaran completos: se requieren creación, listado propio, listado interno, nota y transición")

// newTicketTools builds the RF5 tools from their ports. A nil
// TicketDependencies means the composition root did not wire the tickets, which is
// a construction fact (no tools) and not an authorization rule.
func newTicketTools(deps *TicketDependencies) ([]Tool, error) {
	if deps == nil {
		return nil, nil
	}
	if deps.Create == nil || deps.ListMine == nil || deps.ListAll == nil || deps.AddNote == nil || deps.Transition == nil {
		return nil, errTicketPartiallyWired
	}

	return []Tool{
		createTicketTool(deps.Create),
		listMyTicketsTool(deps.ListMine),
		addTicketNoteTool(deps.AddNote),
		listTicketsTool(deps.ListAll),
		transitionTicketTool(deps.Transition),
	}, nil
}
