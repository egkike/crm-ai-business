package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// This file declares the RF4 catalog tools: search_catalog (every resolved role)
// and the Owner/Admin surface create_catalog_item / update_catalog_item.
//
// The registry is the single source of truth for both halves of RF2: what
// tools/list announces and what tools/call executes. Declaring Access here is
// therefore the whole authorization contract of the transport; the use cases
// re-assert it independently (AGENTS.md double RBAC).

const (
	// searchCatalogToolName is the RF4 full-text search. It is the tool Hermes
	// calls with the client's own words, and it is visible to every role the
	// identity chain resolved — never to an unknown caller, who must register
	// first (the RF3 loop).
	searchCatalogToolName = "search_catalog"

	// createCatalogItemToolName and updateCatalogItemToolName are the RF4
	// high/edición surface, reserved to Owner/Admin.
	createCatalogItemToolName = "create_catalog_item"
	updateCatalogItemToolName = "update_catalog_item"
)

// The argument names of the three tools, shared by the schemas and the decoders
// so a rename cannot desynchronize them.
const (
	argQuery       = "query"
	argLimit       = "limit"
	argOffset      = "offset"
	argID          = "id"
	argKind        = "kind"
	argName        = "name"
	argDescription = "description"
	argUseCases    = "use_cases"
	argLimitations = "limitations"
	argPriceCents  = "price_cents"
	argSalesURL    = "sales_url"
	argDurationMin = "duration_min"
)

// The catalog wire schemas.
//
// additionalProperties is false in all three for the same reason as in
// register_client: an unknown key is an attempt to smuggle a second contract in
// (a "role", a "client_id", an "is_active" flag) and is refused, never ignored.
// The nullable money/duration fields are declared as ["integer","null"] because
// null is a MEANING here: an absent price is "a consultar", an absent duration is
// "not stated".
var (
	searchCatalogInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "maxLength": 500,
      "description": "Lo que la persona está buscando, en sus propias palabras (por ejemplo \"corte de pelo\" o \"me duele la espalda\"). Se busca en el nombre, la descripción, los casos de uso y las limitaciones de cada ítem."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "description": "Cuántos resultados devolver como máximo. Por defecto 10."
    },
    "offset": {
      "type": "integer",
      "minimum": 0,
      "description": "Cuántos resultados saltear, para pedir la página siguiente. Por defecto 0."
    }
  },
  "additionalProperties": false
}`)

	catalogItemProperties = `    "kind": {
      "type": "string",
      "enum": ["producto", "servicio"],
      "description": "producto (se vende) o servicio (se reserva)."
    },
    "name": {
      "type": "string",
      "maxLength": 120,
      "description": "Nombre del ítem tal como debe verlo el cliente."
    },
    "description": {
      "type": "string",
      "maxLength": 2000,
      "description": "Qué es el ítem, en una o dos frases."
    },
    "use_cases": {
      "type": "string",
      "maxLength": 2000,
      "description": "Qué problemas resuelve y para quién. Es lo que permite encontrar el ítem cuando el cliente describe su problema en vez de nombrarlo."
    },
    "limitations": {
      "type": "string",
      "maxLength": 2000,
      "description": "Qué NO cubre el ítem. Honestidad pre-venta: evita promesas que el negocio no puede cumplir."
    },
    "price_cents": {
      "type": ["integer", "null"],
      "minimum": 0,
      "description": "Precio en centavos (entero). Omitilo o enviá null si todavía no tiene precio: quedará a consultar."
    },
    "sales_url": {
      "type": "string",
      "maxLength": 500,
      "description": "Enlace de compra online, si existe."
    },
    "duration_min": {
      "type": ["integer", "null"],
      "minimum": 1,
      "description": "Duración en minutos. Solo para servicios: un producto no puede tener duración."
    }`

	createCatalogItemInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
` + catalogItemProperties + `
  },
  "required": ["kind", "name"],
  "additionalProperties": false
}`)

	updateCatalogItemInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "Identificador interno (UUID) del ítem a editar, tal como lo devolvió search_catalog."
    },
