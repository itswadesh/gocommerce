# Threat model: AI features in GoCommerce

Status: proposed, 21 October 2026, for the M0 gate. It covers the features in
the [spec](spec.md) and the decisions in [ADR 0001](adr/0001-service-boundaries.md)
to [ADR 0005](adr/0005-model-providers.md), and is revisited at each
milestone gate.

## What is being protected

- **Each store's data, kept from every other store:** catalogue, prices,
  orders, companies, credit, conversations, documents, memories, embeddings,
  usage.
- **Money and stock:** payments, refunds, prices, discounts, credit, stock
  levels.
- **Customers' personal data and their trust:** contact details, messages,
  consent, the store's sending reputation on WhatsApp and email.
- **Credentials:** provider keys, Meta tokens, agent keys, webhook secrets.
- **The store's AI spend.**
- **The engine's integrity:** core state changes only through services (rule
  3), and no code runs that nobody reviewed.

## Who and what is involved

| Party | Trust |
|---|---|
| Store operators (people with roles) | Trusted within their rights, in their store |
| Agents (agent keys, unattended) | Trusted to read within their rights; never to act alone |
| Developers on a local store (stdio, own session) | Trusted as themselves |
| Customers writing in (WhatsApp, email, forms, uploaded POs) | Untrusted content |
| Suppliers' files, product text from imports | Untrusted content |
| Model providers | Processors of what the data-sharing policy lets out; not trusted with anything else |
| Meta (webhooks, Graph and Marketing APIs) | Trusted only after signature or TLS checks |
| Other stores on the same install | Adversarial to each other |

Trust boundaries are crossed:

- when inbound content arrives;
- when a request leaves for a model provider;
- when an agent's proposal becomes an action;
- when a request is bound to a store;
- when a generated extension leaves review.

## Threats and controls

