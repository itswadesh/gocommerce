# ADR 0001: The AI services are modules in the engine, not separate services

Status: proposed, 21 October 2026. Becomes a row in `PLAN.md` §5 when accepted.

## Context

The spec (§2) lists eleven "separate services/modules": ai-gateway,
commerce-context, mcp-server, memory, integrations, order-intelligence,
sales-inbox, procurement-intelligence, sales-intelligence,
marketing-intelligence and extension-sdk. It also asks for "Go services for
orchestration".

GoCommerce is one Go module and one binary that a store composes from modules
in `main()` (D5, D6). Its rules shape what "separate" can mean:

- Rule 1: one production dependency (`pgx`).
- Rule 2: `ext/` adds no third-party dependency; a module that needs an SDK is
  its own repository.
- Rule 3: modules never write core tables; they call services.
- Rule 9: a module mounts only under `/x/<name>/` and `/api/admin/x/<name>/`.
- D57: no runtime code loading.

## Decision

1. **Each named service is an `ext/` module in the same process**, with its own
   `<name>_` tables, its own rights, a plugin entry that installs it idle, and
   its routes under rule 9's namespaces:

   | Spec service | Module | First milestone |
   |---|---|---|
   | ai-gateway | `ext/ai-gateway` | M1 |
   | commerce-context | `ext/context` | M1 |
   | mcp-server | `ext/mcp` (exists; extended) | M1 |
   | memory | `ext/memory` | M1 |
   | integrations | one module per provider: `ext/whatsapp-cloud`, `ext/meta-ads` | M2, M5 |
   | order-intelligence | `ext/order-desk` | M2 |
   | sales-inbox | `ext/sales-inbox` | M2 |
   | procurement-intelligence | `ext/procurement` | M3 |
   | sales-intelligence | `ext/sales-intelligence` | M4 |
   | marketing-intelligence | `ext/meta-ads` (analysis only) | M5 |
   | extension-sdk | not a module: `gctest`, a module manifest, docs and a generator | M1 skeleton, M6 |

2. **A module moves out of the process only for one of three reasons**, each
   decided on its own when it arises:
   - it needs a third-party SDK (rule 2): a separate repository;
   - it does heavy CPU or memory work that would starve checkout, such as
     rendering scanned PDFs for text recognition: a separate worker that
     the module calls over HTTP, with the call after the commit (rule 5);
   - it runs code nobody has reviewed yet, such as a generated extension:
     built and tested in its own process (CI or a container), never loaded
     into the engine (D57).

3. **AI modules depend on core services and on `ext/identity` and `ext/b2b`
   through their Go APIs.** Customers and companies are module concepts
   (rule 8, D22), so the order desk asks `ext/b2b` for a company's prices and
   catalogue, the way `ext/b2b` already builds on `ext/identity`.

4. **The spec's `/v1/...` paths map onto rule 9's namespaces**, and the
   versioning the spec asks for is the module's: every module route is in the
   OpenAPI document (rule 11), changes to a published route are additive, and
   a breaking change is a new path. Proposed mapping:

   | Spec path | Here |
   |---|---|
   | `POST /v1/integrations/whatsapp/connect` | `POST /api/admin/x/whatsapp-cloud/connect` |
   | `POST /v1/webhooks/meta/whatsapp` | `POST /x/whatsapp-cloud/webhook`: one URL per install, because Meta calls one webhook per app; the store comes from the payload ([ADR 0002](0002-tenancy.md)) |
   | `GET /v1/sales-inbox/conversations` | `GET /api/admin/x/sales-inbox/conversations` |
   | `POST /v1/sales-inbox/replies/{id}/approve` | `POST /api/admin/x/sales-inbox/replies/{id}/approve` |
   | `POST /v1/ai/order-intake`, `GET …/{jobId}` | `POST /api/admin/x/order-desk/intakes`, `GET …/intakes/{id}` |
   | `POST /v1/draft-orders/{id}/approve` | `POST /api/admin/x/order-desk/drafts/{id}/approve` |
   | `POST /v1/ai/catalog-imports` | `POST /api/admin/x/procurement/catalog-imports` |
   | `POST /v1/ai/rfq-comparisons` | `POST /api/admin/x/procurement/rfq-comparisons` |
   | `GET /v1/ai/sales-opportunities` | `GET /api/admin/x/sales-intelligence/opportunities` |
   | `POST /v1/ai/followups/{id}/approve` | `POST /api/admin/x/sales-intelligence/followups/{id}/approve` |
   | `POST /v1/integrations/meta-ads/connect`, `GET /v1/analytics/meta-ads` | `POST /api/admin/x/meta-ads/connect`, `GET /api/admin/x/meta-ads/report` |
   | `GET /v1/ai/usage`, `GET /v1/ai/audit` | `GET /api/admin/x/ai-gateway/usage`, `GET /api/admin/x/ai-gateway/executions` |

5. **Documents are read with what the standard library and the model offer.**
   Spreadsheets: XLSX is a zip of XML, readable with `archive/zip` and
   `encoding/xml`; CSV with `encoding/csv`. PDFs: the standard library has no
   reader, so a PDF goes to a provider that reads documents natively, and
   only when the store's data-sharing policy allows that document class to
   leave ([ADR 0005](0005-model-providers.md)). A scanned PDF that needs text
   recognition is the second exception above.

## Consequences

- One deployment, one database, one set of row policies: every AI table is
  isolated the same way as every other store table, and the existing
  `ext/alltest` check covers them.
- A module switched off costs nothing; a store that never enables AI runs
  exactly the engine it runs today.
- The modules share the engine's failure domain. A runaway model call must not
  hold a transaction (rule 5) or a request goroutine: model work runs in the
  job runner of [ADR 0004](0004-events-and-jobs.md) with timeouts and budgets.
- Splitting a module out later is cheap because it already talks to the rest
  only through Go service calls and its own tables.

## Alternatives considered

- **Separate network services from the start.** Gives independent scaling the
  workload has not asked for, and costs a service-to-service auth scheme,
  distributed tenancy and a second deployment before the first AI feature
  ships.
- **`/v1` prefixes on new routes.** Would make two route conventions in one
  API, and rule 9's namespace check would refuse them.

## Owner decision needed

The spec says "separate services" and "versioned domain APIs". This record
reads the first as separate modules and the second as additive-only module
routes under rule 9. If the spec meant separate processes or `/v1` paths, that
overrides D5, D6, D16 and rule 9, and should be decided here.
