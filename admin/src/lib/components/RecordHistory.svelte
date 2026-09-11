<script module>
    /**
     * The five records the engine keeps a per-record history for, and what each
     * one is called and gated on.
     *
     * The keys are the URL segment, because that is what the routes are:
     * `GET /api/admin/{products|categories|discounts|tax-rates|locations}/{id}/history`
     * (core/audit_http.go). There is deliberately no `orders` entry — an order's
     * history is the merged timeline, which carries the outbox's delivery state
     * as well, and OrderTimeline is its reader. There is no `variants` entry
     * either: since M26 a variant's history is its stock ledger, which
     * StockHistory reads.
     *
     * Each right is the one the route already checks. Naming it here is not a
     * second permission decision — the engine's answer is still the engine's —
     * it is so the drawer can say "you do not have this" instead of opening
     * empty on a 403.
     */
    export const HISTORY_KINDS = {
        products: { right: "catalog.read", one: "product" },
        categories: { right: "catalog.read", one: "category" },
        discounts: { right: "discounts.read", one: "discount" },
        "tax-rates": { right: "taxes.read", one: "tax rate" },
        locations: { right: "locations.read", one: "location" },
    };

    /* What each recorded act is called on screen. A verb missing from this
       table still renders — under the engine's own summary, which is a whole
       sentence — so an action added later appears as a plain line rather than
       disappearing. */
    const ACTIONS = {
        "product.create": { icon: "ri-add-line", label: "Created" },
        "product.update": { icon: "ri-pencil-line", label: "Edited" },
        "product.delete": { icon: "ri-delete-bin-line", label: "Deleted" },
        "product.option_add": { icon: "ri-list-settings-line", label: "Option added" },
        "product.options_set": { icon: "ri-list-settings-line", label: "Options set" },
        "product.media_set": { icon: "ri-image-line", label: "Pictures changed" },
        "product.collections_set": { icon: "ri-stack-line", label: "Collections changed" },
        "product.import": { icon: "ri-upload-2-line", label: "Imported" },
        "variant.create": { icon: "ri-add-line", label: "Variant added" },
        "variant.update": { icon: "ri-pencil-line", label: "Variant edited" },
        "variant.delete": { icon: "ri-delete-bin-line", label: "Variant deleted" },
        "variant.media_set": { icon: "ri-image-line", label: "Variant pictures changed" },
        "category.create": { icon: "ri-add-line", label: "Created" },
        "category.update": { icon: "ri-pencil-line", label: "Edited" },
        "category.delete": { icon: "ri-delete-bin-line", label: "Deleted" },
        "discount.create": { icon: "ri-add-line", label: "Created" },
        "discount.update": { icon: "ri-pencil-line", label: "Edited" },
        "discount.delete": { icon: "ri-delete-bin-line", label: "Deleted" },
        "tax_rate.create": { icon: "ri-add-line", label: "Created" },
        "tax_rate.update": { icon: "ri-pencil-line", label: "Edited" },
        "tax_rate.delete": { icon: "ri-delete-bin-line", label: "Deleted" },
        "location.create": { icon: "ri-add-line", label: "Created" },
        "location.update": { icon: "ri-pencil-line", label: "Edited" },
        "location.delete": { icon: "ri-delete-bin-line", label: "Deleted" },
        "location.set_default": { icon: "ri-star-line", label: "Made the default" },
    };

    /* The audit feed filters on the engine's entity vocabulary, which is
       singular and underscored, while the routes are plural and hyphenated.
       One place that knows both, rather than every caller having to. */
    const ENTITY_TYPE = {
        products: "product",
        categories: "category",
        discounts: "discount",
        "tax-rates": "tax_rate",
        locations: "location",
    };

    function entityTypeOf(kind) {
        return ENTITY_TYPE[kind] ?? "";
    }
</script>

