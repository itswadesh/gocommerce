# ADR 0002: A store is the tenant, and AI data obeys the same row policy

Status: accepted, 21 October 2026, as D104 in `PLAN.md` §5.

## Context

The spec (§2) wants isolation "on every query, vector namespace, cache key,
log, and tool invocation", and every AI outcome to carry a `tenant_id` (§11).

One engine already serves every store from one set of tables (D83). A store
table carries `store_id` under a forced row policy, and each statement is bound
to the caller's store through its context (rule 15). `ext/alltest` fails any
module table outside the policy, and `doctor` fails a database login that could
bypass it.

## Decision

1. **`tenant_id` is `store_id`.** No second tenant concept is introduced. API
   payloads that the spec says carry `tenant_id` carry the store id under the
   existing field name.

2. **Every AI table is a store table.** Executions, approvals, memories,
   embeddings, conversations, intakes and usage counters are created by their
   module and put behind the policy with `ScopeMigration`. Every query takes
   the caller's `ctx`; background work runs through `app.ForEachStore`.

3. **An inbound event that does not name a store is routed by a global index
   holding only identifiers.** Meta calls one webhook per app, so a WhatsApp
   message names a phone-number id, not a store. A table such as
   `whatsapp_cloud_numbers_index (phone_number_id → store_id)` sits outside the
   policy, like `api_key_index` does today, and holds nothing but the mapping.
   The handler verifies the signature, looks up the store, binds it, and
   everything after that runs under the policy.

4. **Vectors live in store tables and are searched under the policy.**
   - An embeddings table has `store_id`, so the row policy filters before any
     row is returned; there is no shared index another store can reach.
   - Filtering after an approximate nearest-neighbour scan can return fewer
     than *k* rows for a small store inside a large table. M1 uses pgvector
     0.8 or later with iterative index scans, and its gate measures recall per
     store size rather than assuming it.
   - Engine documentation (OpenAPI, skills, decisions) is not store data. It is
     embedded in the binary, and the context engine searches it lexically
     first; it is embedded as vectors only if measurement shows lexical search
     is not enough.

5. **pgvector is an operational dependency, and the engine runs without it.**
   - Compose and CI move to a Postgres image that ships pgvector, for the major
     versions each uses today (17 in Compose, 18 in CI).
   - The extension is created once by whoever administers the database. The
     engine's own login is an ordinary login, never a superuser (rule 15, D83,
     enforced by `doctor`'s row-security check), and is not expected to
     create it.
   - The memory module checks for the extension at start. Without it, semantic
     search is off and lexical search is used; `doctor` reports which.

6. **Cache keys begin with the store id**, including any cache of model
   answers in the gateway. Two stores asking an identical question do not share
   an answer: a shared cache would let one store learn, from a hit, what
   another store had asked.

7. **Logs carry the store and a trace id; they do not carry prompts.**
   - Every AI log line, and the request log line (which lacks both today), gets
     `store` and `trace_id`.
   - Prompts, model outputs and message bodies stay in store tables under the
     policy, with retention, and never in logs.

8. **A tool call is bound to exactly one store.**
   - Over HTTP, MCP inherits the request's store binding. Over stdio it binds
     `Config.DefaultStore` or the only active store, and refuses when there
     are several, as today.
   - The credential an agent uses is bound to one store
     ([ADR 0003](0003-identity-rights-approvals.md)). The static admin token,
     which spans stores, is not accepted for agent work.

9. **A model request carries one store's data.** The gateway assembles each
   request from reads made under one store binding. Provider-side prompt
   caches hold only content that is the same for every store (instructions,
   schemas, engine docs), placed before the first store-specific part of the
   request.

## Consequences

- Isolation is enforced by the database for AI data exactly as for orders, and
  proven by the same `ext/alltest` check, plus one test per AI module that runs
  two stores side by side and asserts neither sees the other's rows, vectors or
  cached answers. That is the M1 gate's "permission tests".
- Deleting a store removes its AI data by the existing cascade.
- The global routing indexes are a deliberate, small exception to the policy,
  and each must be listed next to `api_key_index` in the scope tests' allow
  list, so adding one is a visible act.
- Hosting changes: the Postgres image changes before semantic search is turned
  on anywhere.

## Alternatives considered

- **A schema or database per store for vectors.** Stronger walls, but a second
  isolation mechanism beside the one D83 chose, and migrations per store.
- **An external vector database.** A second system to keep consistent, a second
  place to delete from, and a new dependency; the spec already names pgvector.

## Owner's decision

Moving the production Postgres image to one that ships pgvector is a hosting
change for admin.kitcommerce.store and any self-hosted install. It is not
needed for M1's first features (gateway, read-only MCP, structured memory).
The owner decided on 21 October 2026 that it waits until semantic search is
built.
