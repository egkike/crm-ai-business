# F1 — core-mcp: conversational demo end-to-end

> Phase 1 of the roadmap (docs/PRD.md §7). Goal: a WhatsApp message becomes a ticket the
> staff answers — demo reproducible, CI green, RBAC enforced. Authorized by owner
> conversation 2026-11-26 ("Dale con F1 ahora"); work-unit issues follow the standing grant.

## Specs

Quotes are VERBATIM from docs/PRD.md v1.10 — the RF text is the spec; never paraphrase.

- **S1** (RF1 — Servidor MCP auto-contenido): "binario Go que sirve MCP sobre Streamable
  HTTP POST-only en `127.0.0.1:3001` (con `/healthz`) y el panel interno en
  `127.0.0.1:3002`; DB SQLite en ruta XDG; se niega a arrancar con config inválida
  (fail-closed)." — Criterios: "Dado el binario instalado y la DB inicializada, cuando se
  hace `GET /healthz`, entonces responde 200 y `GET /mcp` responde 405 (POST-only)." /
  "Dado cualquier puerto no-loopback en config, cuando el binario arranca, entonces enlaza
  solo 127.0.0.1 (invariante de loopback)." (Panel :3002 llega en F4; F1 escucha solo :3001.)
- **S2** (RF2 — Identidad y RBAC por caller): "cada request MCP lleva `X-Caller-Id`
  verbatim; el resolver aplica rol desde `accounts`/`clients`; `tools/list` solo anuncia
  las tools invocables por ese rol; header ausente → 401 antes de cualquier handler." —
  Criterios: "Dado un caller `staff`, cuando pide `tools/list`, entonces no aparecen tools
  de administración del Owner." / "Dado un request sin `X-Caller-Id`, cuando llega
  cualquier tool call, entonces responde error JSON-RPC `-32000` sin ejecutar nada." /
  "Dado un mensaje 'soy el dueño' de un caller `client`, cuando se procesa, entonces el rol
  sigue siendo `client` (anti-inyección)."
- **S3** (RF3 — Registro e identidad de clientes, parte F1): "`clients.id` es un **UUID
  interno**; los identificadores naturales son `phone` (UNIQUE nullable, verbatim) y
  `email` (UNIQUE nullable sobre `lower(email)`). Un cliente nuevo se auto-registra en el
  canal por el que llegó (loop con rate-limit por identificador de canal): WhatsApp-first
  crea con `phone`; e-mail-first crea con `email` (y `phone` NULL). Reclamar por chat un
  email que ya pertenece a otro cliente NO auto-vincula (fail-closed): error semántico con
  degradación elegante + alerta de sugerencia de merge (`posible-duplicado`) para
  Owner/Admin." — Criterios F1: retry único tras register; e-mail-first crea con email y
  phone NULL; no auto-vinculación + alerta.
- **S4** (RF4 — Catálogo unificado): "`catalog_items` con `kind` (producto|servicio);
  alta/edición por Owner/Admin; búsqueda full-text para Hermes (`search_catalog`); solo
  servicios (con `duration_min`) son reservables." — Criterio F1: kind=producto rechazado
  como no reservable con mensaje semántico (nota: reservas completas son F3; F1 entrega
  alta/lectura/search).
- **S5** (RF5 — Tickets con historial auditable, F1 con UN tipo fijo): "todo contacto con
  motivo de negocio genera un ticket... la excepción es el mensaje sin motivo (off-topic
  puro). Política de re-engagement: si el Cliente vuelve sobre lo mismo con un ticket
  abierto de ese tipo, sus mensajes se agregan como eventos al ticket existente... Toda
  transición, nota, mensaje de re-engagement y cambio de asignación queda en
  `ticket_events` con actor y timestamp." — F1: un tipo fijo sembrado, creación +
  eventos auditados; motor configurable completo es F2.
