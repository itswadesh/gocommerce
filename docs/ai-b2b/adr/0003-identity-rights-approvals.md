# ADR 0003: Agents get their own keys, and their writes wait for a person

Status: proposed, 21 October 2026. Becomes a row in `PLAN.md` §5 when accepted.

## Context

The spec asks for tenant-scoped credentials, developers and agents
authenticated separately, read-only tools by default, and mutating tools
behind explicit scopes and approvals (§2, §3.2). No AI-generated reply,
refund, price change or publication happens without an authorised domain tool
and a policy check (§2), and sellers approve AI replies and drafts in the
first release (§4, §16).

Today:

- An API key is bound to one store and carries one of four built-in roles. It
  resolves to a `Superuser` with id 0, so every key is recorded in
  `admin_audit` as operator 0, told apart only by an "api key: name" label,
  and the actor filter merges them all. It has no expiry, and a readable copy of the secret is kept
  so the owner can see it again (M46).
- The static admin token reaches every store with every right, and the audit
  trail records it only as "token".
- `ext/mcp` runs its ten mutating tools, including `refund_order`,
  `set_variant_price` and `create_discount`, as soon as they are called by
  anyone holding the right. `Config.ReadOnly` is the only brake. `mcp_audit`
  has no actor.
- Approvals exist only inside a buying company (`b2b_approvals`, D69).

## Decision

### Who is calling

1. **Three kinds of caller, told apart in every audit row.**

   | Caller | Credential | How it appears in `admin_audit` |
   |---|---|---|
   | A person (operator, developer) | Panel session, as today | `operator`, their id |
   | A script or integration | API key, as today | `key`, the key's id and name |
   | An agent | **Agent key** (new) | `agent`, the key's id and name, plus the label of the run |

2. **An agent key is an API key with `kind = agent`** and four differences:
   - it carries an explicit list of rights, a subset of its issuer's, instead
     of a role;
   - it expires, 90 days by default;
   - only its hash is kept; it is shown once and never again (M46's readable
     copy stays for integration keys only);
   - it cannot manage keys, roles, members, plugins or store settings, whatever
     rights it lists.

3. **Agent work never runs on the static admin token.** AI modules and agent
   tool calls refuse it, because it is bound to no store and names no one.
   Stdio MCP keeps working for a developer's local store and is treated as the
   developer: read tools run, and mutating tools propose (below).

### What an agent may do

4. **Reads run directly**, within the key's rights, and are recorded
   (`mcp_audit` gains an actor and records reads). Personal data is masked for
   agents on every store, not only demo stores, unless the key holds the right
   to see it and the task needs it.

5. **Writes split in two:**
   - *Records only staff can see* (drafts, notes, exception items, classified
     messages, proposed matches) are written by the agent directly through the
     owning module's service, audited as the agent.
   - *Anything that reaches a customer, moves money or stock, or changes a
     price, a catalogue or a published page* is never executed by an agent.
     The agent files a proposal; a person approves it. In the first release
     this covers every one of `ext/mcp`'s existing mutating tools when called
     with an agent key.

### Proposals and approval

6. **One approval object for the store side**, owned by `ext/ai-gateway`:

   - `approval_requests`:
     - store, id, kind (the tool or action name), typed arguments, a preview,
       an idempotency key;
     - requested by (agent key and run), status (pending, approved, rejected,
       expired, executed, failed), expiry;
     - decided by, decided at, the result.
   - **Approving runs the action as the approver.** The module calls the same
     service method the tool wraps, with the approver on the context and
     `WithActorLabel` naming the proposal. Core writes its own audit row and
     outbox event in that transaction, as it does for a person clicking the
     button. No AI module writes a core table (rule 3).
   - **The approver must hold the action's rights** and must be a person;
     an agent key can never approve, including its own proposal.
   - **The preview is recomputed when the person opens it and again at
     approval.** A price, stock level or credit limit that moved since the
     proposal shows as a change, and the action runs against current state,
     never against the snapshot.
   - **Executing is idempotent** on the proposal's key, so a double click or
     a retried request runs the action once.
   - **Proposals expire**, 7 days by default; an expired proposal cannot run.

7. **New rights, registered by the modules (D65):**
   - `ai.read`: executions and usage
   - `ai.configure`: providers, keys, budgets, data-sharing
   - `ai.approve`: open the approval queue; executing still needs the action's
     own right
   - `inbox.read` and `inbox.reply`: M2

## Consequences

- "Which agent did that, and who let it" has an answer in one table, for every
  write an agent caused.
- Existing integrations and scripts keep working unchanged; only agent keys
  get the new rules.
- A developer working in Claude Code with their own session keeps today's
  behaviour; the spec's separation is between that person and an agent
  running unattended.
- Core changes needed:
  - an actor kind for keys and agents in `admin_audit`, and its vocabulary
    opened to module actions;
  - rights lists and expiry on API keys;
  - masking for agent callers on all stores.

  These are the first M1 tasks, because every later milestone relies on them.

## Alternatives considered

- **Reuse `b2b_approvals`.** It approves a buyer's basket inside a company; a
  seller approving an agent's refund is a different party, object and right.
- **Let agents run writes and rely on undo.** Refunds, messages and published
  prices cannot be undone, and the spec rules it out.
- **Approve by role instead of by right.** A role is a bundle that changes;
  the right the action needs is the fact being checked.

## Owner decision needed

- Agent keys keep no readable copy, unlike M46's integration keys. This
  reverses M46 for agents only.
- Which writes count as "only staff can see" is a list the owner should read;
  it is enforced in code, one tool at a time.