` + catalogItemProperties + `
  },
  "required": ["id", "kind", "name"],
  "additionalProperties": false
}`)

	catalogItemOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "Identificador interno (UUID) del ítem."},
    "kind": {"type": "string", "enum": ["producto", "servicio"]},
    "name": {"type": "string"},
    "description": {"type": "string"},
    "use_cases": {"type": "string"},
    "limitations": {"type": "string"},
    "price_cents": {"type": ["integer", "null"], "description": "Precio en centavos, o null si está a consultar."},
    "sales_url": {"type": "string"},
    "duration_min": {"type": ["integer", "null"], "description": "Duración en minutos, o null si no aplica."}
  },
  "required": ["id", "kind", "name", "description", "use_cases", "limitations", "price_cents", "sales_url", "duration_min"]
}`)

	searchCatalogOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "items": {
      "type": "array",
      "description": "Ítems activos que coinciden, del más relevante al menos relevante.",
      "items": ` + string(catalogItemOutputSchema) + `
    },
    "limit": {"type": "integer", "description": "Tamaño de página aplicado."},
    "offset": {"type": "integer", "description": "Resultados salteados."}
  },
  "required": ["items", "limit", "offset"]
}`)
)

// catalogItemOutput is the wire carrier of one item. It is the same shape for
// search results and for the create/update outcome, so the agent has one mental
// model for the three tools. It is a carrier: no behavior.
type catalogItemOutput struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	UseCases    string `json:"use_cases"`
	Limitations string `json:"limitations"`
	// PriceCents is null when the price is not published ("a consultar").
	PriceCents *int64 `json:"price_cents"`
	SalesURL   string `json:"sales_url"`
	// DurationMin is null when the item does not state one.
	DurationMin *int `json:"duration_min"`
}

func newCatalogItemOutput(record dto.CatalogItemRecord) catalogItemOutput {
	return catalogItemOutput{
		ID:          record.ID,
		Kind:        string(record.Kind),
		Name:        record.Name,
		Description: record.Description,
		UseCases:    record.UseCases,
		Limitations: record.Limitations,
		PriceCents:  record.PriceCents,
		SalesURL:    record.SalesURL,
		DurationMin: record.DurationMin,
	}
}

// searchCatalogOutput is the wire carrier of the search.
type searchCatalogOutput struct {
	Items  []catalogItemOutput `json:"items"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

// catalogSearchTool declares the RF4 search.
//
// Access: every RESOLVED role. A client searching the catalog is the product's
// main use case, so the tool must never be Owner-only; and it is not anonymous,
// because an unresolved sender has to be registered first — announcing the
// catalog to an identity the database does not know would be answering for a
// business that has not admitted them (PRD §3.8).
func catalogSearchTool(search CatalogSearch) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: searchCatalogToolName,
			Description: "Busca en el catálogo del negocio lo que la persona está pidiendo. " +
				"Usá las palabras de la persona (aunque describa un problema, no un producto): la búsqueda mira nombre, descripción, usos y limitaciones. " +
				"Devuelve solo ítems activos, del más al menos relevante.",
			InputSchema:  searchCatalogInputSchema,
			OutputSchema: searchCatalogOutputSchema,
		},
		Access: Access{Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff, auth.RoleClient}},
		Handler: func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			caller, err := resolvedCaller(ctx, searchCatalogToolName)
			if err != nil {
				return nil, err
			}

			query, err := decodeSearchCatalogArgs(rawArguments(req))
			if err != nil {
				return nil, err
			}

			found, err := search.Execute(ctx, caller, query)
			if err != nil {
				return nil, err
			}

			output := searchCatalogOutput{
				Items:  make([]catalogItemOutput, 0, len(found.Items)),
				Limit:  found.Limit,
				Offset: found.Offset,
			}
			for _, item := range found.Items {
				output.Items = append(output.Items, newCatalogItemOutput(item))
			}
			return encodedResult(output)
		},
	}
}

