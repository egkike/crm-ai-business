# Advisory backlog — review follow-ups

> Durable inventory of non-blocking findings from APPROVED reviews. Each entry cites its
> review lineage and carries a disposition; nothing here blocks delivery or reopens a
> review. Entries leave this file when fixed (their PR is linked).

| ID | Finding | Disposition |
|---|---|---|
| R3-001 (review-15ebf00d81654095) | `cmd/server/main.go` run(): no `http.ErrServerClosed` filter; a listener error can overwrite the nil Shutdown return; the graceful-shutdown timeout path prints ErrServerClosed as a failure | **Fold into F1 T2** (same file, same behavior — PR #10) |
| R3-002 (review-15ebf00d81654095) | `TestResolvePort` does not pin boundary values 1 and 65535 as valid | **Fold into F1 T2** |
| R1-port-range (review-15ebf00d81654095) | `resolvePort` accepts ports < 1024 | **No action** — loopback dev skeleton; acceptable, revisit if the listener ever binds non-loopback |
| R3-003 (review-15ebf00d81654095) | `go.mod` pins go 1.27.4 (fine-grained) — CI toolchain resolution dependency | **Monitor** — first CI run green (run 45884152670); no action |
| R3-004 (review-15ebf00d81654095) | `golangci-lint-action@v7` floating tag — pin to full SHA for determinism | **F6 (install-release)** hardening |
| R2-correction-lines-opaque (review-bc24a6f33f5241a5) | AGENTS.md: "one replaced line = 2" without the deletion+addition rationale | **Sweep 1** (this issue's PR) |
| R2-001 (review-bc24a6f33f5241a5) | AGENTS.md: ruleset-id advice duplicated in the same section | **Sweep 1** |
| R4-pr-gate-case-silent-exit (review-cc27ad41eef79137) | pr-gate.yml alarm guard exits 0 on non-numeric ISSUE, hiding the skip | **Sweep 1** |
| R4-pr-gate-retry-no-backoff (review-cc27ad41eef79137) | pr-gate.yml: immediate retry, no delay; combined failure message | **Sweep 1** (with R2-002 below — retry helper) |
| R2-002 (review-cc27ad41eef79137) | pr-gate.yml: identical `gh issue view` invocation duplicated on two lines | **Sweep 1** (retry helper) |
| R1-001 / R1-002 (review-cc27ad41eef79137) | pr-gate.yml: best-effort re-flag is by design; digit guard is hardening-only | **No action** — documented design; unreachable in practice |
