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
| T1 | S8 | Go module + cmd/server mínimo + .golangci.yml + ci.yml; pipeline verde; RDD; PR; ruleset += ci | 58e02d1 (PR #8 → 677a7d1) |
| T2 | S1 | /mcp POST-only 405 + ErrServerClosed fix + boundary tests + rider docs | (este PR) |
| T3 | S2 | internal/auth — CallerResolver §3.8 fail-closed, port anti-inyección, sentinels para T6 | 1999079 |
| T4 | S2,S3 | SQLite db layer + migration runner append-only + F1 schema (option B) + CallerLookup adapter | (este PR) |
| T5 | S3 | register_client usecase + clients repo + rate limiter + seam F2 | (este PR) |
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
- L3 (2026-11-26, T1 merge): PR #8 squash-mergeado como 677a7d1 (Closes #7); CI de
  GitHub verde en su primera corrida (1m11s, run 45884152670); ruleset main-ci-gate
  actualizado: required checks = pr-gate + ci (backup previo, read-back verificado).
  Review T1 (review-15ebf00d81654095) APPROVED/burned; advisories triaged en
  odd/tasks/backlog.md (sweep 1 ya mergeado como 421b8a1, PR #10).
- L4 (2026-11-26, sweep): advisories de gobernanza barridos en PR #10 (421b8a1,
  Closes #9, review-10f6baa897a67c71 APPROVED/burned); backlog doc creado.
- L5 (2026-11-26, T2): issue #11 aprobado; /mcp POST-only con Allow header (405),
  POST responde 501 semántico hasta T6; plegados R3-001 (filtro ErrServerClosed +
  deadline explícito del Shutdown) y R3-002 (boundaries 1/65535); rider: este Log y
  backlog.md actualizado.
- L6 (2026-11-26, T3): DELEGADO a gentle-ai-worker (backstop de contexto) con
  handoff por referencia (T3/S2); worker entregó internal/auth completo (826 líneas:
  Role/Caller/CallerLookup port/CallerError español/CallerResolver fail-closed con
  guards de fila corrupta + 16 tests + 10 subtests, pipeline verde). Verificación
  propia: diff revisado, boundary respetado (solo internal/auth/**), pipeline
  re-corrido. Fixes del orquestador: mensaje canónico "no se proporcionó X-Caller-Id"
  (contrato del repo de referencia; el worker lo había reworded por el linter) +
  test actualizado + .golangci.yml misspell ignore-rules (clave v2; ignore-words no
  valida) para textos españoles. SLICE: un solo commit de 838 líneas (paquete
  único cohesivo — partirlo fragmentaría tests que referencian el paquete entero);
  boundary registrado acá per ODD. Issue #13; PR con rider backlog (advisories T2 +
  decisiones CallerError→domain para T4/T6).
- L7 (2026-11-26, v1.11): pre-T4 el owner aprobó conocimiento de catálogo orientado
  al agente — catalog_items gana use_cases y limitations (PRD v1.11, PR #16 → 07ecd67,
  assess passive + readback).
- L8 (2026-11-26, T4): owner eligió scope B (F1-only + migration runner append-only).
  DELEGADO a gentle-ai-worker: internal/db (Open con pragmas DSN, MCP_DB_PATH
  fail-closed, runner versionado con rechazo de huecos, schema F1 v1+v2 con seed
  'consulta') + internal/repository (CallerLookup adapter, accounts-first, guard de
  ambigüedad, is_active estricto) + E2E resolver+SQLite. ~880 prod + ~1130 test líneas.
  Desviaciones de calidad aceptadas: price_cents INTEGER (dinero sin float), índice
  unique lower(email) en accounts, ticket_events AUTOINCREMENT (audit append-only),
  deferrals documentados por fase. Fixes del orquestador: doc del port auth alineado
  a §3.8 accounts-first, misspell += producto (nolint del worker dropeado). SLICE:
  un solo commit (unidad schema+migración+adapter inseparable); boundary registrado.
  Issue #17; PR con rider backlog (6 advisories T3 + fixes). MERGED 44e9654 (PR #18,
  review-31ae9e99067acc33 APPROVED/burned, 10 advisories triaged en backlog).
- L9 (2026-11-26, T5): DELEGADO (3ª) a gentle-ai-worker: primer código de la capa
  application — RegisterClient usecase (seam anónima: identidad SOLO del caller-id,
  sin parámetro de rol por diseño; cadena empty→254bytes→limiter→accounts→pre-check→
  insert; colisiones fail-closed mapeadas a semántico español; NUNCA auto-vincula;
  DuplicateNotifier seam documentado para F2 con claimingClientID='' en la seam
  anónima) + dto carriers + MemoryRegistrationLimiter (0=disabled fail-closed, clock
  inyectado, bound 100k) + ClientsRepo (UNIQUE exact+lower(email) clasificados, PK
  1555 queda error interno) + RIDER R2-002 (comentario mentiroso corregido, behavior
  intacto) + E2E: resolver-unknown→register→resolver-resuelve (la cadena §3.8 cierra
  con admisión). 99 tests PASS -race. Worker status PARTIAL honesto: bloqueado solo
  por misspell 'clientes' (24 hits) fuera de su surface → fix orquestador (ignore-rules
  += clientes, tercera palabra del patrón). Desviación aceptada: guard >254 bytes
  (validación de args per AGENTS). SLICE: commit único cohesivo. Issue #19.
