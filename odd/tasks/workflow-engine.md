# F2 — workflow-engine: configuración conversacional del negocio

> Phase 2 of the roadmap (docs/PRD.md §7, PRD v1.12). Goal: the Owner configures the
> business by conversation — ticket types, states, transitions, declarative rules — and
> the engine executes them with deadlines and alerts. `internal/domain` is BORN here
> (it does not exist today). Authorized by owner conversation 2026-11-27 ("Arrancar F2
> workflow-engine"); work-unit issue follows the standing grant after owner approval.

## Specs

Quotes are VERBATIM from docs/PRD.md v1.12 — the RF text is the spec; never paraphrase.

- **S1** (RF5 — Tickets con historial auditable, parte F2 configurable): "todo contacto
  con motivo de negocio genera un ticket — también las consultas que no terminan en
  compra ni solicitud (alimentan el funnel, el seguimiento y las estadísticas); la
  excepción es el mensaje sin motivo (off-topic puro). Política de **re-engagement**: si
  el Cliente vuelve sobre lo mismo con un ticket abierto de ese tipo, sus mensajes se
  agregan como eventos al ticket existente y no se crea uno nuevo; ticket nuevo solo
  cuando el anterior está cerrado. Toda transición, nota, mensaje de re-engagement y
  cambio de asignación queda en `ticket_events` con actor y timestamp;
  **estados/deadlines computan desde la config del tipo**." — **Prioridad**: "Must (F1
  tipo fijo → F2 configurable)". F2: tipos/estados/transiciones dejan de ser fijos y
  pasan por el motor configurable.
- **S2** (RF6 — Motor de workflows configurable, reglas declarativas): "el Owner define
  por UI (panel) o conversacionalmente (Hermes, con confirmaciones de doble gate para
  operaciones destructivas): tipos de ticket, estados, transiciones válidas y reglas
  declarativas — notificación on-enter/on-exit, `deadline` por estado con aviso overdue,
  auto-asignación round-robin del área, rol autorizado por transición. Sin scripting ni
  condiciones libres. La config inválida se rechaza con error semántico (fail-closed)."
  — Criterios: "Dado un estado con `deadline_hours=48`, cuando el scheduler corre pasada
  la fecha, entonces se genera una alerta `overdue` pendiente para el área asignada." /
  "Dada una transición que exige rol `staff`+, cuando un `client` intenta ejecutarla por
  chat, entonces es rechazada con `FORBIDDEN` semántico." / "Dada una config con
  transición a un estado inexistente, cuando el Owner la guarda, entonces se rechaza la
  config completa y nada queda parcialmente escrito." — **Prioridad**: "Must (F2)".
- **S3** (RF7 — Áreas configurables): "el Owner crea/renombra/elimina áreas y les asigna
  staff; cada tipo de ticket declara su área por defecto." — Criterio: "Dado un staff en
  las áreas 'Ventas' y 'Soporte', cuando entra al board, entonces ve los tickets no
  asignados de ambas áreas." — **Prioridad**: "Must (F2)".
- **S4** (RF8 — Visibilidad de tickets): "staff ve sus asignados + los no asignados de
  sus áreas; admin/owner ven todo; la misma regla aplica a las tools MCP de listado." —
  Criterio: "Dado un ticket asignado a otro staff de otra área, cuando un staff lista
  tickets, entonces ese ticket no aparece." — **Prioridad**: "Must (F2)".
- **S5** (RF9 — Operación conversacional de tickets): "internos operan tickets por el
  bot de Telegram: listar, consultar, transicionar, comentar, tomar un ticket no
  asignado de su área — cada acción pasa por tools MCP con su identidad y permisos." —
  Criterio: "Dado un staff autenticado por `request_contact`, cuando pide 'pasá el
  ticket 23 a en_proceso', entonces la transición se ejecuta con su identidad y el
  evento queda auditado a su nombre." — **Prioridad**: "Must (F2)".
- **S6** (RF11 — Notificaciones como alertas pendientes): "scheduler in-process (tick
  configurable) inserta alertas pendientes por: deadlines vencidos (overdue), reglas
  on-enter/on-exit, recordatorios de inactividad, tickets nuevos por área; Hermes las
  recoge con `get_pending_alerts` y las marca con `mark_alert_as_sent`. El backend jamás
  envía mensajes por sí mismo; si Hermes cae, las alertas se acumulan y se entregan a su
  vuelta." — Criterios: "Dada una alerta pendiente, cuando Hermes consulta y entrega,
  entonces queda `sent` y no se re-entrega en la siguiente pasada." / "Dado Hermes caído
  2 horas, cuando vuelve, entonces recibe todas las alertas acumuladas en orden, sin
  duplicados." — **Prioridad**: "Must (F2 esqueleto, F5 inactividad)". F2: overdue,
  on-enter/on-exit, nuevo-ticket por área; inactividad es F5.