<script>
    /**
     * One record's own history: who changed this price, this rate, this rule.
     *
     * The engine has served these five routes since M20 and no screen called
     * any of them. The only audit reader in the panel was /settings/audit,
     * behind `store.operate` — and audit_http.go is explicit that the split is
     * the point: filtering the whole store by person is surveillance of the
     * team, while the history of one record is part of that record. A manager
     * who has to answer "who dropped this price" holds `catalog.read` and not
     * `store.operate`, and until this existed they could not answer it.
     *
     * The shape is OrderTimeline's, because it is the same question asked of a
     * different record: a list of acts, newest first, each one a phrase, the
     * fields it moved, who did it and when. What it adds is the before and
     * after, expanded one row at a time — that is the whole reason somebody
     * opens a price's history rather than reading the price — and AuditChanges
     * is the renderer for it, the same one the store-wide feed uses.
     *
     * Mounting it: give it the record's own URL segment, its id and something
     * to call it.
     *
     *     <RecordHistory
     *         open={historyOpen}
     *         kind="tax-rates"
     *         id={editing?.id}
     *         label={editing?.name}
     *         onclose={() => (historyOpen = false)}
     *     />
     */
    import { base } from "$app/paths";
    import { api, can, query } from "$lib/api.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { rowKey } from "$lib/rowkey.js";
    import { toast } from "$lib/toast.svelte.js";
    import AuditChanges from "$lib/components/AuditChanges.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import Pager from "$lib/components/Pager.svelte";

    const PER_PAGE = 25;

    let { open = false, kind = "", id = null, label = "", onclose } = $props();

    const shape = $derived(HISTORY_KINDS[kind] ?? { right: "", one: "record" });
    const allowed = $derived(!shape.right || can(shape.right));

    let entries = $state([]);
    let meta = $state(null);
    let loading = $state(false);
    let page = $state(1);
    let perPage = $state(PER_PAGE);
    let expanded = $state(null);

    /* A different record, or a re-open, starts at the top. Without this,
       opening the history of the next tax rate would land on page 3 of the
       previous one's. */
    $effect(() => {
        kind;
        id;
        open;
        page = 1;
        expanded = null;
    });

    $effect(() => {
        // Nothing is fetched for a closed drawer: every list screen mounts one
        // of these, and a history is not what the screen is for.
        if (!open || !id || !allowed) return;
        load(kind, id, page, perPage);
    });

    /* Two page presses leave two requests in flight, and the list would
       otherwise settle on whichever reply landed last. */
    let reqId = 0;

    async function load(k, recordID, pageNumber, limit) {
        const mine = ++reqId;
        loading = true;
        try {
            const result = await api.get(
                `/api/admin/${k}/${recordID}/history` + query({ page: pageNumber, limit }),
            );
            if (mine !== reqId) return;
            entries = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function look(entry) {
        return (
            ACTIONS[entry.action] ?? {
                icon: "ri-information-line",
                label: entry.summary || entry.action,
            }
        );
    }

    /** Who did it, in the words the trail actually has — OrderTimeline and the
     *  store-wide feed say the same three things the same way. */
    function who(entry) {
        if (entry.actor_email) return entry.actor_email;
        if (entry.actor_label) return entry.actor_label;
        if (entry.actor_kind === "token") return "an admin token";
        if (entry.actor_kind === "system") return "the store itself";
        return "";
    }

    /** Which fields the act moved, named rather than valued: the values are one
     *  press away, and a history is a list. */
    function fields(entry) {
        const after = Object.keys(entry.changes?.after ?? {});
        const before = Object.keys(entry.changes?.before ?? {});
        const all = [...new Set([...after, ...before])];
        if (!all.length) return "";
        return "Changed " + all.map((f) => f.replace(/_/g, " ")).sort().join(", ");
    }

    function toggle(entry) {
        expanded = expanded === entry.id ? null : entry.id;
    }
</script>

<Drawer
    {open}
    size="lg"
    title={label ? `History · ${label}` : "History"}
    onclose={() => onclose?.()}
>
    {#if !allowed}
        <p class="txt-hint">
            Reading this {shape.one}'s history needs
            <span class="txt-code">{shape.right}</span>, which this account does not carry.
        </p>
    {:else}
        <!-- `.order-history` rather than a class of this component's own: those
             rules in gocommerce.css are about a chronology's shape — a fixed
             icon column so every phrase starts at the same x, a text column
             that grows from zero, a timestamp that drops under the phrase on a
             phone — and none of that is specific to an order. A second
             identical ruleset would be the worse answer. -->
        <div class="list order-history">
            {#each entries as entry (entry.id)}
                {@const look_ = look(entry)}
                {@const actor = who(entry)}
                {@const moved = fields(entry)}
                <!-- Clickable, where OrderTimeline is not, and for a reason
                     rather than by drift: an order's timeline answers "what
                     happened", which the phrase already says. This one answers
                     "what was it before", and that is only in the change
                     block. -->
                <div
                    class="list-item handle"
                    role="button"
                    tabindex="0"
                    aria-expanded={expanded === entry.id}
                    onclick={() => toggle(entry)}
                    onkeydown={(e) => rowKey(e, () => toggle(entry))}
                >
                    <i class={look_.icon} aria-hidden="true"></i>
                    <div class="order-item-text">
                        {look_.label}
                        <div class="txt-hint txt-sm">
                            {entry.summary}{#if actor}&nbsp;·&nbsp;by {actor}{/if}
                        </div>
                        {#if moved}
                            <div class="txt-hint txt-sm">{moved}</div>
                        {/if}
                    </div>
                    <div class="flex-fill"></div>
                    {#if entry.actor_role}
                        <span class="label sm">{entry.actor_role}</span>
                    {/if}
                    <span
                        class="txt-hint txt-sm order-history-when"
                        title={formatDate(entry.created_at)}
                    >
                        {relativeTime(entry.created_at)}
                    </span>
                    <i
                        class={expanded === entry.id ? "ri-arrow-up-s-line" : "ri-arrow-down-s-line"}
                        aria-hidden="true"
                    ></i>
                </div>

                {#if expanded === entry.id}
                    <AuditChanges changes={entry.changes} />
                {/if}
            {/each}

            {#if loading && !entries.length}
                {#each Array(4) as _, i (i)}
                    <div class="list-item"><span class="skeleton-loader"></span></div>
                {/each}
            {/if}

            {#if !loading && !entries.length}
                <!-- An empty history is a real answer and not a failure: the
                     trail starts at the first change made after this store was
                     upgraded, and everything older happened before there was
                     anywhere to write it down. audit_http.go never 404s here
                     for the same reason. -->
                <p class="txt-hint txt-sm">
                    Nothing recorded for this {shape.one} yet. The trail starts at the first
                    change made after this store was upgraded.
                </p>
            {/if}
        </div>

        {#if meta && meta.total > entries.length}
            <div class="flex m-t-sm">
                <Pager
                    {meta}
                    {loading}
                    noun="change"
                    {perPage}
                    onpage={(n) => (page = n)}
                    onperpage={(n) => ((perPage = n), (page = 1))}
                />
            </div>
        {/if}

        {#if can("store.operate")}
            <!-- The way out to the whole store's feed, for the one operator who
                 has it. Pre-filtered to this record, which is the same address
                 that screen's own "Its history" button builds. -->
            <div class="field-help m-t-sm">
                <a
                    href="{base}/settings/audit{query({
                        entity_type: entityTypeOf(kind),
                        entity_id: id,
                    })}"
                >
                    See this in the store-wide audit trail
                </a>
            </div>
        {/if}
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Close</span>
        </button>
    {/snippet}
</Drawer>
