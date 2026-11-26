# CRM-AI-Business

> Self-hosted CRM for small businesses where an AI agent — not an app — is the interface.

<div align="center">

<a href="https://github.com/Gentleman-Programming/gentle-ai">
  <img width="180" src="https://raw.githubusercontent.com/Gentleman-Programming/gentle-ai/main/docs/assets/brand/built-with-gentle-ai.png" alt="Built with Gentle-AI" />
</a>

</div>

<div align="center">

🚧 **Pre-alpha — Phase 1 (`core-mcp`) delivered.** The MCP server runs: identity by
sender, anonymous client registration, catalog search with agent-oriented knowledge
(what each item solves, what it does not), tickets with an audited history, and a
role-gated tool catalog. See [`docs/demo.md`](./docs/demo.md) for the reproducible
end-to-end script. No installers yet — those land with Phase 6; today you run the
binary from source.

</div>

## What it is

A self-hosted, lightweight **CRM + tickets + bookings backend** for small businesses,
exposed as an **MCP (Model Context Protocol) server** that the conversational agent
**[Hermes](https://hermes-agent.nousresearch.com/)** operates as the only customer-facing
interface:

- **Customers** never see a web or mobile app. They talk to Hermes over **WhatsApp** and
  **e-mail**; the business team (owner/admin/staff) operates over a **single shared
  Telegram bot**.
- **Every contact becomes a ticket** and enters a configurable workflow — including the
  ones that do not end in a purchase, because they are the funnel the business needs to see.
- **The agent resolves**: from the first greeting, Hermes conducts each customer toward the
  effective resolution of their motive — catalog answers, a direct purchase link (or a
  routed salesperson), a booked service, a home-visit technician, a product delivery, or a
  filed complaint.
- The backend is **Go + SQLite, one binary, no containers, no external services**
  (no Redis, no Node in production). An embedded Astro admin panel serves the internal
  team on loopback.

```
 Customers ── WhatsApp · e-mail ──▶  Hermes (AI agent)  ──MCP tools──▶  crm-ai-business
 Owner / Admin / Staff ── Telegram ─▶        │                                 Go + SQLite
 Owner / Admin / Staff ── admin panel ───────┼──────────────▶  127.0.0.1:3002     127.0.0.1:3001
                                            └── alerts out via its channels (the backend
                                                never sends messages by itself)
```

## Key design facts

| Fact | Value |
|---|---|
| Backend | Go, single self-contained binary; SQLite (WAL, FTS5) via `modernc.org/sqlite` |
| MCP transport | Streamable HTTP POST-only, `127.0.0.1:3001` — loopback invariant |
| Identity | `X-Caller-Id` per sender, per request, verbatim — the channel transports an identity, never a role; RBAC resolved server-side against the database |
| Admin panel | Astro + Tailwind + TypeScript, built to static and **embedded in the Go binary** (`go:embed`), loopback `127.0.0.1:3002`; remote access by documented opt-in recipes (SSH tunnel / Caddy+HTTPS / Tailscale) |
| Notifications | 100% via Hermes: the backend only generates pending alerts; Hermes delivers them over its channels |
| Scheduling | in-process, state in SQLite — deadlines, inactivity and sales follow-ups become alert rows; survives restarts |
| Workflows | configurable ticket types, states, transitions and declarative rules (on-enter/on-exit notifications, deadlines with overdue alerts, per-area round-robin assignment) — no scripting |
| Bookings | one agenda per bookable staff member; three appointment kinds: in-office service, home assistance, product delivery (with address and travel buffer) |
| Security posture | loopback-only by default; argon2id + server-side sessions + CSRF on the panel; PII masked in audit logs; a documented minimal tool allowlist is required on the Hermes runtime |
| Deployment | Linux/macOS native, user-level service (systemd/launchd), no Docker; Windows via WSL2 |
| License | MIT |

## Roadmap

| Phase | Scope | Status |
|---|---|---|
| F1 · `core-mcp` | Hexagonal Go skeleton + SQLite schema + MCP server + RBAC + client registration + Hermes pairing — demo: a WhatsApp message becomes a ticket the staff answers | **Delivered** |
| F2 · `workflow-engine` | Configurable ticket types/states/transitions + declarative rules + in-process scheduler + areas/visibility + the internal Telegram bot | Planned |
| F3 · `bookings` | Bookable staff, schedules, slot search (per staff or per area), three appointment kinds with address and buffer | Planned |
| F4 · `web-internal` | Embedded Astro panel: login (sessions/argon2id/CSRF), ticket board, workflow editor, business settings, reports | Planned |
| F5 · `loyalty-alerts` | Inactivity reminders, loyalty reports, lead follow-up alerts, CSV export and charts | Planned |
| F6 · `install-release` | `install.sh` covenant (pinned, SHA256, user-level service, uninstall), Hermes allowlist documentation, release automation | Planned |

The full definition — 23 functional requirements with acceptance criteria, the data model,
the authorization model, risks and dependencies — lives in the
[PRD](./docs/PRD.md) (Spanish, canonical).

## Documentation

- [`docs/PRD.md`](./docs/PRD.md) — canonical Product Requirements Document (Spanish, approved)
- [`docs/idea.md`](./docs/idea.md) — the original idea document (historical origin)
- [`docs/pairing.md`](./docs/pairing.md) — connecting Hermes: identity contract, channels (the
  e-mail channel is **pending verification**), the skill install and the mandatory tool allowlist
- [`docs/demo.md`](./docs/demo.md) — reproducible end-to-end demo of what works today
- [`skills/recepcionista-negocio/SKILL.md`](./skills/recepcionista-negocio/SKILL.md) — the Hermes
  skill (resolutive doctrine, personas, guardrails)
- [`AGENTS.md`](./AGENTS.md) — project conventions, architecture enforcement contract and
  development workflow (applies to AI-assisted development)
- ODD feature tracking under `odd/tasks/` (created as each phase starts)

## Contributing

This repository takes **issues only**: bug reports and feature requests through the issue
forms; pull requests from outside the maintainer's gate are closed automatically. See
[`CONTRIBUTING.md`](./CONTRIBUTING.md).

**Security:** do not report vulnerabilities in public issues — use the repository's
*Report a vulnerability* private advisory.

## Development

Run the tests (the scale ceiling is part of the suite):

```bash
go test -race ./...
go test -run TestScaleCeiling ./internal/db/   # 10k clients / 100k tickets
```

The standards this code follows —
hexagonal architecture, prepared statements only, semantic Spanish errors, the
loopback invariant, PII masking, the full pre-commit pipeline — are already contracted
in [`AGENTS.md`](./AGENTS.md).

Development is orchestrated with
[Gentle AI](https://github.com/Gentleman-Programming/gentle-ai): features run through
Organic Driven Development (ODD) — tracked feature docs in `odd/tasks/`, work-unit
commits, and a native review gate.

## License

MIT — see [`LICENSE`](./LICENSE).
