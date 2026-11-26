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
