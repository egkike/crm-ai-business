# Contributing to CRM-AI-Business

Thanks for taking the time to help. Read this before you open anything — it is
short, and it is the whole policy.

## What this repository accepts: issues only

This project takes **issues, not pull requests**.

| You want to… | Do this |
|---|---|
| Report something broken | Open a [Bug Report](https://github.com/egkike/crm-ai-business/issues/new?template=bug_report.yml) |
| Propose a feature or a change | Open a [Feature Request](https://github.com/egkike/crm-ai-business/issues/new?template=feature_request.yml) |
| Ask about the design or the roadmap | Read the [README](./README.md) and the [PRD](./docs/PRD.md) (Spanish, canonical) first; open a Bug Report if the docs are wrong about something |

Pull requests are closed automatically, without review, with a link back to this
file (`.github/workflows/close-unsolicited-prs.yml`). That is a stated policy,
not a judgement about your change.

**Why:** delivery here is single-owner and runs through an internal gate. Every
candidate is implemented task by task with tracked evidence and must pass a
review gate that only the maintainer can drive. Code arriving from outside that
gate cannot be evaluated or granted a receipt, so it cannot be merged. Issues are
the input that flow accepts.

**Scope:** this rule binds outside contributors. The maintainer's own changes —
documentation included — land as issue-linked pull requests through the gates
described in [`AGENTS.md`](./AGENTS.md).

You do not need to label, assign, claim, or comment "I'll take this" — there is
nothing for you to reserve, and the issue form stamps its one `type:` label by
itself.

## Before you open an issue

1. **Search first** — open *and* closed issues. Duplicates get closed, not answered.
2. **Read the doc that should have covered it** — the [README](./README.md) and the
   [PRD](./docs/PRD.md).
3. **One issue per report.** Two bugs are two issues.

## Reporting a bug

Include:

- **Version** — `crm-ai-business --version` (or the commit you built from; see
  the bug form)
- **Platform** — Linux, macOS, or Windows via WSL2
- **How you run it** — built from source, or through the installer once it ships
- **Steps to reproduce**, minimal and in order
- **Expected** vs. **actual**
- **Evidence** — the MCP tool response, the panel response, and the relevant log
  lines

> **Never paste secrets or real customer data.** No API keys, no caller IDs, no
> database dumps, no client names or phone numbers. Redact before you paste.
> A report that leaks someone's data gets deleted, not triaged.

## Requesting a feature

Lead with the problem, not the solution: what you cannot do today, and what it
costs you. Then the shape you have in mind, then what you already considered and
discarded. A request that names the problem can be redesigned; one that only
names an implementation usually gets closed as out of scope.

## What happens after you submit

- The maintainer triages every issue, alone. There is no SLA; silence means "not
  planned yet", never "rejected".
- You may be asked for information. An issue that stays unanswered for a long
  time may be closed — reopening it is fine.
- Accepted work is implemented and delivered by the maintainer under the internal
  gates described in [`AGENTS.md`](./AGENTS.md). That file is internal: it is
  worth reading for how this codebase is built, but nothing in it is a task
  assigned to you.

## Security

**Do not report a vulnerability in a public issue.** Use the *Security* tab of
this repository → *Report a vulnerability*, which opens a private advisory only
the maintainer can read. Give a reasonable window to fix it before disclosing
anything publicly; if the private form is unavailable, contact `@egkike`
directly instead.

## Code of conduct

Be direct and be decent. Technical disagreement is welcome; contempt is not.
