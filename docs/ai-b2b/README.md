# AI-Native B2B: M0 documents

This folder starts the AI workstream described in the [spec](spec.md). It holds
M0's documents: what exists, what the decisions are, and what can go wrong.
Nothing here changes code. The owner accepted the decisions at the M0 gate
(spec §12, §15.5) on 21 October 2026, and M1 has started.

Read in this order:

1. [The spec](spec.md), version 1.0, as the owner wrote it.
2. [Gap analysis](gap-analysis.md): every section of the spec against the code
   at `e5fc9ea6`.
3. The decision records below.
4. [Threat model](threat-model.md).

The owner's [ShopNx AI plan](shopnx-ai-plan.md) of 29 October 2026 lists the
merchant-facing features to build on this foundation. The
[ShopNx AI audit](shopnx-ai-audit.md) sets each one against the code and gives
the order of work.

## Decision records

| # | Decision | In one line |
|---|---|---|
| [0001](adr/0001-service-boundaries.md) | Service boundaries | The eleven services are `ext/` modules in the engine; the spec's `/v1` routes map onto rule 9's namespaces |
| [0002](adr/0002-tenancy.md) | Tenancy | A store is the tenant; AI tables, vectors and caches sit behind the same row policy; pgvector is optional |
| [0003](adr/0003-identity-rights-approvals.md) | Identity, rights, approvals | Agents get their own expiring, store-bound keys; anything reaching a customer, money, stock or prices waits for a person's approval |
| [0004](adr/0004-events-and-jobs.md) | Events and jobs | Model work runs as durable Postgres jobs created in the same transaction as their cause; no Redis |
| [0005](adr/0005-model-providers.md) | Model providers | One provider interface behind the gateway; named tasks; every run recorded with tokens and integer cost; budgets reserved before calls |

All five are accepted, as D103 to D107 in [`PLAN.md`](../../PLAN.md) §5.

## The owner's decisions

The owner answered the six questions M0 left open on 21 October 2026, taking
each recommendation as proposed:

1. **Modules, not separate processes; module routes, not `/v1`**
   ([ADR 0001](adr/0001-service-boundaries.md), D103).
2. **Production Postgres moves to an image with pgvector when semantic search
   is built**, not before ([ADR 0002](adr/0002-tenancy.md), D104). M1's first
   features do not need it.
3. **Agent keys keep no readable copy**, reversing M46 for agents only
   ([ADR 0003](adr/0003-identity-rights-approvals.md), D105).
4. **The agent writes that count as "staff only"**, and so skip approval, are
   drafts, notes, exception items, classified messages and proposed matches
   ([ADR 0003](adr/0003-identity-rights-approvals.md), D105).
5. **A draft order is a quote the seller opens for a company**, approved and
   then converted as quotes are today, rather than a new draft state in core's
   order machine ([gap analysis §5](gap-analysis.md#5-ai-order-desk-5), D108).
6. **On a hosted install, a platform-wide model key is off by default** and,
   when switched on, metered against each store's cap
   ([ADR 0005](adr/0005-model-providers.md), D107). Since 2026-10-29 (D151)
   the install's owner switches it on by setting `ANTHROPIC_API_KEY`; every
   store with no key of its own then uses it, paid from its AI credit.

Two more rows came out of the same answer. D109 makes addresses that a store
operator chooses for the server to call (webhook endpoints, provider base
URLs) reachable only when they are public, which the
[threat model](threat-model.md) needs before any agent can set one. D110 seals
stored secrets under an install key, which is step 3 below.

## Order of work for M1

Each step is its own pull request, behind a plugin that installs idle.

1. Fix the documentation drift listed in the
   [gap analysis](gap-analysis.md#drift-to-fix-before-a-context-engine-serves-it),
   and MCP's report currency.
2. Agent keys, audit actor kinds, masking for agents, per-key rate limits.
3. Encryption at rest for provider keys, tokens and webhook secrets (D110).
4. `ext/ai-gateway`: providers, tasks, `ai_jobs`, `ai_executions`, budgets,
   the approval queue, the usage screen. `ext/import-amazon` moves onto it.
5. `ext/mcp`:
   - in the shipped binary;
   - a current protocol revision;
   - an actor on every call;
   - proposals for agent writes;
   - read tools for companies, quotes, prices and stock per location.
6. `ext/context`: the spec's seven context tools over the embedded OpenAPI
   document, routes, rights, skills, decisions and the store's own policies.
7. `ext/memory`: structured, store-scoped memory with review status and
   expiry; vectors wait for semantic search (decision 2).
8. The SDK skeleton: a module manifest with an engine-compatibility check.
9. The token benchmark: a fixed task set run through the gateway, with its
   baseline recorded.

M0 still owes the domain contracts, fixtures and cost baseline (spec §12).
The contracts depended on these decisions, so they come next. They are built
with the gateway and the approval queue (step 4), where the first callers of
each contract are.
