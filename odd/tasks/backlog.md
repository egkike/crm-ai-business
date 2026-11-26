# Advisory backlog — review follow-ups

> Durable inventory of non-blocking findings from APPROVED reviews. Each entry cites its
> review lineage and carries a disposition; nothing here blocks delivery or reopens a
> review. Entries leave this file when fixed (their PR is linked).

| ID | Finding | Disposition |
|---|---|---|
| R3-001 (review-15ebf00d81654095) | `cmd/server/main.go` run(): no `http.ErrServerClosed` filter; a listener error can overwrite the nil Shutdown return; the graceful-shutdown timeout path prints ErrServerClosed as a failure | **Fixed** — F1 T2 PR |
| R3-002 (review-15ebf00d81654095) | `TestResolvePort` does not pin boundary values 1 and 65535 as valid | **Fixed** — F1 T2 PR |
| R1-port-range (review-15ebf00d81654095) | `resolvePort` accepts ports < 1024 | **No action** — loopback dev skeleton; acceptable, revisit if the listener ever binds non-loopback |
| R3-003 (review-15ebf00d81654095) | `go.mod` pins go 1.27.4 (fine-grained) — CI toolchain resolution dependency | **Monitor** — first CI run green (run 45884152670); no action |
| R3-004 (review-15ebf00d81654095) | `golangci-lint-action@v7` floating tag — pin to full SHA for determinism | **F6 (install-release)** hardening |
| R2-correction-lines-opaque (review-bc24a6f33f5241a5) | AGENTS.md: "one replaced line = 2" without the deletion+addition rationale | **Fixed** — sweep 1 (PR #10) |
| R2-001 (review-bc24a6f33f5241a5) | AGENTS.md: ruleset-id advice duplicated in the same section | **Fixed** — sweep 1 (PR #10) |
| R4-pr-gate-case-silent-exit (review-cc27ad41eef79137) | pr-gate.yml alarm guard exits 0 on non-numeric ISSUE, hiding the skip | **Fixed** — sweep 1 (PR #10; now exit 1 + ::error) |
| R4-pr-gate-retry-no-backoff (review-cc27ad41eef79137) | pr-gate.yml: immediate retry, no delay; combined failure message | **Fixed** — sweep 1 (PR #10; issue_labels helper, sleep 5) |
| R2-002 (review-cc27ad41eef79137) | pr-gate.yml: identical `gh issue view` invocation duplicated on two lines | **Fixed** — sweep 1 (PR #10; retry helper) |
| R1-001 / R1-002 (review-cc27ad41eef79137) | pr-gate.yml: best-effort re-flag is by design; digit guard is hardening-only | **No action** — documented design; unreachable in practice |
| R1-001 (review-10f6baa897a67c71) | pr-gate.yml: non-numeric guard now exit 1 — fail-closed accepted, spurious red at worst | **No action** — accepted design (sweep review) |
| R1-002 (review-10f6baa897a67c71) | pr-gate.yml: issue_labels still swallows gh stderr; improved message mitigates | **No action** — message is honest and actionable |
| R2-sleep-constant (review-10f6baa897a67c71) | pr-gate.yml: bare 5s literal in the backoff | **No action** — surrounding comment explains the intent |
| R4-001 (review-10f6baa897a67c71) | pr-gate.yml: single fixed backoff, latency grows with linked issues | **No action** — bounded gate (seconds); revisit only if linked PRs grow large |
| R4-002 (review-10f6baa897a67c71) | pr-gate.yml: empty ISSUE now fails vs skips — unreachable in practice (event field always set) | **No action** — out-of-domain input |
| R3-mcp-body-assert (review-6c2652d0ceedee5c) | main_test.go: 501 subtest does not assert Content-Type: application/json | **Pending** — 1-line test addition, fold into the next cmd/server touch |
| R3-mcp-errch-drain (review-6c2652d0ceedee5c) | run(): errCh unbuffered send can block if run() already returned with the process alive | **No action** — harmless for a leaf main; note for future refactors |
| misspell/Spanish (T3 worker report) | .golangci.yml misspell flagged Spanish user-facing strings (no Spanish locale) | **Fixed** — T3 PR: misspell `ignore-rules` (v2 key; `ignore-words` does not validate) |
| CallerError placement (T3 worker report) | CallerError lives in internal/auth; internal/domain does not exist yet | **Decision pending** — T4 landed without internal/domain; keep the Code() contract, revisit at T6 (mapping must not lose Code()) |
| R1-lookup-interface-broad-identity (review-68febdeb10f15fde) | adapter owns assembling Role/Enabled; a future adapter bug hardcoding a role would flow if it is one of the four valid values | **Implemented note** — T4 adapter derives Role from accounts.role and the resolver validates fail-closed; stays as guidance for every future adapter |
| R1-ordering-failclosed-disabled (review-68febdeb10f15fde) | corrupt-row guards run before the Enabled check → corrupt disabled row reports internal error instead of account_disabled | **No action** — fail-safe; error-classification nuance only |
| R2-msg-collision (review-68febdeb10f15fde) | adapter forgetting Enabled surfaces as ErrAccountDisabled, indistinguishable from a real disabled account | **No action** — T4 adapter sets Enabled from is_active strictly (test pins it); residual risk documented |
| R2-sentinel-type (review-68febdeb10f15fde) | CallerError sentinels declared as `error` type; `*CallerError` would allow errors.Is without the As dance | **Pending** — cosmetic typing tweak, fold into the next internal/auth touch |
| R3-001 (review-68febdeb10f15fde) | wrapped adapter errors may echo internals; the transport collapse is doc-only until T6 | **Open for T6** — transport must collapse non-CallerError errors to the generic JSON-RPC -32603 |
| R3-002 (review-68febdeb10f15fde) | corrupt-row guard order couples to the observable outcome | **No action** — same nuance as R1-ordering |
| misspell/producto (T4 worker report) | `producto` flagged as an English misspelling; worker carried a scoped nolint | **Fixed** — T4: added to misspell ignore-rules, nolint dropped |
| auth port doc order (T4 worker report) | internal/auth/lookup.go doc said "clients... then accounts" while the PRD §3.8 chain is accounts-first | **Fixed** — T4: port doc aligned to §3.8 accounts-first |
| R1-ascii-fold (review-31ae9e99067acc33) | lower() folds ASCII only — non-ASCII local parts compare byte-exact | **No action** — documented deterministic behavior (schema + adapter) |
| R1-enabled-shadow (review-31ae9e99067acc33) | disabled account sharing an identifier shadows a coexisting client row forever | **No action** — spec-compliant (§3.8 accounts-first); F4 panel note: warn when transferring identifiers |
| R2-002 (review-31ae9e99067acc33) | test comment described coverage the fixture never created | **Fixed** — T5 rider: comment now states what the test pins (guard-before-lookup); behavior untouched |
| R2-003 (review-31ae9e99067acc33) | TestOpenRefusesInvalidPath lives in migrate_test.go | **T7** — move to open_test.go with the next internal/db touch |
| R2-004 (review-31ae9e99067acc33) | seed via string concatenation of constants — normalizes DDL interleave | **T7** — parameterize or guard-comment when the FTS migration lands |
| R3-001 (review-31ae9e99067acc33) | DSN built by concatenation, guard fragile against future DSN params | **T7** — same internal/db touch |
| R3-003 (review-31ae9e99067acc33) | appliedVersions loads all versions into memory | **T7** — MAX/COUNT refactor in the same pass |
| R3-004 (review-31ae9e99067acc33) | MkdirAll leaves the directory if migrate fails — cleanup untested | **T7** — cleanup test in the same pass |
| R2-001 (review-31ae9e99067acc33) | lookupAccount/lookupClient/findAccount/findClient structural duplicates (4 places) | **Post-F1 refactor sweep** — unify helpers in a type:refactor work-unit |
| R3-002 (review-31ae9e99067acc33) | ErrCallerNotFound sentinel reused across 4 queries — 'no match' vs 'table absent' needs query order to distinguish | **Post-F1 refactor sweep** — same pass as R2-001 |
| misspell/clientes (T5 worker report) | `clientes` flagged as an English misspelling (24 hits) | **Fixed** — T5: added to misspell ignore-rules (third Spanish word; pattern: add, never reword canonical strings) |
| R1-displayname-unbounded (review-923a035dd8dd3224) | register_client accepts a DisplayName of unbounded length before insertion | **Pending** — add a MaxDisplayNameLen guard; fold into the next internal/application touch (T7/T8 rider) |
| R1-enum-oracle (review-923a035dd8dd3224) | distinct registration rejections let a gateway-controlling prober map internal identifiers | **Resolved by owner** — keep distinct messages (honest UX, predecessor contract); threat model documented: X-Caller-Id is injected by the trusted loopback gateway, senders cannot present arbitrary identifiers |
| R3-001 (review-923a035dd8dd3224) | same unbounded display_name (duplicate of the risk finding) | **Pending** — same fix as R1-displayname-unbounded |
| R3-002 (review-923a035dd8dd3224) | limiter budget burned by attempts that fail after admission | **No action** — fail-closed by design; retry-UX note for the F2 scheduler backlog |
| misspell/conversacional+argumentos (T6 worker report) | two more Spanish words flagged | **Fixed** — T6: added to misspell ignore-rules (fifth and sixth words) |
| R1-backlog-displayname-pending (review-f90b8b8884381ad5) | backlog still listed R1-displayname-unbounded as Pending though internal/mcp already enforces the 120-rune cap | **Fixed** — entry closed: the T6 worker implemented MaxDisplayNameLen=120 runes in register_client |
| R1-transport-identity-header-trust (review-f90b8b8884381ad5) | identity taken solely from X-Caller-Id with no verification | **No action** — documented design: mitigated by the loopback invariant pinned at the composition root |
| R3-dbhandle-leak-composition (review-f90b8b8884381ad5) | buildMCPHandler leaves the SQLite handle unclosed on intermediate errors | **Pending** — fold into the next cmd/server touch (harmless today: startup failure exits the process) |
| R1-displayname-unbounded (review-923a035dd8dd3224) | register_client accepts unbounded DisplayName | **Fixed** — via T6 (120-rune cap; closed by the T6 review's own finding) |
| misspell/problemas+columnas (T7 worker report) | two more Spanish words | **Fixed** — T7: added to misspell ignore-rules (seventh and eighth words) |
| R3-dbhandle-leak-composition (review-f90b8b8884381ad5) | buildMCPHandler leaves the SQLite handle unclosed on intermediate errors | **Fixed** — T8 wiring: committed-flag defer closes it unless the composition fully succeeds |
| misspell/transicion (T8 worker report) | Spanish event-kind vocabulary flagged | **Fixed** — T8a: added to misspell ignore-rules (ninth word) |
| R1-ticket-clientid-trust (review-9e50b7843687095a) | create_ticket forwards the caller-supplied client_id; the use case overwrites it with the resolved identity | **Pending** — defense in depth: refuse the field at the transport for the client role on the next internal/mcp/tickets.go touch |
| R1-transition-state-decode (review-9e50b7843687095a) | the transition decoder accepts any string for state; the use case refutes it | **Pending** — enforce the enum in the decoder (cheaper rejection) |
| R3-schema-decoder-bounds (review-9e50b7843687095a) | schema bounds (limit<=50, motive<=120, text<=2000) are not enforced by the decoders; use cases should re-enforce, unproved here | **Pending** — one test per upper bound at the transport level |
| R3-arglimit-undeclared (review-9e50b7843687095a) | argLimit/argOffset live in another file of package mcp, so the sync comment does not cover them | **No action** — cosmetic; the names compile from catalog.go |
| R3-dbhandle-close-untested (review-9e50b7843687095a) | the committed-flag handle-close fix has no test | **Pending** — one composition-failure test on the next cmd/server touch |
| R3-listresult-iserror (review-9e50b7843687095a) | ticketListCallResult ignores result.IsError | **Pending** — test-helper hardening, fold into the next mcp test touch |
| R3-mask-split-run (review-e72126401bac0e55) | CRITICAL: maskPhoneRuns masked contiguous runs only, leaving separator-split phones verbatim in audited payloads | **Fixed** — T8a: masking works on digit clusters (space/-/./()/+); last 4 survive; slash excluded so dates stay intact |
| R1-NOTE-RACE (review-e72126401bac0e55) | AddTicketNote's ownership/closed check is a read not repeated inside the write — a concurrent close lets the note land on a closed ticket | **Pending** — fix: extend the compare-and-set guard to the note append in one repository operation; next internal/repository/tickets.go touch |
| R3-note-nonatomic (review-e72126401bac0e55) | AddTicketNote performs three non-transactional steps (append → touch → read); a failed touch leaves the event audited but updated_at unmoved (F2 inactivity clock not reset) | **Pending** — same fix: append + touch + readback in one transaction under the state guard |
