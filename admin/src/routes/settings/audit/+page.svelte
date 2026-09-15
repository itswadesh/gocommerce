<script>
    /**
     * The operator audit trail: who did what in this store, and when.
     *
     * The engine has recorded this since M20 — every mutating service writes a
     * row inside the same transaction as the change it describes — and nothing
     * displayed it. An order's own history reached the order drawer and a
     * variant's stock moves reached the stock drawer, but "who changed this
     * price", "who edited that tax rate" and "who invited this person" were
     * answerable only with curl. This is the reader for the store-wide feed.
     *
     * Behind `store.operate`, which is the right the API puts it behind and for
     * the reason audit_http.go states: filtering the whole store by person is
     * surveillance of the team, so it is deliberately a heavier right than the
     * per-record histories, which each follow their own record's read right.
     *
     * The URL parameters ARE the API's parameters — `actor_kind`, `actor_id`,
     * `entity_type`, `action`, `from`, `to` — so an address off this screen is
     * the request that produced it, and a filter cannot be declared here and
     * silently dropped on the way out.
     */
    import { base } from "$app/paths";
    import { api, can, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import SettingsSidebar from "$lib/components/SettingsSidebar.svelte";
    import AuditChanges from "$lib/components/AuditChanges.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    /*
     * The entity vocabulary, in core/audit.go's own display order
     * (AuditEntityTypes). It is a closed list the engine declares and no route
     * serves, so it is copied here rather than fetched — and copying it is safe
     * in a way copying the rights table was not: an entity_type the feed does
     * not know is an empty page, never a 400 (audit_http.go says so
     * explicitly), so a list that falls behind loses a filter option and
     * nothing else.
     */
    const ENTITIES = [
        { value: "order", many: "Orders", one: "Order" },
        { value: "product", many: "Products", one: "Product" },
        // Both forms written out rather than derived by trimming an "s":
        // "Categories" does not singularise that way, and a filter labelled
        // "Categorie" is the kind of thing nobody reports and everybody sees.
        { value: "category", many: "Categories", one: "Category" },
        { value: "collection", many: "Collections", one: "Collection" },
        { value: "discount", many: "Discounts", one: "Discount" },
        { value: "tax_rate", many: "Tax rates", one: "Tax rate" },
        { value: "location", many: "Locations", one: "Location" },
        { value: "superuser", many: "Operators", one: "Operator" },
        { value: "invitation", many: "Invitations", one: "Invitation" },
        { value: "role", many: "Roles", one: "Role" },
        { value: "taxonomy_attribute", many: "Attributes", one: "Attribute" },
    ];

    const ENTITY_TYPES = [
        { value: "", label: "Anything" },
        ...ENTITIES.map((e) => ({ value: e.value, label: e.many })),
    ];

    const ENTITY_LABEL = Object.fromEntries(ENTITIES.map((e) => [e.value, e.one]));

    /*
     * Where a record of each kind is read, so "who changed this" leads to the
     * thing that was changed. Only the types that have a screen addressed by
     * the record's own id; the rest are named and not linked, which is honest —
     * a link to a listing that does not select the row is a worse answer than
     * no link.
     */
    const ENTITY_HREF = {
        order: (id) => `/orders/${id}`,
        product: (id) => `/products/${id}`,
    };

    /* The URL keys are the API's, so list.query() builds the request with no
       translation step in between — the class of bug where a filter is declared
       on the screen and never reaches the server. */
    const list = listState({
        actor_kind: "",
        actor_id: "",
        entity_type: "",
        // Declared even though no control sets it: `.query()` serialises only
        // the keys named here, so the drawer's "everything that happened to
        // this record" link would land on an address the request then ignored.
        entity_id: "",
        action: "",
        from: "",
        to: "",
        page: 1,
        limit: PER_PAGE,
    });
    const perPage = $derived(list.params.limit);

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let actors = $state([]);

    let draftAction = $state(list.params.action);

    let detailOpen = $state(false);
    let entry = $state(null);

    const allowed = $derived(can("store.operate"));

    $effect(() => {
        // Any parameter changing reloads: the actor, the entity, the dates,
        // the page.
        list.params;
        // Gated here as well as in the template, so a role without the right
        // does not fire two 403s at a screen that is already explaining itself.
        if (!allowed) return;
        load();
    });

    $effect(() => {
        if (!allowed) return;
        loadActors();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/audit" + list.query({ page: list.page, limit: perPage }),
            );
            rows = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /**
     * The Who picker.
     *
     * /audit/actors aggregates the trail rather than listing the team, which is
     * the right source for a filter: an operator who has been removed still has
     * a past here, and a colleague who has never changed anything would be an
     * option that can only ever return nothing.
     */
    async function loadActors() {
        try {
            const result = await api.get("/api/admin/audit/actors");
            actors = result.data ?? [];
        } catch {
            // A picker that cannot be built is one filter missing, not a broken
            // screen — the feed itself is unaffected, and its own failure is
            // already toasted.
            actors = [];
        }
    }

    /*
     * One control, two parameters. An operator is `actor_id`; a token and the
     * engine's own background work are `actor_kind` with no id, because there
     * is no row to point at. The composite value keeps that a detail of this
     * picker rather than something the URL or the API has to know about.
     */
    const actorValue = $derived(
        list.params.actor_id
            ? `id:${list.params.actor_id}`
            : list.params.actor_kind
              ? `kind:${list.params.actor_kind}`
              : "",
    );

    function chooseActor(value) {
        if (!value) return list.set({ actor_id: "", actor_kind: "" });
        const colon = value.indexOf(":");
        const tag = value.slice(0, colon);
        const rest = value.slice(colon + 1);
        if (tag === "id") return list.set({ actor_id: rest, actor_kind: "" });
        return list.set({ actor_kind: rest, actor_id: "" });
    }

    const actorOptions = $derived([
        { value: "", label: "Anybody" },
        ...actors.map((a) => ({
            value: a.id ? `id:${a.id}` : `kind:${a.kind}`,
            // The count is on the option because it is what makes the list
            // worth reading: it says who is actually active in this store.
            label: `${actorName(a)} · ${a.acts} ${a.acts === 1 ? "act" : "acts"}`,
        })),
    ]);

    /** Who, in the words the trail actually has. Matches OrderTimeline, which
     *  reads the same rows through the order's merged history. */
    function actorName(a) {
        if (a.email) return a.email;
        if (a.label) return a.label;
        if (a.kind === "token") return "An admin token";
        if (a.kind === "system") return "The store itself";
        return a.kind;
    }

    function submitAction(e) {
        e.preventDefault();
        list.set({ action: draftAction.trim() });
    }

    function open(row) {
        entry = row;
        detailOpen = true;
    }

    /** Where the record this row is about is read, or "" when it has no screen
     *  addressed by its own id. */
    function recordHref(row) {
        const make = ENTITY_HREF[row.entity_type];
        if (!make || !row.entity_id) return "";
        return `${base}${make(row.entity_id)}`;
    }

    const emptyMessage = $derived(
        list.pristine
            ? "Nothing recorded yet. The trail starts at the first change made after this store " +
                  "was upgraded — anything older than that happened before there was anywhere " +
                  "to write it down."
            : "No recorded action matches that. Try clearing a filter.",
    );
</script>

<svelte:head><title>Audit trail · GoCommerce</title></svelte:head>

<div class="page page-audit shopify-skin">
    <SettingsSidebar />

    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Audit trail</div>
            </nav>

            <!-- The filters belong to a screen this operator can read. Left
                 standing beside the refusal below they are controls over
                 nothing, which reads as a broken screen rather than as a
                 permission. -->
            {#if allowed}
                <form class="fields searchbar" onsubmit={submitAction}>
                    <div class="field">
                        <input
                            type="text"
                            class="p-l-20"
                            placeholder="Filter by action, e.g. order.refund"
                            bind:value={draftAction}
                        />
                    </div>
                    {#if draftAction || list.params.action}
                        <div class="field addon p-r-5">
                            {#if draftAction !== list.params.action}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button
                                type="button"
                                class="btn sm pill secondary transparent"
                                onclick={() => ((draftAction = ""), list.set({ action: "" }))}
                            >
                                Clear
                            </button>
                        </div>
                    {/if}
                </form>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        disabled={loading}
                        onclick={() => load()}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                <div class="page-header-primary-btns">
                    <div class="field">
                        <Select
                            id="audit-actor"
                            ariaLabel="Who"
                            value={actorValue}
                            options={actorOptions}
                            onchange={chooseActor}
                        />
                    </div>
                    <div class="field">
                        <Select
                            id="audit-entity"
                            ariaLabel="What was changed"
                            value={list.params.entity_type}
                            options={ENTITY_TYPES}
                            onchange={(v) => list.set({ entity_type: v })}
                        />
                    </div>
                    <div class="field">
                        <input
                            type="date"
                            aria-label="From"
                            value={list.params.from}
                            onchange={(e) => list.set({ from: e.currentTarget.value })}
                        />
                    </div>
                    <div class="field">
                        <!-- Exclusive, as every other date range in this engine
                             is, and said on the control rather than discovered
                             from a missing row. -->
                        <input
                            type="date"
                            aria-label="To (exclusive)"
                            value={list.params.to}
                            onchange={(e) => list.set({ to: e.currentTarget.value })}
                        />
                    </div>
                    <!-- The one filter with no control of its own, set from a
                         drawer. Without a chip saying so, a feed narrowed to one
                         record reads as a feed that has lost most of its rows. -->
                    {#if list.params.entity_id}
                        <!-- `.label.handle` is PocketBase's clickable chip, and
                             a button is what this is: pressing it widens the
                             feed back to every record. -->
                        <button
                            type="button"
                            class="label info handle"
                            title="Showing one record only — press to see every record again"
                            onclick={() => list.set({ entity_id: "" })}
                        >
                            one record · #{list.params.entity_id}
                            <i class="ri-close-line" aria-hidden="true"></i>
                        </button>
                    {/if}
                    {#if !list.pristine}
                        <button
                            type="button"
                            class="btn secondary"
                            onclick={() => ((draftAction = ""), list.clear())}
                        >
                            <span class="txt">Clear filters</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if !allowed}
            <!-- The sidebar already hides the link; this is the direct URL. -->
            <NoAccess right="store.operate" what="the audit trail" />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">What happened</th>
                            <th class="col-field-type-select">Who</th>
                            <th class="col-field-type-text">Record</th>
                            <th class="col-field-type-date min-width">When</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr
                                class="handle"
                                tabindex="0"
                                onclick={() => open(row)}
                                onkeydown={(e) => rowKey(e, () => open(row))}
                            >
                                <td class="col-field-name-id" data-name="What happened">
                                    <div class="row-name">
                                        <!-- The engine's own sentence leads and
                                             the action name sits under it: the
                                             summary is what an operator reads,
                                             and `order.refund_settle` is what
                                             they would paste into the filter. -->
                                        <span class="txt-ellipsis">{row.summary}</span>
                                        <span class="txt-hint txt-sm row-handle txt-code">
                                            {row.action}
                                        </span>
                                    </div>
                                </td>
                                <td class="col-field-type-select" data-name="Who">
                                    {#if row.actor_email}
                                        <span class="txt-ellipsis">{row.actor_email}</span>
                                        {#if row.actor_role}
                                            <span class="label sm">{row.actor_role}</span>
                                        {/if}
                                    {:else if row.actor_label}
                                        <span class="txt-ellipsis">{row.actor_label}</span>
                                    {:else if row.actor_kind === "token"}
                                        <!-- Not a person. Saying "an admin
                                             token" rather than leaving it blank
                                             is the difference between "a script
                                             did this" and "nobody knows". -->
                                        <span class="label warning">admin token</span>
                                    {:else if row.actor_kind === "system"}
                                        <span class="label">the store itself</span>
                                    {:else}
                                        <span class="txt-hint">—</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-text" data-name="Record">
                                    <span class="txt-hint txt-sm">
                                        {ENTITY_LABEL[row.entity_type] ?? row.entity_type}
                                    </span>
                                    {#if row.entity_label}
                                        <span class="txt-ellipsis">{row.entity_label}</span>
                                    {:else}
                                        <span class="txt-code txt-sm">#{row.entity_id}</span>
                                    {/if}
                                </td>
                                <td
                                    class="col-field-type-date min-width txt-hint"
                                    data-name="When"
                                    title={formatDate(row.created_at)}
                                >
                                    {relativeTime(row.created_at)}
                                </td>
                                <td class="col-meta min-width">
                                    <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                                </td>
                            </tr>
                        {/each}

                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr>
                                <td colspan="5" class="txt-center txt-hint p-base">
                                    {emptyMessage}
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>

            <div class="field-help">
                Every operator action is written in the same transaction as the change it
                describes, so a row here and the thing it describes are true together or not at
                all. Nothing can edit or delete one — there is no route that could.
            </div>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager
                    {meta}
                    {loading}
                    noun="action"
                    {perPage}
                    onpage={(n) => list.setPage(n)}
                    onperpage={(n) => list.set({ limit: n })}
                />
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<Drawer open={detailOpen} size="lg" title="Recorded action" onclose={() => (detailOpen = false)}>
    {#if entry}
        <!-- The order drawer's own card recipe, reused rather than re-cut. -->
        <div class="order-detail">
            <div class="order-status">
                <span class="txt-code txt-bold">{entry.action}</span>
                <div class="flex-fill"></div>
                <span class="txt-hint txt-sm">#{entry.id}</span>
            </div>

            <section class="order-card">
                <h6 class="order-card-title m-b-10">What happened</h6>
                <div class="list">
                    <div class="list-item">
                        <span class="txt-hint">Summary</span>
                        <div class="flex-fill"></div>
                        <span class="txt">{entry.summary}</span>
                    </div>
                    <div class="list-item">
                        <span class="txt-hint">Who</span>
                        <div class="flex-fill"></div>
                        <span class="txt">
                            {actorName({
                                email: entry.actor_email,
                                label: entry.actor_label,
                                kind: entry.actor_kind,
                            })}
                        </span>
                        {#if entry.actor_role}
                            <span class="label sm">{entry.actor_role}</span>
                        {/if}
                    </div>
                    <div class="list-item">
                        <span class="txt-hint">Record</span>
                        <div class="flex-fill"></div>
                        <span class="txt">
                            {ENTITY_LABEL[entry.entity_type] ?? entry.entity_type}
                            {entry.entity_label ? `· ${entry.entity_label}` : ""}
                        </span>
                        {#if recordHref(entry)}
                            <a class="btn sm secondary" href={recordHref(entry)}>
                                <span class="txt">Open</span>
                            </a>
                        {/if}
                        <!-- The whole of one record's story, which is the
                             question this drawer usually raises: the row says
                             what changed once, and the next thing anybody wants
                             is what else has happened to it. -->
                        <button
                            type="button"
                            class="btn sm transparent secondary"
                            onclick={() => {
                                detailOpen = false;
                                list.set({
                                    entity_type: entry.entity_type,
                                    entity_id: entry.entity_id,
                                    actor_kind: "",
                                    actor_id: "",
                                });
                            }}
                        >
                            <span class="txt">Its history</span>
                        </button>
                    </div>
                    <div class="list-item">
                        <span class="txt-hint">When</span>
                        <div class="flex-fill"></div>
                        <span class="txt">{formatDate(entry.created_at)}</span>
                    </div>
                    {#if entry.changes?.event}
                        <!-- The join between the trail and the outbox: the same
                             transaction wrote both, and this is the name it
                             announced the change under. The link lands on the
                             events screen already filtered to it. -->
                        <div class="list-item">
                            <span class="txt-hint">Announced as</span>
                            <div class="flex-fill"></div>
                            <a
                                class="txt-code"
                                href="{base}/settings/events{query({
                                    name: entry.changes.event,
                                    state: 'any',
                                })}"
                            >
                                {entry.changes.event}
                            </a>
                        </div>
                    {/if}
                </div>
            </section>

            <AuditChanges changes={entry.changes} />
        </div>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (detailOpen = false)}>
            <span class="txt">Close</span>
        </button>
    {/snippet}
</Drawer>
