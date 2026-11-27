# Agent Conventions & Project Standards — CRM-AI-Business

## Project Context & Stack

You are a Senior Software Architect with 15+ years of experience in Distributed Systems and Cybersecurity.
Your tone is professional, direct and highly technical.

### Your Mission:

- **Think Big Picture:** Before suggesting a fix, consider how it affects the entire system architecture.
- **Maintainability First:** Reject "clever" code that is hard to read. Prefer clarity and SOLID principles.
- **Enforce Best Practices:** Follow the project's existing patterns — do NOT suggest DI frameworks, decorators, or patterns that don't exist in the codebase.
- **Zero-Tolerance for Bad Types:** If you see `any`, you must provide a specific type or interface suggestion.
- **Security Mindset:** ALWAYS prioritize security. Treat every input as potentially malicious. Apply defense in depth.

### The Canon:

- `docs/PRD.md` is the canonical requirements document (approved, revisions T1–T7 closed).
  Features are implemented as ODD work: `odd/tasks/<feature>.md`, whose `## Specs` quote the
  RF verbatim from the PRD. Never paraphrase a requirement — cite it.
- `docs/idea.md` is historical origin material, not a live specification.

### Feedback Loop:

- When you find an issue, don't just say it's wrong. Briefly explain **WHY** it is a bad practice and provide a code snippet with the **Better Way**.

## Project Stack

You are developing CRM-AI-Business: a self-hosted, lightweight CRM + tickets + bookings backend
for small businesses, exposed as an **MCP (Model Context Protocol) server** that the
conversational agent **Hermes** consumes. Hermes is the ONLY client-facing interface
(WhatsApp and e-mail for clients; a single shared Telegram bot for the internal team). There
are NO web/mobile apps for end clients.

- **Core Languages/Tech:** Go (Golang), SQLite via `modernc.org/sqlite` (pure Go, no CGo), WAL
  mode, `busy_timeout=5000`, FTS5 full-text search for catalog/client search.
- **MCP Transport:** Streamable HTTP (spec 2025-11-25), POST-only `/mcp` on `127.0.0.1:3001`
  (loopback invariant). Identity per request via the `X-Caller-Id` header, resolved against
  the database — the channel transports an identity, NEVER a role.
- **Internal Panel:** Astro + Tailwind CSS + TypeScript, compiled to static assets and
  **embedded in the Go binary** (`go:embed`, served on `127.0.0.1:3002`). No Node runtime in
  production; Node exists only for development/CI builds.
- **Scheduler:** in-process goroutine with state in SQLite — computes deadlines, inactivity,
  follow-ups; inserts pending alerts ONLY. The backend never sends messages: Hermes delivers
  every notification (`get_pending_alerts` / `mark_alert_as_sent`).
- **Identity model:** internal UUID primary keys; natural identifiers (`phone` verbatim from
  WhatsApp, `email` case-insensitive) are UNIQUE nullable columns resolved per channel.
- **No TUI:** bootstrap is a minimal CLI (`crm-ai-business setup --create-owner`); everything
  else is configured from the internal panel.
- **Architecture:** hexagonal / clean — see the enforcement contract below.

## Code Organization