// catalogCreateTool declares the RF4 high.
//
// Access: Owner/Admin only (RF4 "alta/edición por Owner/Admin"). For any other
// role the tool is not announced at all and its invocation is refused by the
// registry before a handler runs; the use case re-asserts the same rule.
func catalogCreateTool(create CatalogCreate) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: createCatalogItemToolName,
			Description: "Da de alta un producto o servicio del catálogo del negocio. " +
				"Cargá use_cases y limitations siempre que puedas: son los campos que permiten que un cliente encuentre el ítem describiendo su problema.",
			InputSchema:  createCatalogItemInputSchema,
			OutputSchema: catalogItemOutputSchema,
		},
		Access:  Access{Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin}},
		Handler: catalogCreateHandler(create),
	}
}

// catalogUpdateTool declares the RF4 edición.
//
// The payload REPLACES the item's editable fields: the schema asks for the whole
// item, and what is not sent is cleared. That keeps "not sent" from meaning two
// different things on a nullable column (a JSON argument has no third state), and
// the tool description says so explicitly.
func catalogUpdateTool(update CatalogUpdate) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: updateCatalogItemToolName,
			Description: "Edita un ítem del catálogo. Enviá el ítem COMPLETO: los campos que no vengan se guardan vacíos. " +
				"Los campos opcionales que no apliquen se envían vacíos o en null.",
			InputSchema:  updateCatalogItemInputSchema,
			OutputSchema: catalogItemOutputSchema,
		},
		Access:  Access{Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin}},
		Handler: catalogUpdateHandler(update),
	}
}

// catalogCreateHandler adapts the creation use case to the tool handler.
func catalogCreateHandler(create CatalogCreate) gomcp.ToolHandler {
	return func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		caller, err := resolvedCaller(ctx, createCatalogItemToolName)
		if err != nil {
			return nil, err
		}

		item, err := decodeCatalogItemArgs(rawArguments(req), false)
		if err != nil {
			return nil, err
		}

		record, err := create.Execute(ctx, caller, item)
		if err != nil {
			return nil, err
		}
		return encodedResult(newCatalogItemOutput(record))
	}
}

// catalogUpdateHandler adapts the edition use case to the tool handler.
func catalogUpdateHandler(update CatalogUpdate) gomcp.ToolHandler {
	return func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		caller, err := resolvedCaller(ctx, updateCatalogItemToolName)
		if err != nil {
			return nil, err
		}

		arguments := rawArguments(req)
		id, err := decodeCatalogItemID(arguments)
		if err != nil {
			return nil, err
		}
		item, err := decodeCatalogItemArgs(arguments, true)
		if err != nil {
			return nil, err
		}

		record, err := update.Execute(ctx, caller, id, item)
		if err != nil {
			return nil, err
		}
		return encodedResult(newCatalogItemOutput(record))
	}
}

// resolvedCaller reads the caller the identity middleware resolved.
//
// A missing caller is a wiring bug (the registry gate already refused every
// unresolved sender for these tools), so it answers the generic internal error
// instead of inventing an authorization failure.
func resolvedCaller(ctx context.Context, toolName string) (auth.Caller, error) {
	caller, ok := CallerFromContext(ctx)
	if !ok {
		return auth.Caller{}, fmt.Errorf("transporte MCP: %s sin identidad resuelta en el contexto", toolName)
	}
	return caller, nil
}

// rawArguments returns the raw arguments of a call (nil when absent).
func rawArguments(req *gomcp.CallToolRequest) json.RawMessage {
	if req == nil || req.Params == nil {
		return nil
	}
	return req.Params.Arguments
}

// encodedResult serializes an output carrier as both the text content and the
// structured content of a successful call.
func encodedResult(output any) (*gomcp.CallToolResult, error) {
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("transporte MCP: serializar el resultado: %w", err)
	}
	return &gomcp.CallToolResult{
		Content:           []gomcp.Content{&gomcp.TextContent{Text: string(encoded)}},
		StructuredContent: output,
	}, nil
}

