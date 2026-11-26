package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/auth"
)

const (
	// registerClientToolName is the RF3 registration tool. It is the only F1
	// tool, and the only one visible to a caller the §3.8 chain could not
	// resolve.
	registerClientToolName = "register_client"

	// displayNameArg is the only argument the tool accepts.
	displayNameArg = "display_name"

	// maxDisplayNameLen bounds the display name before persistence (AGENTS.md:
	// validate every tool arg by type, length and format). 120 runes is far more
	// than a person's name and far less than a place to hide a payload.
	maxDisplayNameLen = 120
)

// The tool's wire schema. additionalProperties is false because an unknown key
// in this tool is an attempt to smuggle a second contract in — a "role",
// "client_id" or "verified" flag would be exactly the injection seam PRD §3.8
// forbids — and it is refused, not ignored.
var (
	registerClientInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "display_name": {
      "type": "string",
      "maxLength": 120,
      "description": "Nombre con el que la persona quiere ser reconocida. Opcional; si no se envía, se usa el nombre por defecto enmascarado."
    }
  },
  "additionalProperties": false
}`)

	registerClientOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "client_id": {"type": "string", "description": "Identificador interno (UUID) del cliente."},
    "display_name": {"type": "string", "description": "Nombre con el que quedó registrado."},
    "created": {"type": "boolean", "description": "true si el cliente se creó en esta llamada; false si el registro ya existía."}
  },
  "required": ["client_id", "display_name", "created"]
}`)
)

// registerClientOutput is the tool result. It is a carrier: no behavior, only
// the three facts the gateway and the agent need after a registration.
type registerClientOutput struct {
	// ClientID is the internal UUID primary key of the clients row.
	ClientID string `json:"client_id"`
	// DisplayName is the name stored on the row.
	DisplayName string `json:"display_name"`
	// Created distinguishes a first admission from an idempotent one.
	Created bool `json:"created"`
}

// registerClientTool declares the RF3 registration seam as a tool.
//
// This is the sole documented exception to the double-RBAC contract of AGENTS.md
// (boundary contract 1): the sender by definition has no row yet, so the tool
// declares Access.Anonymous. Nothing here decides authorization — the use case
// owns the compensating controls (per-identifier rate limit, accounts collision
// refusal, fail-closed identifier collision, PII-masked default name).
func registerClientTool(register RegisterClient) Tool {
	return Tool{
		Definition: &gomcp.Tool{
			Name: registerClientToolName,
			Description: "Registra a la persona que está conversando como cliente del negocio, " +
				"usando el identificador del canal por el que llegó (teléfono o email). " +
				"Llamala solo cuando el sistema indique que no reconoce al cliente; repetirla no duplica el registro.",
			InputSchema:  registerClientInputSchema,
			OutputSchema: registerClientOutputSchema,
		},
		Access: Access{
			Anonymous: true,
			// A resolved caller may repeat the call: the use case answers
			// idempotently, which is exactly the gateway's retry-after-register
			// path of RF3.
			Roles: []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff, auth.RoleClient},
		},
		Handler: func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			// The identity comes from the request context, NEVER from the
			// arguments: the schema has no identity field, and a call that carried
			// one was already refused by decodeRegisterClientArgs.
			callerID, ok := CallerIDFromContext(ctx)
			if !ok {
				return nil, errors.New("transporte MCP: register_client sin identidad en el contexto")
			}

			var arguments json.RawMessage
			if req != nil && req.Params != nil {
				arguments = req.Params.Arguments
			}
			displayName, err := decodeRegisterClientArgs(arguments)
			if err != nil {
				return nil, err
			}

			result, err := register.Execute(ctx, dto.RegisterClientCommand{
				CallerID:    callerID,
				DisplayName: displayName,
			})
			if err != nil {
				// Semantic and identity failures keep their class (the
				// sanitizer maps them); an internal one collapses to the
				// generic message.
				return nil, err
			}

			output := registerClientOutput{
				ClientID:    result.ClientID,
				DisplayName: result.DisplayName,
				Created:     result.Created,
			}
			encoded, err := json.Marshal(output)
			if err != nil {
				return nil, fmt.Errorf("transporte MCP: serializar el resultado de register_client: %w", err)
			}

			return &gomcp.CallToolResult{
				Content:           []gomcp.Content{&gomcp.TextContent{Text: string(encoded)}},
				StructuredContent: output,
			}, nil
		},
	}
}

// decodeRegisterClientArgs validates the arguments against the tool's schema.
//
// The SDK's raw tool path performs no validation (we use AddTool rather than the
// inferred AddTool[In, Out] so the schema stays a hand-written contract with
// additionalProperties:false), so the check is explicit here:
//
//   - no arguments, or JSON null → the default display name
//   - an unknown key → refused (never ignored: ignoring it would let a caller
//     believe a role or identifier was accepted)
//   - display_name must be a JSON string of at most maxDisplayNameLen runes
//
// Every rejection is a protocol-level -32602 with a Spanish message and no echo
// of the offending value: attacker-controlled text has no business travelling
// back into the agent's context.
func decodeRegisterClientArgs(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}

	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return "", invalidArguments("los argumentos de register_client no son un objeto JSON válido.")
	}

	displayName := ""
	for key, value := range arguments {
		if key != displayNameArg {
			return "", invalidArguments("register_client no acepta parámetros adicionales.")
		}
		if err := json.Unmarshal(value, &displayName); err != nil {
			return "", invalidArguments("display_name debe ser un texto.")
		}
	}

	if utf8.RuneCountInString(displayName) > maxDisplayNameLen {
		return "", invalidArguments("display_name es demasiado largo (máximo 120 caracteres).")
	}

	return displayName, nil
}