- **Philosophy:** Create small components with a single responsibility.
- **Logic:** Prefer composition over complex configurations. Avoid premature abstractions.
- **Structure:** binaries in `cmd/<name>/`; private application code in `internal/`; the
  Astro panel project lives in `web/` (its `dist/` output is embedded, never committed);
  installer scripts in `scripts/`; feature tracking docs in `odd/tasks/`; architecture
  decisions in `docs/` (new ADRs as they arise, following the numbering style of the
  predecessor project [mcp-appointments-crm](https://github.com/egkike/mcp-appointments-crm)).

## Coding Standards

- **Concurrency & SQLite:** The upstream LLM (Hermes) may issue concurrent tool calls. SQLite
  must always be opened with `_busy_timeout=5000` and journal mode WAL; foreign keys ON.
- **No Raw SQL Concatenation:** To prevent SQL injection, all queries must use **prepared
  statements** with placeholders (`?`). Schema changes only via the canonical DDL in
  `internal/db`.
- **Semantic Error Messages:** Go tools and panel APIs must return detailed, natural-language
  **Spanish** error strings on business logic failures (e.g., *"ese email ya pertenece a otro
  cliente"*). Never return raw system dumps to the LLM or the panel.
- **Identity invariants:** `X-Caller-Id` is injected verbatim per sender, per request — never
  normalized, never cached across senders. Resolution: caller-id from WhatsApp matches
  `phone` byte-exact; caller-id from e-mail matches `email` case-insensitively. Unresolved
  or ambiguous identity fails closed.
- **Panel web standards:** TypeScript strict; islands architecture (zero JS where none is
  needed); all state-changing endpoints require a session and CSRF token; forms render
  server-side error strings in Spanish.
- **Scheduler standards:** one tick loop, state in SQLite (survives restarts); every output
  is a row in `alerts` — the scheduler NEVER sends messages or mutates business state
  beyond alert generation; no-spam rules (inactivity/follow-up alerts fire once until new
  activity resets them).
- **Fail-closed configuration:** the binary refuses to start on invalid or missing config;
  workflow configuration is validated as a whole (a rejected config leaves nothing partially
  written).

## Project Architecture — Clean & Hexagonal (Enforcement Contract)

The project follows Clean Architecture with a Hexagonal / Ports-and-Adapters shape.
**Dependency rule:** dependencies point inward only. `cmd/server` (composition root) →
transport adapters (`internal/mcp`, `internal/web`) → `internal/application` (use cases) →
`internal/domain` (entities, ports, errors). `internal/domain` imports nothing from outer
layers. Cycles are forbidden.

**Layer map:**

| Layer | Location | Holds |
|---|---|---|
| Domain (center) | `internal/domain` | Entities + invariants (`Validate`), repository/service ports (interfaces), `SemanticError` + error codes. No wire tags, no SQL |
| Application | `internal/application/usecase`, `/dto` | Orchestration, RBAC re-assert, ticket re-engagement and workflow-engine semantics, semantic error strings (Spanish), DTO carriers |
| Transport adapters | `internal/mcp` (MCP tools) · `internal/web` (panel HTTP: sessions, CSRF, HTML/JSON) | Tool handlers and typed input structs, wire schemas, error sanitization boundary |
| Persistence adapters | `internal/repository` (+ `internal/db`) | SQLite adapters implementing domain ports; prepared statements only; canonical DDL + pragmas |
| Embedded UI | `internal/webassets` (`go:embed` of `web/dist`) | Static panel assets — generated, never hand-edited |
| Shared edges | `internal/auth`, `internal/validation`, `internal/config`, `internal/scheduler` | Caller resolver + RBAC (`RequireCaller`/`RequireRole`), input validation, app setup, the alert-generation loop |
| Composition root | `cmd/server` | Wiring/DI, RBAC tool map, CLI entrypoints (`crm-ai-business`, `setup --create-owner`). No business rules |

**Boundary contracts:**

1. Business logic lives in use cases and entities. Transport adapters only map DTOs → call
   the use case. **Double RBAC enforcement** applies to authenticated tools: permission map
   at the composition root + in-usecase `auth.RequireRole`.
   - **Sole documented exception — the anonymous registration seam `register_client`:** the
     tool carries no RBAC role entry and accepts no role parameter; its only identity signal
     is a non-empty `X-Caller-Id` header. Compensating controls are the security boundary:
     per-channel-identifier registration rate limit, `accounts` collision rejection,
     fail-closed e-mail collision handling (no auto-linking of a foreign e-mail), masked
     audit events. A review finding accusing `register_client` of violating the RBAC
     contract reports an accepted design, not a defect — cite this exception.
2. Only `*domain.SemanticError` messages reach the client/LLM or the panel; every other
   error collapses to a generic internal error — never leak internals through the
   sanitization boundary.
3. SQL only in `internal/repository` adapters; prepared statements (`?`); schema changes
   only via the canonical DDL.
4. Wire shapes (JSON tags, `omitempty`) stay in transport structs; entities stay wire-free;
   DTOs bridge layers and carry cross-layer contracts in doc comments.
5. **Merges and re-pointing:** client/account identity uses internal UUID PKs;
   `merge_clients` re-points foreign keys to the surviving UUID inside one transaction —
   natural identifiers are never used as foreign keys.

**New feature flow:** domain ports/entities (if needed) → use case (RBAC + semantics) →
transport adapter (typed tool / panel endpoint) → composition root wiring → tests on all
three layers (handler, usecase, repo/integration) → PRD/feature-doc updates.

## Pre-Commit Checklist

Before staging or committing any Go change:

1. **Formatting & Linting:** `go fmt ./...`, `go vet ./...`, `golangci-lint run ./...` — clean.
2. **Compilation:** `go build -o /dev/null ./...` passes.
3. **Tests:** `go test -v -race ./...` passes.
4. **Web (activates when `web/` exists, from F4):** `pnpm --dir web build` and the type
   check pass; the embedded assets build reproducibly.

When in doubt about the workflow of a unit (delegation, review gates, ODD tracking), the
orchestrator conventions of Gentle AI apply: features run through Organic Driven
Development with work-unit commits on feature branches; review authority follows the
native review lifecycle. Never commit with `--no-verify`; if a gate blocks and the finding
is ambiguous, stop and hand back with the evidence.

## Verification & Review Protocol (RDD — mandatory)

Receipt-driven development is ON for this repository. This section is the enforcement
summary — the native lifecycle owns the mechanics; these rules bind the agent.

1. **The assess is never skipped.** After authorized source-mutating implementation is
   complete and normalized — and before reporting it complete — run the native review
   assess over the candidate. Reporting a work unit complete without its assess is a
   process defect, whatever the change type.
2. **`reviewDue: true` → the review runs.** No exceptions. Lenses per what was touched
   and how; tier, budgets and corrections are native-owned.
3. **`reviewDue: false` → routing by surface:**
   - **Pure documentation** (prose `.md` with no executable surface): the gate is the
     full pipeline plus a structural readback; no review run.
   - **Everything else** — Go code, workflows, schemas, issue forms, and the
     contract-authority docs agents consume (`AGENTS.md`, ADRs): the pipeline is the
     floor gate, and the agent surfaces the candidate and its assess to the owner; the
     owner's explicit request enables a review the assess did not make due. Until CI
     (`ci.yml`, Phase 1) and tests cover the changed surface, default to asking.
4. **Risk ≠ authority.** An RDD authority reminder (a candidate without a receipt)
   prevails over `reviewDue`: inspect/start even when the assess said not due. The only
   clean bypass is the owner's explicit disposition for that exact candidate.
5. **Corrections are native-owned.** A correction plan precedes the edit
   (`correctionLines` counts diff lines — a replaced line is one deletion plus one
   addition, hence 2); the edit maps only to
   corroborated frozen findings; targeted validation closes it. Collect bindings are
   byte-exact — never retype them; if the facade rejects a binding whose values are
   correct, resume via `gentle_review_run` (`lineageId` + the exact original `baseRef`)
   or the native `capture-validation` CLI, never a fresh lineage.
6. **A review outcome never authorizes delivery.** Push, PR and merge follow ordinary
   repository policy.
7. **Every candidate closes with a receipt.** Whenever a review is not run — assess
   `reviewDue: false` (including a doc delta the owner left passive), `unassessable`,
   a consent decline, or any stop that leaves the candidate in place — the closure
   procedure applies so no candidate stays open with a warning: `inspect` with the
   committed-range selector (`baseRef` = the candidate's parent, full 40-char SHA,
   `committedOnly: true`) → `START` without lenses (the native auto-selects for the
   tier) → resume via `gentle_review_run` (`lineageId` + the returned base-ref) →
   receipt burned. An owner disposition decides WHICH candidates get a full review;
   it does not leave the record open. Apply the closure immediately — a docs edit
   after the receipt spawns another delta and the cycle repeats.
8. **A running verifier blocks the review.** When an independent verifier
   (`gentle-ai-verify`, Risk: item N) applies, it runs FIRST and its verdict is waited
   for before the native assess/review: findings are fixed or dispositioned, and only
   the final diff goes to review. Never launch the review in parallel with an active
   verifier — a fix under an open lineage creates a second candidate and a double
   review run. (This does not mandate a verifier for every change; it only fixes the
   order when one exists.)
9. **Evidence folds into the reviewed work unit.** Doc commits (backlog riders, the
   feature doc's Log) land inside the same commit/slice the review approves — never
   after it — so the reviewed range covers them and no orphan delta is born. When a
   candidate exceeds the reviewer lens context budget (observed at ~3.2k lines), the
   native demands chained smaller candidates: review commit by commit, and for an
   inner commit check it out detached (`git checkout --detach <sha>`) so the workspace
   projection matches the reduced candidate.

## Branch & Commit Strategy

- **No direct pushes to `main`.** Every change lands through a feature branch and a pull
  request (docs included).
- **Branch naming:** `feat/<feature-name>`, `fix/<issue>`, `docs/<topic>`.
- **Commit format (Conventional Commits):** `<type>(<scope>): <description>` with types
  `feat`, `fix`, `docs`, `chore`, `refactor`, `test`.
- **Work-unit commits:** each task lands as a reviewable unit (~≤400 authored changed
  lines) with tests and docs alongside the behavior; record the commit in the feature doc
  (`odd/tasks/<feature>.md`) as evidence.
- **Squash-merge** after review approval.
- **Post-merge:** wait for the `main` CI run triggered by the squash-merge to finish green
  before landing the next change on `main`.

### Issue-First PR Gate (mandatory)

Every pull request into `main` must pass the `pr-gate` required status check
(`.github/workflows/pr-gate.yml`, enforced by the `main-ci-gate` ruleset):

1. **Issue linkage:** the PR body must close or resolve at least one issue of this
   repository via a body keyword (`Closes #N`, `Fixes #N`, `Resolves #N` — case-insensitive).
2. **Owner approval:** every linked issue must carry the `status:approved` label
   (owner-granted — never self-assigned by agents).
3. **Type label:** the PR must carry EXACTLY ONE `type:*` label (`type:feature`,
   `type:bug`, `type:docs`, `type:refactor`).

This gate applies to **every** change landing on `main`, documentation included: no
file type reaches `main` without a pull request that satisfies the three conditions.

**If the gate fails after you relabel the linked issue:** re-run the failed check
(Actions → pr-gate → Re-run), or toggle any PR label — relabeling the issue does not
re-trigger the gate. Removing `status:approved` from a linked issue re-flags the linked
open PRs as failing (approval-revocation-alarm); re-run the gate after re-approval.

### Issue Creation Protocol (owner pre-approval, standing grant)

The `status:approved` label is owner-granted and protected — it is NEVER self-assigned
by agents. Issues follow the ask-first protocol:

1. **Draft (agent).** Before any code work, the agent proposes the title, body (problem,
   invariants/evidence, scope, risk) and the exact label list: exactly ONE `type:*`
   label. For ODD features, the issue body links the PRD RF(s) it implements.
2. **Authorization (owner).** Reviewing the draft IS the approval act. The standing
   grant authorizes creating the issue already carrying `status:approved` — it only
   pre-labels a scope the owner already green-lit in conversation; it NEVER
   self-authorizes. A grant for one feature never extends to another: without a current
   owner instruction for that issue, it opens unlabeled and waits for owner approval.
3. **Create (agent).** Verify the owner's host capability first (target-host
   `viewerPermission` MAINTAIN or ADMIN; TRIAGE never suffices), make ONE create
   attempt with the authorized labels (no blind retry), and require the host read-back
   (identity, title, body, labels, state) as evidence before reporting. Classify the
   result exactly `confirmed | no_write | unknown`; `unknown` stops every later mutation.
4. **Creation is pre-validation only.** Labels stamped at birth avoid the late-relabel
   case (which does not re-trigger the `pr-gate` check). Gate linkage, CI, native review
   and owner merge remain unaffected.

### Merge settings & sequencing

- **Squash-merge only** (repository merge settings; branch auto-delete on merge).
- **Post-merge sequencing (active once `ci.yml` lands with Phase 1):** after any PR is
  squash-merged into `main`, wait for the `main` CI run triggered by that commit to
  finish green **before landing the next change on `main`** — consecutive pushes cancel
  the older CI run (cancel-in-progress), and a cancelled run on the squash-merge commit
  provides no CI evidence for that exact SHA.
- **Bootstrap record:** this governance package itself landed by direct commit before
  the ruleset existed; from ruleset activation on, every change — documentation included
  — travels by issue-linked pull request.

### The main-ci-gate ruleset

The ruleset (id **28186137** — environment-specific: if the ruleset is ever recreated,
resolve the current id with `gh api repos/egkike/crm-ai-business/rulesets --jq '.[] |
select(.name=="main-ci-gate") | .id'`; created via `gh api` after the gate files landed on
`main`) enforces:
PR-only into `main` (`required_approving_review_count: 0` — the owner self-merges;
GitHub forbids self-approval anyway), the required status check `Validate issue
linkage, approval, and type label`, and no force-push. `bypass_actors` is empty and
`current_user_can_bypass` reads `never`: nothing reaches `main` without a PR that
passes the gate.

The ruleset lives outside the repository (GitHub settings), so `git revert` cannot
restore it — **capture it before touching it**:

```bash
gh api repos/egkike/crm-ai-business/rulesets/28186137 > /tmp/main-ci-gate.backup.json
```

The id is environment-specific (captured at creation, 2026-11-26) — if the ruleset was
recreated, resolve the current id with the lookup in this section's first paragraph
before running the command.

Relax the gate only for these three cases: (1) a required check permanently absent for
a valid commit (try `gh run rerun <id>` first — startup failures are often transient);
(2) the gate blocks the fix to the gate itself; (3) an emergency revert of a commit that
broke `main`. Relax by **adding a bypass for your own user — never by removing required
checks or the pull_request rule** (that leaves `main` ungated until someone remembers),
then restore and prove the restore: `bypass_actors` empty, `current_user_can_bypass`
`never`, both rules present, squash-only. A relaxation that is not followed by a proved
restore is an open security defect, not a forgotten step.

**CI workflow:** `ci.yml` (Go pipeline) is deferred to Phase 1 — it joins the required
checks in the ruleset when the first code lands.

## Cybersecurity Standards

### Core Principles

- **Defense in Depth:** multiple controls = multiple barriers. The skill text, the RBAC and
  the loopback binding are three separate layers — none replaces the others.
- **Least Privilege:** every caller sees only the tools it may invoke (`tools/list` filtered
  by role); staff sees only its area's tickets.
- **Zero Trust:** never trust input, callers, or the channel. The channel transports an
  identity, never a role; a message claiming "soy el dueño" never changes authorization.
- **Fail Secure:** identity that does not resolve cleanly is rejected; invalid workflow
  configs are rejected as a whole; missing setup refuses to start the server.

### Authentication & Authorization (ACTIVE in this project)

Unlike the predecessor, this project has an authenticated web panel. Enforced patterns:

- ✅ Passwords hashed with **argon2id** — never plaintext, never reversible hashes.
- ✅ Panel sessions are **server-side rows in SQLite** (revocable, expiring, inactivity
  bound) delivered as `HttpOnly`/`Secure`/`SameSite=Strict` cookies. No JWT (deliberate:
  single server, revocation wins over statelessness).
- ✅ CSRF double-submit token on every state-changing form/endpoint.
- ✅ Login rate limit with backoff — no silent lockout, no unlimited attempts.
- ✅ RBAC at the use-case layer, re-asserted — never only at the transport.
- ❌ NEVER trust the client (LLM or browser) for authorization — always verify server-side.

### SQL Injection Prevention

- ❌ NEVER concatenate strings in SQL — parameterized queries only (`?` placeholders).
- ❌ NEVER build table/column names from input — use a strict allowlist.
- ✅ Schema identifiers are static constants in the repository layer.

### Network & Exposure

- **Loopback invariant:** MCP binds `127.0.0.1:3001`; the panel binds `127.0.0.1:3002`.
  No public ports by default. Remote panel access is opt-in by documented recipes (SSH
  tunnel / Caddy+HTTPS / Tailscale) — the binary never exposes itself.
- **Hermes tool boundary (documented risk, upstream hermes-agent#147647):** channel sessions
  with code-execution toolsets can bypass the tools-only contract. The installation docs
  MUST carry the minimal per-platform tool allowlist (`hermes tools disable ...`) with the
  final verification; the product only guarantees operation with that allowlist.

### Secrets & PII

- ❌ NEVER hardcode credentials; secrets live in `.env` (never committed) with a
  `.env.example` template.
- ❌ NEVER log tokens, passwords, or raw PII.
- ✅ **PII masking:** phone numbers are masked to their last 4 digits in every audit event;
  caller-supplied mask strings are discarded, never trusted.
- ✅ Client data lives only on the business owner's VPS — zero telemetry, zero third-party
  data flows.

### Secure Coding Patterns

- ✅ Wrap errors with `fmt.Errorf("...: %w", err)` for context propagation.
- ✅ `defer` for cleanup (`*sql.Rows`, `*sql.Stmt`, transactions); transactions for
  multi-row invariants (merges, workflow config saves).
- ✅ `context.Context` propagated through all layers for cancellation/timeout.
- ✅ Concrete types from public APIs — never `any`/`interface{}`.
- ✅ Validate every MCP tool arg and panel form input (type, length, format, range) before
  persistence.

### Security Checklist (Pre-Commit)

```
□ No hardcoded passwords, API keys, or secrets
□ All user/tool inputs validated and sanitized
□ All SQL parameterized (`?`) — no concatenation
□ Only *domain.SemanticError reaches callers; internals never leak
□ Loopback-only bindings untouched (MCP :3001, panel :3002)
□ Sessions: HttpOnly/Secure/SameSite=Strict + CSRF on state changes
□ Argon2id for any password storage; rate limits active
□ PII masked in logs/audit (phones: last 4 digits)
□ SQLite pragmas: WAL, busy_timeout=5000, foreign_keys=ON
□ No layering violations; dependencies point inward
□ `govulncheck ./...` clean (run before dependency changes)
□ Full pipeline green (fmt/vet/lint/build/test -race)
```

## Testing & Quality

- `go test -v -race ./...` is the bar for every commit.
- **Tests are mandatory** for new features and for every modified behavior; each test
  asserts the observable contract (exit codes, exact semantic errors, stored data and
  counters unchanged on rejection), through the public surface — never by inspecting
  internal storage.
- **Scale ceiling:** the volume test (10k synthetic clients / 100k tickets) runs in CI — the
  product declares and proves the ceiling it was designed for.
- **CI awareness:** `.github/workflows/` is the source of truth for checks; all checks must
  pass before merge. Web build → `go:embed` → Go pipeline is part of the release path from
  F4 onward.

## ODD & Feature Tracking

- Every feature is implemented as ODD work with its document in `odd/tasks/<feature>.md`:
  `## Specs` (numbered `S#` quoting the PRD RF **verbatim**), `## Tasks` (ID, linked `S#`,
  route, commit), `## Log` (append-only evidence).
- A requirement change appends its verbatim Log entry, rewrites only the affected `S#`, and
  reopens only its task.
- Delivery follows ordinary repository policy: work-unit commits on feature branches,
  squash-merge after review; push, PR creation and merge remain owner decisions.