- **S7** (RF19 — Canales, bot interno único de Telegram, parte F2): "un solo bot de
  Telegram compartido; cada interno hace `request_contact` una única vez; el teléfono
  compartido (solo el propio es compartible) resuelve contra `accounts`; desconocidos
  rechazados sin auto-registro; clientes por WhatsApp/e-mail con el canal del negocio."
  — Criterios: "Dado un teléfono que no matchea ninguna cuenta, cuando escribe al bot
  interno, entonces el rechazo ocurre en el gateway antes de cualquier tool call, con
  mensaje en español." / "Dado un staff ya resuelto, cuando escribe de nuevo días
  después, entonces no se le vuelve a pedir el contacto (resolución persistente)."
  — **Prioridad**: "Must (F1 pairing clientes / F2 bot interno)". Resolución (§3.8
  Q33b): "`request_contact` una sola vez por persona (Telegram solo permite compartir
  el propio número — no suplantable) → teléfono → match contra `accounts` → rol.
  Evolución futura documentada: binding por `telegram_user_id` con aprobación del
  Owner/Admin." — Nota: "el rechazo ocurre en el gateway" — Hermes es el gateway; el
  backend aporta la resolution seam y las tools; el bot de Telegram y su cableado de
  Hermes es documentación/pairing (D3: token del bot, owner).
- **S8** (F2 entregables + DoD, PRD §7 Fase 2 verbatim): "Entregables: entidades de
  configuración (tipos, estados, transiciones, reglas on-enter/on-exit, deadlines,
  round-robin por área) + validación íntegra fail-closed. / Tools MCP de administración
  para Owner (config de workflows por chat, doble gate en operaciones destructivas). /
  Scheduler in-process (deadlines → alertas overdue; on-enter/on-exit → alertas). /
  Áreas configurables + visibilidad de tickets + operación conversacional completa. /
  Plantillas seed **'venta'** y **'consulta'**: pipelines listos (venta: nuevo →
  contactado → presupuestado → cerrado/perdido, área Ventas con round-robin, evento
  'link enviado'; consulta: nuevo → atendido → resuelto). / Bot interno único de
  Telegram con `request_contact` + resolución per-emisor." — DoD: "Workflow configurable
  end-to-end por chat sin tocar código." / "RF6–RF9, RF11 (esqueleto), RF19 (bot interno)
  verificados." / "Tests de paridad del motor (config inválida rechazada íntegra;
  transiciones por rol)."
- **S9** (canon de schema §3.7, filas verbatim — la forma canónica acordada): "`areas`:
  Áreas de atención configurables — nombre, staff miembros (N:M con `accounts`)." /
  "`ticket_types`: Tipos de ticket configurables — nombre, `default_area_id`; plantillas
  seed **'venta'** y **'consulta'** provistas ('venta': nuevo → contactado →
  presupuestado → cerrado/perdido, área Ventas con round-robin, evento 'link enviado';
  'consulta': nuevo → atendido → resuelto, sin área fija)." / "`ticket_states`: Estados
  por tipo — nombre, `deadline_hours` opcional, reglas on-enter/on-exit declarativas." /
  "`ticket_transitions`: Transiciones válidas — estado origen → destino, rol autorizado,
  auto-assign round-robin opcional." / "`tickets`: Instancia de contacto — tipo,
  cliente, estado actual, área, asignado, deadlines computadas." / "`alerts`: Alertas
  pendientes — tipo (overdue/on-enter/on-exit/inactividad/nuevo-ticket/posible-duplicado/
  seguimiento-lead), target (cliente o account), estado (pending/sent), payload."

## Tasks

| ID | Specs | Route | Commit |
|---|---|---|---|
| T1 | S2, S9 | internal/domain birth: workflow config entities + `Validate` fail-closed + parity tests | 9117c9b (move) + 86cbf8a (workflow) |
| T2 | S9 | migration v5: `areas`, `ticket_types`, `ticket_states`, `ticket_transitions`, `alerts` + seeds 'venta'/'consulta' | — |
| T3 | S1, S2 | engine: creation/transition/deadlines via config, round-robin, events audited, FORBIDDEN por rol | — |
| T4 | S2, S3 | tools MCP de administración Owner: config de workflows por chat, doble gate destructivas, áreas CRUD | — |
| T5 | S4 | visibilidad RF8 en listados MCP (staff: propios + no asignados de sus áreas) | — |
| T6 | S6, S2 | scheduler in-process: overdue + on-enter/on-exit + nuevo-ticket → `alerts`; `get_pending_alerts`/`mark_alert_as_sent` | — |
| T7 | S7, S5 | seam de resolución Telegram (request_contact → accounts) + docs pairing bot interno (D3: token owner) | — |
| T8 | S8 | docs + demo F2 + DoD verificado + pipeline completo | — |

Routes refined with the explorer code map (2026-11-27, evidence path:line):

