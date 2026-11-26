# Demo reproducible — el ciclo de valor de F1

> Guion verificable de lo que la fase 1 entrega: un contacto de cliente se convierte
> en un ticket auditado que el equipo atiende, con el catálogo respondiendo por
> **contenido**, no por coincidencia de nombres.
>
> Todo se ejecuta contra el binario real; no hay mocks en el camino.

## 0. Preparación

```bash
# base de datos de la demo (fuera del repo)
export MCP_DB_PATH="$(mktemp -d)/demo.db"
export MCP_REGISTRATION_RATE_LIMIT=10

# compilar y arrancar
go build -o /tmp/crm-ai-business ./cmd/server
/tmp/crm-ai-business &
```

Comprobación de arranque:

```bash
curl -s 127.0.0.1:3001/healthz          # {"status":"ok"}
curl -s -o /dev/null -w '%{http_code}\n' 127.0.0.1:3001/mcp   # 405 (POST-only)
```

Las llamadas MCP son JSON-RPC por POST a `/mcp` con el header de identidad:

```bash
mcp() { # mcp <caller-id> <json-rpc>
  curl -s 127.0.0.1:3001/mcp \
    -H "Content-Type: application/json" \
    -H "X-Caller-Id: $1" \
    -d "$2"
}
```

## 1. Un desconocido escribe → no se le reconoce

```bash
mcp "+5491100000001" '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

Esperado: error `-32000` con «no te reconozco. Por favor regístrate primero.»
y `data.code = "unknown_caller"`. **Nada se ejecuta** antes de resolver la identidad.

## 2. Se registra (el loop del gateway)

```bash
mcp "+5491100000001" '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"register_client","arguments":{"display_name":"Ana"}}}'
```

Esperado: `created: true`, el UUID interno del cliente y el nombre guardado.
El mismo request repetido responde `created: false` (admisión idempotente).

Ahora la identidad resuelve:

```bash
mcp "+5491100000001" '{"jsonrpc":"2.0","id":3,"method":"tools/list"}'
```

Esperado: solo las herramientas del rol cliente — `search_catalog`, `create_ticket`,
`list_my_tickets`, `add_ticket_note` (+ `register_client`).

## 3. El catálogo responde por el problema, no por el nombre

Primero el equipo carga un servicio con conocimiento (rol `owner` sembrado por el
operador; en la demo se carga por SQL directo con el mismo DDL del servidor):

```bash
sqlite3 "$MCP_DB_PATH" <<'SQL'
INSERT INTO catalog_items (id, kind, name, description, use_cases, limitations, price_cents, is_active, created_at, updated_at)
VALUES ('11111111-1111-4111-8111-111111111111', 'servicio', 'Visita técnica',
        'Asistencia en el domicilio del cliente', 
        'reparación de heladeras y freezer que no enfría; instalación de equipos nuevos',
        'no cubre equipos en garantía del fabricante', 1500000, 1, datetime('now'), datetime('now'));
SQL
```

El cliente **no nombra el servicio** — describe el síntoma:

```bash
mcp "+5491100000001" '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_catalog","arguments":{"query":"se me congela el freezer"}}}'
```

Esperado: la «Visita técnica» en los resultados, con sus `use_cases` y `limitations`
en el payload — el agente puede explicar qué resuelve y qué no.

## 4. El contacto se convierte en ticket auditado

```bash
mcp "+5491100000001" '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"create_ticket","arguments":{"motive":"se me congela el freezer, necesito una visita"}}}'
```

Esperado: el ticket en estado `nuevo`, con su evento de apertura (actor, tipo,
timestamp) y el motivo **con los teléfonos enmascarados** si el cliente escribió uno.

## 5. El equipo lo atiende

Un interno (`staff`, sembrado por el operador) ve el ticket y lo transiciona:

```bash
mcp "+5491100000010" '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"list_tickets","arguments":{}}}'
mcp "+5491100000010" '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"transition_ticket","arguments":{"ticket_id":"<id>","state":"atendido"}}}'
```

Esperado: la transición devuelve el ticket en `atendido` y un evento nuevo con
`from: nuevo`, `to: atendido` y el actor interno. Releer el ticket muestra la
historia completa: quién, cuándo, desde y hacia qué estado.

## 6. El cliente vuelve sobre lo mismo (re-engagement)

```bash
mcp "+5491100000001" '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"add_ticket_note","arguments":{"ticket_id":"<id>","text":"sigue igual, ¿pueden venir mañana?"}}}'
```

Esperado: la nota se agrega como **evento del ticket existente** — no se crea un
ticket nuevo. Un ticket cerrado rechaza la nota con un mensaje semántico.

## 7. Lo que esta demo prueba

| Afirmación del PRD | Dónde se ve |
|---|---|
| RF1 transporte loopback POST-only | paso 0 (`405` en GET) |
| RF2 identidad por emisor + `tools/list` por rol | pasos 1 y 2 |
| RF3 registro por canal, idempotente, sin duplicar | paso 2 |
| RF4 catálogo con conocimiento del agente (v1.11) | paso 3 |
| RF5 ticket por contacto, eventos auditados, re-engagement | pasos 4 y 6 |
| S5 transición por staff con historia | paso 5 |
| Techo de escala declarado y validado | `go test -run TestScaleCeiling ./internal/db/` |

## Notas de honestidad

- El canal **e-mail** no está incluido: sigue **pendiente de verificación** contra el
  gateway real (ver `docs/pairing.md` §3).
- El alta de catálogo por panel llega en F4; en F1 el equipo la hace por herramientas
  MCP (`owner`/`admin`) o, en la demo, por SQL con el DDL canónico.
- Este guion no reemplaza la suite: es la narración de lo que la suite ya pinea.
