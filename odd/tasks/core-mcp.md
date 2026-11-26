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
| T6 | S1,S2,S3 | MCP Streamable HTTP (SDK v1.7.0) + X-Caller-Id middleware + role-gated registry + composition wiring | (este PR) |
| T7 | S4 | FTS5 v3 (v1.11 fields) + CatalogRepo + 3 usecases + 3 tools + 5 riders db + wiring | (este PR) |
| T8 | S5 | Tickets core: dto + 5 usecases double RBAC + TicketsRepo + corrección PII (R3-mask-split-run) | 59b73f9 + 24894ac (PR #27 → d2ad761) |
| T8b | S5 | Transport: 5 tools role-gated + wiring + transport E2E | (este PR) |
| T9 | S6,S7 | Skill recepcionista (doctrina resolutiva) + pairing doc (**WA verificado; canal e-mail PENDIENTE de verificación** — no se declara operativo) | 1ad5cb4 |
| T10 | S8 | Techo 10k/100k validado en CI + demo reproducible + README/cierre DoD | (este PR) |

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
  (validación de args per AGENTS). SLICE: commit único cohesivo. Issue #19. MERGED
  e85a352 (PR #20, review-923a035dd8dd3224 APPROVED/burned, 4 advisories triaged:
  oracle RESUELTO por owner con threat model documentado).
- L10 (2026-11-26, T6): DELEGADO (4ª) a gentle-ai-worker: internal/mcp — servidor
  MCP Streamable HTTP sobre SDK oficial v1.7.0 (stateless + JSONResponse —
  identidad per-request by construction), middleware X-Caller-Id verbatim →
  CallerResolver → -32000 con Code() en data, register_client como seam anónima
  (schema additionalProperties:false, caller-id del context jamás del body),
  role-gated registry (tools/list + tools/call gating; sample owner-only tool
  pinnear S2), sanitización ejecutable -32000/-32002/-32603 (CIERRE del advisory
  R3-001 de T3: internals jamás al wire, test con puerto fallando), 405 non-POST
  con Allow. 21 tests / 40 subtests. WIRING INLINE DEL ORQUESTADOR (mi mitad de
  T6): buildMCPHandler en cmd/server (db.ResolvePath/Open → CallerLookup →
  resolver → ClientsRepo → resolveRegistrationLimit fail-closed (MCP_REGISTRATION_
  RATE_LIMIT: empty=10, 0=disable, inválido=refuse) → RegisterClient →
  mcp.NewHandler → newMux mounts), stub 501 RETIRADO (el handler propio posee los
  405), tests del composition actualizados (routing + registration limit + loopback).
  Fixes orquestador: misspell += conversacional, argumentos (5ª y 6ª palabras).
  ORÁCULO RESUELTO PREVIO por el owner: mensajes distintivos + trade-off documentado
  (X-Caller-Id inyectado por gateway confiable). SLICE: worker+mío en un commit
  (unidad transport+wiring inseparable). Issue #21. MERGED 5459aa5 (PR #22,
  review-f90b8b8884381ad5 APPROVED/burned — 3 advisories; incidente de admisión
  reliability resuelto canónicamente: payload ambiguo preservado → STATUS → mismo
  binding → nuevo payload admitido).
- L11 (2026-11-26, T7): DELEGADO (5ª) a gentle-ai-worker: catálogo completo —
  FTS5 migration v3 external-content (índice sobre name+description+use_cases+
  limitations — v1.11: EL MATCH NACE DEL PROBLEMA DESCRITO; tokenize unicode61
  remove_diacritics 2; 3 triggers de sync), CatalogRepo (CRUD+search prepared,
  ftsExpression quoting contra inyección de gramática FTS5, browse para query
  vacío, money INTEGER), Search/Create/Update usecases con double RBAC (re-assert
  in-usecase), 3 MCP tools (search_catalog todos los roles resueltos;
  create/update owner+admin invisibles+ininvocables para el resto), + LOS 5 RIDERS
  db del review T4 RESUELTOS ESTRUCTURALMENTE: R2-003 test movido a open_test,
  R2-004 seeds ahora placeholders por construcción con validateSeeds arity guard,
  R3-001 buildDSN con tabla de rechazos (NUL/?/#, params _-prefixed, url.Values
  Encode), R3-003 appliedVersions COUNT/MAX con aritmética de prefijo pineada,
  R3-004 rollback honesto (cierra handle, borra SOLO lo que creó, respeta
  pre-existing). WIRING INLINE MÍO: buildMCPHandler += CatalogRepo + 3 usecases
  + Catalog deps (sin wiring el binario no exponía las tools). Pipeline verde
  (7 paquetes -race, lint 0 issues tras problemas+columnas — 7ª y 8ª palabras).
  ~3.8k líneas. Issue #23.

- L14 (2026-11-26, T10 — cierre F1): techo de escala VALIDADO en CI
  (internal/db/volume_test.go: 10.000 clientes + 100.000 tickets + sus eventos en
  una transacción; mediana medida — resolución de identidad 24µs, búsqueda FTS
  132µs, presupuesto del PRD 200ms); demo reproducible documentada (docs/demo.md,
  siete pasos contra el binario real, cada afirmación del PRD mapeada a su paso);
  README actualizado (F1 entregada, índice de docs, cómo correr las pruebas).
  DoD de F1: demo reproducible ✅ · RF1–RF4 y RF5 (tipo fijo) verificados en CI ✅ ·
  go test -race verde ✅ · CI verde ✅. HONESTIDAD: el canal e-mail sigue
  PENDIENTE DE VERIFICACIÓN contra el gateway real (docs/pairing.md §3) — no se
  declara operativo.

- L15 (2026-11-26, sweep post-F1): issue #33, branch refactor/post-f1-advisory-sweep.
  DELEGADO a gentle-ai-worker con 5 fixes; el worker reportó status
  `interaction_required` (honesto: fix 5 medía 634ms/1.354s bajo -race vs el
  presupuesto de 200ms) y ofreció tres opciones — el orquestador eligió la 1
  (arreglar la causa, no saltar el test) y la ejecutó inline: migración v4
  `tickets_activity_idx (updated_at, id)` + división del statement de listado en
  dos vistas (el disjunct `(? = '' OR client_id = ?)` impedía el seek del índice).
  Resultado medido bajo -race, todo dentro de 200ms: 993µs / 971µs resolución,
  6.2ms catálogo, 2.44ms listado por cliente, 83.5ms listado global, 1.55ms
  transición. El índice no es un parche para el test: el listado a 100k era O(n)
  en producción (13ms medidos, degradándose con el volumen).
  Fixes entregados: nota atómica (append+touch+guard en una tx), bounds en los
  decoders (50/120/2000 → invalid_arguments), client_id rechazado para rol
  cliente (defensa en profundidad), test del cierre del handle con control
  positivo, y el techo midiendo listado y transición. Suite completa verde con
  -race (internal/db 63s por el techo: esperado).
