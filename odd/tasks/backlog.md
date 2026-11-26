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
| CallerError placement (T3 worker report) | CallerError lives in internal/auth; internal/domain does not exist yet | **Decision pending T4** — map into domain.SemanticError or keep Code() contract; T6 must not lose Code() |