// decodeSearchCatalogArgs validates the search arguments.
//
// Every rejection is a protocol-level -32602 with a Spanish message and no echo
// of the offending value.
func decodeSearchCatalogArgs(raw json.RawMessage) (dto.SearchCatalogQuery, error) {
	arguments, err := argumentObject(raw, searchCatalogToolName)
	if err != nil {
		return dto.SearchCatalogQuery{}, err
	}

	query := dto.SearchCatalogQuery{}
	for key, value := range arguments {
		switch key {
		case argQuery:
			if err := json.Unmarshal(value, &query.Text); err != nil {
				return dto.SearchCatalogQuery{}, invalidArguments("query debe ser un texto.")
			}
		case argLimit:
			limit, err := decodeOptionalInt(value, argLimit)
			if err != nil {
				return dto.SearchCatalogQuery{}, err
			}
			if limit != nil && *limit < 1 {
				return dto.SearchCatalogQuery{}, invalidArguments("limit debe ser un entero mayor que cero.")
			}
			if limit != nil {
				query.Limit = *limit
			}
		case argOffset:
			offset, err := decodeOptionalInt(value, argOffset)
			if err != nil {
				return dto.SearchCatalogQuery{}, err
			}
			if offset != nil && *offset < 0 {
				return dto.SearchCatalogQuery{}, invalidArguments("offset no puede ser negativo.")
			}
			if offset != nil {
				query.Offset = *offset
			}
		default:
			return dto.SearchCatalogQuery{}, invalidArguments(searchCatalogToolName + " no acepta parámetros adicionales.")
		}
	}
	return query, nil
}

// decodeCatalogItemArgs validates the item payload of create/update. When
// requireID is false the id key is refused (it belongs to update only).
func decodeCatalogItemArgs(raw json.RawMessage, allowID bool) (dto.CatalogItemInput, error) {
	arguments, err := argumentObject(raw, catalogItemToolName(allowID))
	if err != nil {
		return dto.CatalogItemInput{}, err
	}
	name := catalogItemToolName(allowID)

	var item dto.CatalogItemInput
	for key, value := range arguments {
		switch key {
		case argID:
			if !allowID {
				return dto.CatalogItemInput{}, invalidArguments(name + " genera un id nuevo: no acepta el parámetro id.")
			}
			// The update handler decodes the id separately.
		case argKind:
			var kind string
			if err := json.Unmarshal(value, &kind); err != nil {
				return dto.CatalogItemInput{}, invalidArguments("kind debe ser un texto.")
			}
			item.Kind = dto.CatalogKind(kind)
		case argName:
			if err := json.Unmarshal(value, &item.Name); err != nil {
				return dto.CatalogItemInput{}, invalidArguments("name debe ser un texto.")
			}
		case argDescription:
			if err := json.Unmarshal(value, &item.Description); err != nil {
				return dto.CatalogItemInput{}, invalidArguments("description debe ser un texto.")
			}
		case argUseCases:
			if err := json.Unmarshal(value, &item.UseCases); err != nil {
				return dto.CatalogItemInput{}, invalidArguments("use_cases debe ser un texto.")
			}
		case argLimitations:
			if err := json.Unmarshal(value, &item.Limitations); err != nil {
				return dto.CatalogItemInput{}, invalidArguments("limitations debe ser un texto.")
			}
		case argPriceCents:
			price, err := decodeOptionalCents(value)
			if err != nil {
				return dto.CatalogItemInput{}, err
			}
			item.PriceCents = price
		case argSalesURL:
			if err := json.Unmarshal(value, &item.SalesURL); err != nil {
				return dto.CatalogItemInput{}, invalidArguments("sales_url debe ser un texto.")
			}
		case argDurationMin:
			duration, err := decodeOptionalInt(value, argDurationMin)
			if err != nil {
				return dto.CatalogItemInput{}, err
			}
			item.DurationMin = duration
		default:
			return dto.CatalogItemInput{}, invalidArguments(name + " no acepta parámetros adicionales.")
		}
	}
	return item, nil
}

// decodeCatalogItemID reads the required id of an update.
func decodeCatalogItemID(raw json.RawMessage) (string, error) {
	arguments, err := argumentObject(raw, updateCatalogItemToolName)
	if err != nil {
		return "", err
	}
	value, present := arguments[argID]
	if !present {
		return "", invalidArguments("update_catalog_item requiere el id del ítem a editar.")
	}
	var id string
	if err := json.Unmarshal(value, &id); err != nil {
		return "", invalidArguments("id debe ser un texto.")
	}
	if id == "" {
		return "", invalidArguments("update_catalog_item requiere el id del ítem a editar.")
	}
	return id, nil
}

