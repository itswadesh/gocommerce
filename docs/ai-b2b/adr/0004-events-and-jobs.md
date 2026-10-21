# ADR 0004: Model work runs as durable jobs in Postgres, triggered by the outbox

Status: proposed, 21 October 2026. Becomes a row in `PLAN.md` §5 when accepted.

## Context

The spec wants retries, idempotency and auditability through "a durable
job/outbox", Redis "where already supported", and inbound webhooks verified
and deduplicated (§2, §4, §13).

Today:

- **The outbox** (D8, D9) writes each event in the transaction that made the
  change. It is claimed with `FOR UPDATE SKIP LOCKED`, retried with backoff up
  to 15 minutes, dead after 12 attempts, and shown to operators with a retry
  button (D49).
- **Modules can subscribe** to events but cannot publish them:
  `outbox.write` is unexported, and `docs/events.md` says so.
- **No general job table exists**; importers keep their own job tables.
- **Redis is named in D10 as an optional transport, and no Redis code
  exists.**
- **Inbound payment webhooks** verify their signature, then claim
  `(store_id, event id)` in a table of their own before acting, and release
  the claim if the work fails.

Model calls take seconds, fail transiently, cost money and must never hold a
transaction (rule 5).

## Decision

1. **No Redis.** The spec asks for it only where it is already supported, and
   it is not. Everything below is Postgres.

2. **`ext/ai-gateway` owns one job table for model work, `ai_jobs`:**
   - store, id, task name, typed input reference, idempotency key (unique per
     store), status (queued, running, done, failed, dead), attempts, next run
     time, last error, trace id, created and finished times;
   - claimed with `FOR UPDATE SKIP LOCKED` and a visibility timeout, the
     outbox's own pattern;
   - retried with backoff for transient failures (timeouts, rate limits,
     provider 5xx); never retried for a refused request, a failed output
     check or an exhausted budget;
   - dead after a fixed number of attempts, and every dead job lands in the
     task's human queue rather than disappearing;
   - an operator screen lists, retries and cancels jobs, as D49 does for
     events.

3. **The transaction that creates the work also enqueues it.** The inbox
   stores an inbound message and its `ai_jobs` row in one transaction; the
   order desk stores an intake and its extraction job in one. The model call
   happens after commit, in the runner, and its result is written in a new
   transaction through the owning module's service.

4. **Core events trigger AI work through ordinary subscriptions.** A handler
   on `order.*` or `product.*` enqueues a job keyed by the event id, so the
   outbox's at-least-once delivery becomes exactly-once job creation.

5. **Modules still do not publish events of their own in M1 or M2.** Nothing
   planned needs another module to react to an AI module's change. If that
   need appears (say, outgoing webhooks for "draft ready"), opening the outbox
   to namespaced module events (`x.<module>.<event>`, written in the module's
   transaction) is a decision for then, in `PLAN.md`.

6. **Inbound channel webhooks follow the payment pattern:**
   1. verify the signature (Meta: `X-Hub-Signature-256`, HMAC-SHA256 of the
      raw body with the app secret, compared in constant time);
   2. find the store through the global index of
      [ADR 0002](0002-tenancy.md);
   3. claim `(store_id, provider event id)`;
   4. store the message and enqueue its job in one transaction;
   5. answer 200 quickly, releasing the claim on failure so the provider's
      retry is processed.

7. **Every job and every model call carries a trace id** from the request or
   event that caused it, through the execution record of
   [ADR 0005](0005-model-providers.md) and into the audit label of anything it
   proposes.

## Consequences

- A crash between "message received" and "message classified" loses nothing:
  the job is in the same commit as the message.
- Dead work is visible and has an owner (the task's human queue), which is
  what the spec's dead-letter requirement is for.
- One more polling worker per process. The outbox runner shows that
  `SKIP LOCKED` polling holds up at this engine's scale; Redis stays the
  optional transport D10 describes, unbuilt until a measured need.

## Alternatives considered

- **Put model calls in outbox handlers directly.** A handler failure retries
  the whole event for every subscriber, and the outbox's retry policy (12
  attempts within about half an hour, every one of them paid for) is wrong for
  a call that costs money each time.
- **A generic core job queue for every module.** Useful one day, but no other
  module needs it now; starting in the gateway keeps the core change out of
  M1, and the table can move to core unchanged if a second user appears.
