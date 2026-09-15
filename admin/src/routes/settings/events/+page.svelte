<script>
    /**
     * Events: what the store told the outside world, and what it could not.
     *
     * Every state change writes a row to `outbox_events` inside the transaction
     * that caused it, and a dispatcher delivers them. A delivery that keeps
     * failing gives up after twelve attempts and the row is parked as dead with
     * the failure text on it — which, until this screen, nothing but the
     * doctor's own check ever read. The remedy the documentation handed out was
     * an `UPDATE` in psql.
     *
     * So the screen is a reader and two repairs. Retry is one button doing two
     * different things, and the difference matters: un-parking a dead letter
     * grants it a fresh twelve-attempt budget, because somebody fixed the cause;
     * bringing a merely backed-off event forward does NOT, because `attempts` is
     * the whole dead-letter bound and an impatient operator must not be able to
     * hold a permanently broken event in retry forever. The engine enforces
     * both; this screen says which one the button is about to do.
     *
     * It opens on the dead letters, because that is the question it exists to
     * answer. The filter is rendered in its active state rather than applied
     * silently — the API applies no default of its own, and a view that quietly
     * drops rows is worse than a noisy one.
     */
    import { base } from "$app/paths";
    import { can, events } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { eventStateClass, formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    /* Display only, and a copy of the engine's outboxMaxAttempts. The API
       reports what a row has spent, not the budget it was given — "3" alone
       says nothing about how close to dead it is. */
    const MAX_ATTEMPTS = 12;

    /* Filters and page live in the URL, and the window replaces the rows rather
       than accumulating them, so `?page=3` reopens as page 3 of the same list.
       "any" rather than "" is the all-states value because a parameter equal to
       its default is dropped from the address, and the default here is a view
       rather than "unfiltered". */
    const list = listState({ state: "dead", name: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);


    const state = $derived(list.params.state);
    const name = $derived(list.params.name);

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let draftName = $state(list.params.name);

    /* The dead count drives the bulk button and the "of N" in its toast. When
       the screen is already showing the dead letters it is meta.total and no
       second request is worth making. */
    let deadCount = $state(0);

    let detailOpen = $state(false);
    let event = $state(null);
    let detailLoading = $state(false);
    let retrying = $state(false);

    let confirmOpen = $state(false);
    let confirmConfig = $state({});

    $effect(() => {
        // Any parameter changing reloads: the state, the name, the page.
        list.params;
        // Gated here as well as in the template: a role without the right would
        // otherwise fire two 403s on mount and put a console error on a screen
        // that is already saying, correctly, that it is not available.
        if (!can("store.operate")) return;
        load();
    });

    async function load() {
        loading = true;
        try {
            const result = await events.list({
                state: state === "any" ? "" : state,
                name,
                page: list.page,
                limit: perPage,
            });
            rows = result.data ?? [];
            meta = result.meta;
            if (state === "dead" && !name) {
                deadCount = meta?.total ?? 0;
            } else {
                await countDead();
            }
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* One indexed count against outbox_dead_idx — `remaining` from a requeue is
       the same number, so the two agree by construction. */
    async function countDead() {
        try {
            const result = await events.list({ state: "dead", limit: 1 });
            deadCount = result.meta?.total ?? 0;
        } catch {
            // A count that cannot be read hides the bulk button, which is the
            // safe direction: the operator loses a shortcut, not a fact.
            deadCount = 0;
        }
    }

    function submitName(e) {
        e.preventDefault();
        list.set({ name: draftName.trim() });
    }

    async function open(row) {
        // The row already holds everything the table showed; the drawer opens on
        // it and fills in the payload when it arrives.
        event = row;
        detailOpen = true;
        detailLoading = true;
        try {
            event = await events.get(row.id);
        } catch (err) {
            // Close rather than leave a summary standing with no payload under
            // it: an event that is gone reads as an empty one otherwise, which
            // is a different fact.
            detailOpen = false;
            toast.error(err);
        } finally {
            detailLoading = false;
        }
    }

    async function retry() {
        retrying = true;
        try {
            const repaired = await events.retry(event.id);
            // The response says which of the two acts happened: an un-parked row
            // comes back with a fresh budget, an expedited one keeps its own.
            toast.success(
                repaired.attempts === 0
                    ? "Un-parked. The dispatcher will take it within a second."
                    : "Brought forward. Attempt " +
                          (repaired.attempts + 1) +
                          " of " +
                          MAX_ATTEMPTS +
                          " will run within a second.",
            );
            detailOpen = false;
            await load();
        } catch (err) {
            // The engine's own sentence, verbatim: both 409s — a delivered event
            // and a row somebody else is requeueing — explain themselves.
            toast.error(err);
        } finally {
            retrying = false;
        }
    }

    function confirmExpedite() {
        confirmConfig = {
            title: "Deliver now?",
            message:
                "This brings the next attempt forward. It does not reset the attempt count — " +
                "the event still gives up after " +
                MAX_ATTEMPTS +
                " tries.",
            confirmLabel: "Deliver now",
            run: retry,
        };
        confirmOpen = true;
    }

    function confirmRetryAll() {
        confirmConfig = {
            title: "Retry every dead letter?",
            message:
                "Up to 500 parked events are made deliverable again, each with a fresh attempt " +
                "budget. Fix what was failing first, or they will simply park again.",
            confirmLabel: "Retry them",
            run: retryAllDead,
        };
        confirmOpen = true;
    }

    async function retryAllDead() {
        try {
            const result = await events.retryDead(name);
            // `remaining` is counted in the same transaction as the requeue, so
            // "500 of 3,214" is one consistent answer rather than two requests
            // disagreeing about a table the dispatcher is already draining.
            toast.success(
                result.remaining > 0
                    ? `Retried ${result.requeued} of ${result.requeued + result.remaining} dead events — run it again for the rest.`
                    : `Retried ${result.requeued} dead events — none left.`,
            );
            await load();
        } catch (err) {
            toast.error(err);
        }
    }

    const emptyMessage = $derived(
        list.pristine
            ? "Nothing is parked. Every event this store has written was delivered, or is still on its way."
            : "No event matches that. Try clearing a filter.",
    );
</script>

<svelte:head><title>Events · GoCommerce</title></svelte:head>

<div class="page page-events shopify-skin">

    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Events</div>
            </nav>

            <!-- The filters belong to a screen this operator can read. Left
                 standing beside the refusal below they are controls over
                 nothing, which reads as a broken screen rather than a
                 permission. -->
            {#if can("store.operate")}
                <form class="fields searchbar" onsubmit={submitName}>
                    <div class="field">
                        <input
                            type="text"
                            class="p-l-20"
                            placeholder="Filter by event name, e.g. order.paid"
                            bind:value={draftName}
                        />
                    </div>
                    {#if draftName || name}
                        <div class="field addon p-r-5">
                            {#if draftName !== name}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button
                                type="button"
                                class="btn sm pill secondary transparent"
                                onclick={() => ((draftName = ""), list.set({ name: "" }))}
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
                            id="event-state"
                            ariaLabel="State"
                            value={state}
                            options={[
                                { value: "dead", label: "Dead letters" },
                                { value: "pending", label: "Pending" },
                                { value: "published", label: "Delivered" },
                                { value: "any", label: "Any state" },
                            ]}
                            onchange={(v) => list.set({ state: v })}
                        />
                    </div>
                    <!-- Only when there is something to do: a button that always
                         says "retry 0" trains people to ignore it. -->
                    {#if deadCount > 0}
                        <button type="button" class="btn secondary" onclick={confirmRetryAll}>
                            <i class="ri-restart-line" aria-hidden="true"></i>
                            <span class="txt">Retry all dead</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if !can("store.operate")}
            <!-- The sidebar already hides the link; this is the direct URL.
                 Saying so before the request is better than a fully drawn screen
                 whose every control 403s, which reads as a broken panel. -->
            <NoAccess right="store.operate" what="the event outbox" />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Event</th>
                            <!-- col-field-type-text caps at 300px, which is what
                                 lets a handler's error string ellipsize instead
                                 of pushing every column after it off the table. -->
                            <th class="col-field-type-text">Failure</th>
                            <!-- min-width, unlike the carts listing: col-field-type-select carries a
                                 180px floor, and with five other columns beside it that floor
                                 was eating the width the relative date needed. -->
                            <th class="col-field-type-select min-width">State</th>
                            <th class="col-field-type-number min-width">Attempts</th>
                            <th class="col-field-type-date min-width">Written</th>
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
                                <td class="col-field-name-id" data-name="Event">
                                    <div class="row-name">
                                        <span class="txt-code txt-bold txt-ellipsis">
                                            {row.name}
                                        </span>
                                        <span class="txt-hint txt-sm row-handle">
                                            {row.aggregate_type} #{row.aggregate_id}
                                        </span>
                                    </div>
                                </td>
                                <!-- The failure, in the list, because it is what
                                     an operator scans the list for. The full
                                     string is on the row and in the drawer; a
                                     stack trace in a table cell would push every
                                     column after it off the screen. -->
                                <td
                                    class="col-field-type-text"
                                    data-name="Failure"
                                    title={row.last_error}
                                >
                                    {#if row.last_error}
                                        <span class="txt-hint txt-sm txt-ellipsis">
                                            {row.last_error}
                                        </span>
                                    {:else}
                                        <span class="txt-hint">—</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-select min-width" data-name="State">
                                    <span class="label {eventStateClass(row)}">{row.state}</span>
                                </td>
                                <td class="col-field-type-number min-width" data-name="Attempts">
                                    {#if row.attempts > 0}
                                        {row.attempts}
                                        <span class="txt-hint txt-sm">of {MAX_ATTEMPTS}</span>
                                    {:else}
                                        <span class="txt-hint">—</span>
                                    {/if}
                                </td>
                                <td
                                    class="col-field-type-date min-width txt-hint"
                                    data-name="Written"
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
                                <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr>
                                <td colspan="6" class="txt-center txt-hint p-base">
                                    {emptyMessage}
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager
                    {meta}
                    {loading}
                    noun="event"
                    {perPage}
                    onpage={(n) => list.setPage(n)}
                    onperpage={(n) => list.set({ limit: n })}
                />
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<Drawer open={detailOpen} size="lg" title="Event" onclose={() => (detailOpen = false)}>
    {#if event}
        <!-- The order drawer's own card recipe, reused rather than re-cut: a
             second set of selectors saying the same thing is how the two drift
             apart. -->
        <div class="order-detail">
            <div class="order-status">
                <span class="label {eventStateClass(event)}">{event.state}</span>
                <span class="txt-code txt-bold">{event.name}</span>
                <div class="flex-fill"></div>
                <span class="txt-hint txt-sm">#{event.seq}</span>
            </div>

            {#if event.last_error}
                <!-- Never cleared by a retry: the diagnosis outlives the repair,
                     a success clears it and the next failure overwrites it, so
                     nothing stale can survive a delivery. -->
                <section class="order-card">
                    <h6 class="order-card-title">Why it failed</h6>
                    <p class="field-help txt-danger">{event.last_error}</p>
                </section>
            {/if}

            <section class="order-card">
                <h6 class="order-card-title">Delivery</h6>
                <table class="table">
                    <tbody>
                        <tr>
                            <td class="txt-hint">Event id</td>
                            <td class="txt-right txt-code">{event.id}</td>
                        </tr>
                        <tr>
                            <td class="txt-hint">About</td>
                            <td class="txt-right">
                                {#if event.aggregate_type === "order"}
                                    <a href="{base}/orders?q={event.aggregate_id}">
                                        order #{event.aggregate_id}
                                    </a>
                                {:else}
                                    {event.aggregate_type} #{event.aggregate_id}
                                {/if}
                            </td>
                        </tr>
                        <tr>
                            <td class="txt-hint">Attempts</td>
                            <td class="txt-right">
                                {event.attempts} of {MAX_ATTEMPTS}
                            </td>
                        </tr>
                        <tr>
                            <td class="txt-hint">Written</td>
                            <td class="txt-right">{formatDate(event.created_at)}</td>
                        </tr>
                        <!-- Only while there is one to have. `available_at` is
                             still set on a delivered row — it is the visibility
                             window the last claim wrote — and rendering it under
                             a delivery that already happened invites the reader
                             to expect another one. -->
                        {#if event.state !== "published"}
                            <tr>
                                <td class="txt-hint">Next attempt</td>
                                <td class="txt-right">
                                    {event.state === "dead"
                                        ? "— parked"
                                        : formatDate(event.available_at)}
                                </td>
                            </tr>
                        {/if}
                        <tr>
                            <td class="txt-hint">Delivered</td>
                            <td class="txt-right">
                                {event.published_at ? formatDate(event.published_at) : "—"}
                            </td>
                        </tr>
                    </tbody>
                </table>
            </section>

            <section class="order-card">
                <!-- The event as the handlers were given it, which is the whole
                     reason the detail route exists: the list deliberately omits
                     it, because a page of order payloads is megabytes. -->
                <h6 class="order-card-title">Payload</h6>
                {#if detailLoading && !event.payload}
                    <span class="skeleton-loader"></span>
                {:else if event.payload}
                    <pre><code>{JSON.stringify(event.payload, null, 2)}</code></pre>
                {:else}
                    <p class="txt-hint">No payload.</p>
                {/if}
            </section>
        </div>
    {/if}

    {#snippet footer()}
        <button
            type="button"
            class="btn transparent m-r-auto"
            onclick={() => (detailOpen = false)}
        >
            <span class="txt">Close</span>
        </button>
        {#if event?.state === "dead"}
            <!-- No confirmation: un-parking a dead letter is the safe act. The
                 row is going nowhere otherwise — the dispatcher's claim filters
                 it out, so nothing else in the engine will ever touch it. -->
            <button type="button" class="btn" class:loading={retrying} disabled={retrying} onclick={retry}>
                <i class="ri-restart-line" aria-hidden="true"></i>
                <span class="txt">Retry delivery</span>
            </button>
        {:else if event?.state === "pending"}
            <!-- Behind a confirmation, unlike the one above, because this one is
                 easy to press twice and each press can cause a duplicate
                 dispatch of a row a worker is mid-delivery on. -->
            <button
                type="button"
                class="btn secondary"
                class:loading={retrying}
                disabled={retrying}
                onclick={confirmExpedite}
            >
                <i class="ri-send-plane-line" aria-hidden="true"></i>
                <span class="txt">Deliver now</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title={confirmConfig.title}
    message={confirmConfig.message}
    confirmLabel={confirmConfig.confirmLabel}
    onconfirm={() => confirmConfig.run?.()}
/>