- **Patterns to follow** (existing F1 conventions): use case = struct of ports +
  `New<X>(ports...)` rejecting nil ports (`usecase/tickets.go:255`); in-usecase RBAC via
  hand-written `require*` helpers (`usecase/tickets.go:213-242`) — `auth.RequireRole` does
  NOT exist despite AGENTS.md; tools = `Tool{Definition, Handler, Access{Roles}}` gated by
  `registry.gate/authorize` (`mcp/registry.go:124,168`); SQL = static constants + `?`
  placeholders, guarded transitions inside `BeginTx` (`repository/tickets.go:258`);
  migrations appended to the `migrations` slice in `internal/db/schema.go` (next: v5);
  scheduler goroutine hosted in `run()` beside `errCh` with ctx cancelled on SIGINT/SIGTERM
  (`cmd/server/main.go:45-89`).
- **Reserved seams for F2** (already documented in code): deferred columns
  `schema.go:23,351,375-381` (`ticket_states`, `ticket_transitions`, `ticket_types.default_area_id`,
  `tickets.area_id/deadline_at/closed_at`); `ticket_events.actor='system'` reserved for the
  engine (`schema.go:420`); `DuplicateNotifier` no-op (`usecase/register_client.go:54,121`);
  `tickets.updated_at` is the activity clock; `db/schema_test.go:56` asserts the F2 tables
  are absent — migration v5 flips those assertions.
- **State vocabulary is F2-replaceable**: `dto/ticket.go:17-35` (`TicketState`, `Valid`, `Open`,
  `Closed`) — T3 replaces fixed states with config-driven ones without breaking the
  closed-ticket guards.

## Log

- **L1** (2026-11-27, owner, verbatim): "Hola capo, sigamos con nuestro proyecto
  crm-ai-business desde donde quedó ayer.. dime que sigue"
- **L2** (2026-11-27, owner): Seleccionó "Arrancar F2 workflow-engine" — autoriza el
  inicio de F2 (draft de issue para su aprobación + feature doc + primer work unit).
  State at start: main @ 0e08277 (PRD v1.12 Tailcat), worktree limpio, 0 PRs/0 issues
  abiertos, CI verde, RDD sin candidatos pendientes.
- **L3** (2026-11-27, evidence): explorer code map (gentle-ai-explore, read-only). Key
  facts: `internal/domain` does not exist — ports live in `usecase` (`TicketStore`
  `usecase/tickets.go:47`), `SemanticError` lives in `usecase/errors.go:104`; no
  `internal/scheduler`, no `alerts` table, no Telegram code. Backlog riders relevant to F2:
  R1-transition-state-decode (enforce enum in `mcp/tickets.go:573` decoder — T3/T4 touch
  that file), CallerError placement + R2-sentinel-type (next internal/auth touch),
  R3-listresult-iserror (next mcp test touch). D3 (Telegram bot token) is an owner
  dependency for T7 pairing; the backend seam does not need it.
- **L4** (2026-11-27, evidence): Issue #37 created (one attempt, host permission verified
  `admin` ≥ MAINTAIN) with labels `type:feature` + `status:approved` (standing grant:
  owner approved the draft in this conversation). Read-back confirmed: OPEN, author egkike,
  labels/title/body verified — classification `confirmed`.
- **L5** (2026-11-27, decision): T1 moves `SemanticError` + error codes from
  `internal/application/usecase` to `internal/domain` (AGENTS.md layer map: domain owns
  `SemanticError` + error codes) with a type alias in usecase for compatibility; workflow
  validation errors are born as `domain.SemanticError`. The CallerError placement decision
  (internal/auth) stays separate, per backlog.
- **L6** (2026-11-27, evidence): T1 RED→GREEN observed. RED: stub `Validate`
  returned nil — every rejection subtest failed; templates failed shape assertions.
  GREEN: full rule table passes (`go test ./internal/domain/`), full suite green
  (usecase/auth/db/mcp/repository). The errors move proved behavior-neutral:
  `go test ./internal/application/... ./internal/mcp/...` green with F1 expectations
  unchanged (the mcp sanitization branch still matches the aliased type — same type,
  zero copies). Differential reasoning: F1 tests pin exact codes/messages through the
  public surface; they pass byte-identical, so no unrequested behavior changed.
  Test-fix note: the transition-cap test initially tripped the state cap first — the
  engine was right, the test construction was wrong (fixed: unique pairs within the
  50-state cap).
- **L7** (2026-11-27, evidence): T1 RDD closed. Native assess (baseRef
  0e08277a774c8440ca1a72ca4bdba2e4f71755d2): risk **high**, `reviewDue: true`
  (reasons: `.golangci.yml` configuration change + agent escalation item 3 —
  shared SemanticError machinery). Native review **review-8fc68db721abce52**
  ran 3 lenses (risk/readability/reliability) → **approved**, receipt burned
  (acknowledge-approved completed; verify-always-rdd-high satisfied: full
  `-race` pipeline green + native review). 7 advisory findings, all
  informational, none opened a correction → backlog entries R3-1/R3-2/R3-3
  (Pending riders into T3/T4/T6), R2-001..R2-004 (next-touch/no-action).