// argumentObject decodes the arguments of a call into their key/value pairs.
// Absent arguments and JSON null are the empty object (every field is optional
// for the schema, and the tool that needs one says so itself).
func argumentObject(raw json.RawMessage, toolName string) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}

	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &arguments); err != nil {
		return nil, invalidArguments("los argumentos de " + toolName + " no son un objeto JSON válido.")
	}
	if arguments == nil {
		return map[string]json.RawMessage{}, nil
	}
	return arguments, nil
}

// decodeOptionalInt decodes an optional integer argument. A JSON null (or an
// absent key) is nil; a fractional or non-numeric value is refused, because
// silently truncating a duration or a page size invents a number the caller did
// not send.
func decodeOptionalInt(raw json.RawMessage, label string) (*int, error) {
	if isNull(raw) {
		return nil, nil
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return nil, invalidArguments(label + " debe ser un número entero.")
	}
	value, err := number.Int64()
	if err != nil {
		return nil, invalidArguments(label + " debe ser un número entero.")
	}
	converted := int(value)
	if int64(converted) != value {
		return nil, invalidArguments(label + " está fuera del rango admitido.")
	}
	return &converted, nil
}

// decodeOptionalCents decodes the optional price. Money is an integer number of
// cents: a JSON float is refused instead of being rounded, because a rounded
// price is a wrong price.
func decodeOptionalCents(raw json.RawMessage) (*int64, error) {
	if isNull(raw) {
		return nil, nil
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return nil, invalidArguments("price_cents debe ser un número entero de centavos.")
	}
	value, err := number.Int64()
	if err != nil {
		return nil, invalidArguments("price_cents debe ser un número entero de centavos (sin decimales).")
	}
	return &value, nil
}

// isNull reports whether a raw JSON value is null (an absent key is handled by
// the caller's lookup).
func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// catalogItemToolName names the tool being decoded (create or update).
func catalogItemToolName(allowID bool) string {
	if allowID {
		return updateCatalogItemToolName
	}
	return createCatalogItemToolName
}

// CatalogSearch is the RF4 search use case port (satisfied by
// *usecase.SearchCatalog).
type CatalogSearch interface {
	Execute(ctx context.Context, caller auth.Caller, query dto.SearchCatalogQuery) (dto.CatalogSearchResult, error)
}

// CatalogCreate is the RF4 creation use case port (satisfied by
// *usecase.CreateCatalogItem).
type CatalogCreate interface {
	Execute(ctx context.Context, caller auth.Caller, item dto.CatalogItemInput) (dto.CatalogItemRecord, error)
}

// CatalogUpdate is the RF4 edition use case port (satisfied by
// *usecase.UpdateCatalogItem).
type CatalogUpdate interface {
	Execute(ctx context.Context, caller auth.Caller, id string, item dto.CatalogItemInput) (dto.CatalogItemRecord, error)
}

// CatalogDependencies are the three RF4 catalog ports, wired as a whole.
type CatalogDependencies struct {
	// Search backs search_catalog (every resolved role).
	Search CatalogSearch

	// Create backs create_catalog_item (Owner/Admin).
	Create CatalogCreate

	// Update backs update_catalog_item (Owner/Admin).
	Update CatalogUpdate
}

// errCatalogPartiallyWired is the construction refusal of a partial catalog: a
// handler that could search but not create would announce a tool set the
// composition root never intended, and the gap would only surface per request.
var errCatalogPartiallyWired = errors.New("transporte MCP: el catálogo se declara completo: se requieren la búsqueda, el alta y la edición")

// newCatalogTools builds the RF4 tools from their ports. A nil CatalogDependencies
// means the composition root did not wire the catalog, which is a construction
// fact (no tools) and not an authorization rule.
func newCatalogTools(deps *CatalogDependencies) ([]Tool, error) {
	if deps == nil {
		return nil, nil
	}
	if deps.Search == nil || deps.Create == nil || deps.Update == nil {
		return nil, errCatalogPartiallyWired
	}

	return []Tool{
		catalogSearchTool(deps.Search),
		catalogCreateTool(deps.Create),
		catalogUpdateTool(deps.Update),
	}, nil
}