| # | Threat | Control | Where decided | Proven by (gate) |
|---|---|---|---|---|
| T1 | **Prompt injection** in a message, PO, supplier file or product text makes a model propose a refund, a price, a message to someone else, or a data dump | Models hold no write tools; their output is a proposal or a staff-only record; a person approves with a preview recomputed from current state; untrusted content is fenced as data; a reply's recipient comes from the thread, never from model output; output decoded into a Go type with unknown fields refused | ADR 0003, 0005 | Injection corpus run against each task: no proposal outside the task's kinds, no recipient change (M1, M2) |
| T2 | **One store's data reaches another** through retrieval, a cache, a log, a shared prompt or a tool call | Row policy on every AI table, including embeddings; cache keys start with the store; one store per model request; agent keys bound to one store; the static token refused for agent work | ADR 0002 | Two-store tests per module; `ext/alltest` scope check (M1) |
| T3 | **An agent key is stolen or over-powered** | Hash only, shown once, expires, explicit rights subset, store-bound, cannot manage keys, roles, members, plugins or settings; revocable; last use recorded | ADR 0003 | Key tests: expired, revoked, other store, missing right (M1) |
| T4 | **Approval is bypassed:** self-approval, an agent approving, a stale preview, a double execution | Only a person with the action's rights approves; agent keys cannot; preview recomputed at approval; idempotency key per proposal; expiry | ADR 0003 | Approval tests for each refusal (M1) |
| T5 | **Nobody can say which agent did what** | `admin_audit` actor kinds for keys and agents; `mcp_audit` gains an actor and records reads; executions and proposals carry a trace id | ADR 0003, 0004 | Audit assertions in the approval tests (M1) |
| T6 | **Forged or replayed inbound webhooks** | Signature over the raw body checked in constant time before parsing; `(store, event id)` claimed before acting; store found through the identifier index only after verification | ADR 0004 | Bad-signature, replay and unknown-number tests (M2) |
| T7 | **Messages a customer did not ask for:** outside Meta's window, without a template, after an opt-out, promotional | No outbound AI message without a seller's approval in the first release; window, template and opt-out checked in code at send time, not by the model; opt-out recorded as consent state; no broadcasts | Spec §4, §16; ADR 0003 | Send refused outside the window, after opt-out, without approval (M2) |
| T8 | **More personal data leaves for a provider than the task needs** | Per-store data-sharing policy; contact details and message bodies off by default; fields the task does not use stripped before the request; policy recorded on each execution; personal data masked for agents unless their right and task need it | ADR 0005, 0003 | Request capture in tests shows only allowed fields (M1) |
| T9 | **A flood of inbound messages spends the store's money** | Budget reserved atomically before each call; per-store and per-key rate limits in Postgres; deduplication before any model call; rules and small models first; work over budget goes to a person | ADR 0005 | Budget exhaustion and rate-limit tests (M1) |
| T10 | **A guess or an injected claim becomes a remembered fact** | Memories written by a model are `unverified` until a person confirms; only confirmed memories are used as facts; every memory keeps its source, scope and expiry, and can be revoked and deleted | Spec §3.4 | Memory tests: unverified memory never cited as fact (M1) |
| T11 | **Keys and tokens are read from the database or a backup** | Envelope encryption for provider keys, Meta tokens and webhook secrets, with the key held outside the database; masked on every read; never logged | This document | Encryption round-trip test; `doctor` check that the key is configured (M1) |
| T12 | **The server is made to call an address of the attacker's choosing** (integration endpoints, connector URLs, documents an agent is asked to fetch) | Server-side calls to store-supplied URLs only over https to public addresses, checked after DNS resolution and on every redirect | This document | Refusal tests for private, loopback and link-local targets (M1, before any connector) |
| T13 | **Generated extension code is malicious or wrong** | Generated code is a pull request a developer reviews; it is built and tested in its own process; nothing is loaded at runtime (D57); rights declarations and scope tests run in CI | ADR 0001 | M6 gate |
| T14 | **The Meta Ads connection can change campaigns** | Only read permissions requested; tokens encrypted (T11) and revocable; no write calls exist in the code | Spec §8 | Scope list asserted in tests (M5) |
| T15 | **A model's price or SKU reaches a customer as fact** | Prices only from core pricing; every inferred line carries its source and confidence; nothing is substituted silently; a seller approves every draft and quote | Spec §5 | Order-desk tests: model price ignored, low confidence lands in exceptions (M2) |
| T16 | **Personal data leaks through logs** | Logs carry store, trace id and outcome; prompts, outputs and message bodies stay in store tables with retention | ADR 0002 | Log capture in tests (M1) |
| T17 | **Conversations, documents and embeddings are kept forever** | Per-store retention with defaults; customer erasure removes their conversations, documents, memories and embeddings; store deletion cascades | This document | Erasure tests (M2) |
| T18 | **AI load slows checkout** | Model work runs only in the job runner with a concurrency limit and timeouts; no transaction spans a model call (rule 5) | ADR 0004 | Load check with jobs running during a checkout run (M1) |

## Existing gaps this work depends on

These are in the engine today, and the controls above cannot hold without
closing them. They are first in M1's order of work:

- The static admin token spans stores and names no one.
- Every API key is recorded as operator id 0, told apart only by a label, so
  the audit trail's actor filter merges them.
- MCP's mutating tools run without approval for any credential holding the
  right.
- Agents receive unmasked personal data on real stores.
- There are no per-store or per-key rate limits.
- Secrets are stored in plain text. Plugin secrets are masked when read, but
  an API key's stored copy can be read in full with `apikeys.write`.
- No request or trace id exists to join a log line to an audit row.
- Personal data stays on orders and outbox payloads after a customer is
  erased, and audit, outbox and idempotency rows are never pruned.

## Out of scope for now

- Operators abusing rights they legitimately hold. They are audited, not
  prevented.
- The model provider's own security. The data-sharing policy limits what it
  holds, and the owner chooses the provider and its retention terms.
- Physical and host security of the install.