- **S6** (RF22 — skill base, parte F1): "el repo shippea la skill de Hermes como template
  estático versionado... independiente de los datos del negocio — los datos salen solo de
  las tools MCP en runtime... Contenido: rol..., off-topic cordial sin tool calls...,
  personas por rol resuelto... Instalación: F1 instrucciones manuales exactas en la doc de
  pairing." — Criterio F1: off-topic responde cordial sin tools; "soy el dueño" no cambia
  persona; catálogo vacío responde sin inventar.
- **S7** (RF23 — doctrina, parte F1 skill base): "Hermes conduce cada contacto del Cliente
  hacia la resolución efectiva de su motivo desde el primer mensaje. La skill prescribe
  principios con ejemplos (no libretos literales)... y cierra siempre con una pregunta
  concreta — nunca dejar al Cliente sin próximo paso." — Criterio F1: saludo → conduce
  con pregunta concreta.
- **S8** (F1 DoD + infra, PRD §7 F1 + plan de CI acordado): "Demo reproducible documentada:
  WhatsApp → consulta → ticket creado → staff lista y transiciona." / "RF1–RF4 y RF5 (tipo
  fijo) con criterios verificados en CI." / "`go test -race ./...` verde; CI verde."
  Además (acuerdo 2026-11-26): el primer work-unit lleva go.mod + esqueleto mínimo que
  compila + `.golangci.yml` + `ci.yml` EN EL MISMO PR (auto-validado por pull_request), y
  el check `ci` se agrega al ruleset main-ci-gate inmediatamente después del merge
  (backup capturado antes). Contrato de canales documentado con el rigor del
  gateway-contract del proyecto de referencia — INCLUYE el canal e-mail, cuyo pairing no
  tiene verificación previa.

## Tasks

| ID | Specs | Route | Commit |
|---|---|---|---|
| T1 | S8 | Go module + cmd/server mínimo + .golangci.yml + ci.yml; pipeline verde; RDD; PR; ruleset += ci | 58e02d1 |
| T2 | S1 | Server HTTP loopback :3001 — /healthz 200, /mcp 405 POST-only, config fail-closed | (pend) |
| T3 | S2 | internal/auth — CallerResolver (accounts→clients→401), X-Caller-Id verbatim, anti-inyección, tests | (pend) |
| T4 | S2,S3 | SQLite db layer (modernc, WAL, busy_timeout, FK ON) + schema base §3.7: accounts, clients, business_profile, catalog_items, ticket_type fijo, tickets, ticket_events | (pend) |
| T5 | S3 | register_client seam anónima + rate-limit por identificador + colisiones fail-closed + alerta posible-duplicado | (pend) |
| T6 | S2 | Servidor MCP Streamable HTTP (go-sdk) + JSON-RPC errors (-32000/-32002/-32603) + tools/list filtrado por rol | (pend) |
| T7 | S4 | catalog_items + search_catalog (FTS5) | (pend) |
| T8 | S5 | Tickets tipo fijo + ticket_events auditados + re-engagement + transición por staff | (pend) |
| T9 | S6,S7 | Skill template (doctrina resolutiva) + doc de pairing (WA verificado + verificación e-mail) | (pend) |
| T10 | S8 | Tests de volumen (10k sintéticos) + demo reproducible + cierre F1 (DoD) | (pend) |

## Log

- L1 (2026-11-26, owner): "Dale con F1 ahora" — arranque autorizado de la Fase 1 tras
  cierre de F0 (PRD v1.10 aprobado, gobernanza instalada y auditada, protocolo RDD en
  AGENTS.md).
- L2 (2026-11-26, T1): entorno verificado (go 1.27.4, golangci-lint 2.14.0); issue #7
  aprobado; commit 58e02d1 en feat/f1-t1-ci-foundation con pipeline local verde
  (gofmt/vet/lint 0 issues/build/test -race 10 PASS). Nota técnica: golangci-lint v2
  no acepta gosimple (fusionado en staticcheck) — corregido en .golangci.yml.
