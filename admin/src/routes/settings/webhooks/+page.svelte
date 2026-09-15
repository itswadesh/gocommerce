<script>
    /**
     * Webhooks: where this store sends its events, and whether they arrived.
     *
     * It sits beside Events on purpose. That screen answers "did the engine
     * record it and hand it on"; this one answers "did anybody outside get
     * told". An operator arriving in this corner of the panel usually has one
     * question and does not yet know which half of it is broken.
     *
     * Two things here are unlike the rest of the panel.
     *
     * The secret is readable exactly twice in an endpoint's life — when it is
     * created and when it is rotated — because the store signs with it and
     * therefore holds it recoverably, while no read returns it. So those two
     * responses are shown in a panel the operator has to dismiss, rather than in
     * a toast that disappears on its own. A secret scrolled past is a secret
     * gone, and the only remedy is rotating it again.
     *
     * And the delivery list opens on what is failing rather than on everything.
     * The log's whole reason to exist is the question "what is not getting
     * through", and a first page of successes answers a question nobody walked
     * in with. The filter renders in its active state rather than being applied
     * silently, because a view that quietly drops rows is worse than a noisy one.
     */
    import { can, webhooks } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    /* Display only, and a copy of the module's own maxAttempts. The API reports
       what a row has spent, not the budget it was given — "3" alone says
       nothing about how close to dead it is. */
    const MAX_ATTEMPTS = 12;
    const PER_PAGE = 25;

    /* The delivery filter and page live in the URL, so a link to a failing
       endpoint's log reopens on the same rows. */
    const list = listState({ state: "pending", endpoint_id: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    let endpoints = $state([]);
    let deliveries = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let missing = $state(false);

    /* The one-time secret. Held until dismissed, never re-fetchable. */
    let revealed = $state(null);

    let addOpen = $state(false);
    let form = $state({ url: "", events: "order.*" });
    let saving = $state(false);
    let confirmDelete = $state(null);
    let confirmOpen = $state(false);

    const writable = $derived(can("store.operate"));

    async function load() {
        loading = true;
        try {
            const [eps, ds] = await Promise.all([
                webhooks.list({ limit: 100 }),
                webhooks.deliveries({
                    state: list.params.state === "any" ? "" : list.params.state,
                    endpoint_id: list.params.endpoint_id,
                    page: list.params.page,
                    limit: perPage,
                }),
            ]);
            endpoints = eps.data ?? [];
            deliveries = ds.data ?? [];
            meta = ds.meta ?? null;
            missing = false;
        } catch (err) {
            if (err.status === 404) {
                missing = true;
            } else {
                toast.error(err.message);
            }
        } finally {
            loading = false;
        }
    }

    $effect(() => {
        if (!writable || !modulesKnown()) return;
        if (!hasModule("webhooks")) {
            missing = true;
            loading = false;
            return;
        }
        /* Touch the keys this reload depends on so the effect re-runs when the
           URL changes. */
        void list.params.state;
        void list.params.endpoint_id;
        void list.params.page;
        void perPage;
        load();
    });

    async function add() {
        const events = form.events
            .split(/[\s,]+/)
            .map((e) => e.trim())
            .filter(Boolean);
        if (!form.url.trim()) return toast.error("An endpoint needs a URL.");
        if (!events.length) return toast.error("An endpoint needs at least one event.");

        saving = true;
        try {
            const created = await webhooks.create({ url: form.url.trim(), events });
            revealed = { endpoint: created, why: "created" };
            addOpen = false;
            form = { url: "", events: "order.*" };
            await load();
        } catch (err) {
            toast.error(err.message);
        } finally {
            saving = false;
        }
    }

    async function toggleActive(e) {
        try {
            await webhooks.update(e.id, { active: !e.active });
            toast.success(e.active ? "Endpoint paused" : "Endpoint resumed");
            await load();
        } catch (err) {
            toast.error(err.message);
        }
    }

    async function rotate(e) {
        try {
            const rotated = await webhooks.rotate(e.id);
            revealed = { endpoint: rotated, why: "rotated" };
            await load();
        } catch (err) {
            toast.error(err.message);
        }
    }

    async function remove(e) {
        try {
            await webhooks.remove(e.id);
            toast.success("Endpoint removed");
            confirmDelete = null;
            confirmOpen = false;
            await load();
        } catch (err) {
            toast.error(err.message);
        }
    }

    async function retry(d) {
        try {
            await webhooks.retry(d.id);
            toast.success("Queued again");
            await load();
        } catch (err) {
            toast.error(err.message);
        }
    }

    function closeMenu(id) {
        document.getElementById(`endpoint-more-${id}`)?.hidePopover();
    }

    function stateClass(state) {
        if (state === "delivered") return "success";
        if (state === "dead") return "danger";
        return "warning";
    }

    const endpointOptions = $derived([
        { value: "", label: "Any endpoint" },
        ...endpoints.map((e) => ({ value: String(e.id), label: e.url })),
    ]);
</script>

<svelte:head><title>Webhooks · GoCommerce</title></svelte:head>

{#if !can("store.operate")}
    <NoAccess right="store.operate" what="webhooks" />
{:else}
    <div class="page page-webhooks shopify-skin">

        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Webhooks</div>
                </nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                {#if !missing}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn" onclick={() => (addOpen = true)}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">Add endpoint</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="webhooks"
                    what="Outgoing webhooks are served by ext/webhooks, and this binary does not have it."
                />
            {:else}
                {#if revealed}
                    <!-- Not a toast. This is the only time this string exists
                         outside the database, and a notification that fades
                         takes it with it. -->
                    <div class="alert warning m-b-base">
                        <p>
                            <strong>Copy this signing secret now.</strong>
                            It is shown once, when an endpoint is {revealed.why}, and no later
                            read returns it. If it is lost, rotate the endpoint to issue a new
                            one.
                        </p>
                        <p><code>{revealed.endpoint.secret}</code></p>
                        <p class="txt-hint txt-sm">
                            Verify a delivery with HMAC-SHA256 over
                            <code>&lt;timestamp&gt;.&lt;body&gt;</code>, reading both from the
                            <code>X-GoCommerce-Signature</code> header.
                        </p>
                        <button type="button" class="btn sm" onclick={() => (revealed = null)}>
                            I have copied it
                        </button>
                    </div>
                {/if}

                <div class="wrapper m-b-base">
                    <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Endpoints</h2>

                    {#if loading && !endpoints.length}
                        <div class="block txt-center p-base"><span class="loader"></span></div>
                    {:else if !endpoints.length}
                        <div class="block txt-center p-base txt-hint">
                            No endpoint yet. Add one and every event it names will be POSTed to it,
                            signed, with retries.
                        </div>
                    {:else}
                        <div class="table-scroll">
                            <table class="table">
                                <thead>
                                    <tr>
                                        <th>URL</th>
                                        <th>Events</th>
                                        <th>Secret</th>
                                        <th>State</th>
                                        <th class="min-width"></th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {#each endpoints as e (e.id)}
                                        <tr>
                                            <td><span class="txt-ellipsis" title={e.url}>{e.url}</span></td>
                                            <td>
                                                {#each e.events as ev (ev)}
                                                    <span class="label m-r-5">{ev}</span>
                                                {/each}
                                            </td>
                                            <td class="txt-hint"><code>…{e.secret_hint}</code></td>
                                            <td>
                                                <span class="label {e.active ? 'success' : ''}">
                                                    {e.active ? "active" : "paused"}
                                                </span>
                                            </td>
                                            <td class="min-width txt-right">
                                                {#if writable}
                                                    <!-- Pause stays in the row because it is the
                                                         one an operator reaches for mid-incident.
                                                         The other two went behind the menu when
                                                         three buttons pushed Remove off the edge
                                                         of the table. -->
                                                    <button
                                                        type="button"
                                                        class="btn sm secondary"
                                                        onclick={() => toggleActive(e)}
                                                    >
                                                        {e.active ? "Pause" : "Resume"}
                                                    </button>
                                                    <button
                                                        type="button"
                                                        class="btn sm secondary"
                                                        title="More"
                                                        aria-label="More"
                                                        popovertarget="endpoint-more-{e.id}"
                                                        aria-haspopup="menu"
                                                    >
                                                        <i class="ri-more-2-line" aria-hidden="true"></i>
                                                    </button>
                                                    <div
                                                        id="endpoint-more-{e.id}"
                                                        class="dropdown dropdown-sm"
                                                        popover="auto"
                                                        role="menu"
                                                    >
                                                        <button
                                                            type="button"
                                                            role="menuitem"
                                                            class="dropdown-item"
                                                            onclick={() => { closeMenu(e.id); rotate(e); }}
                                                        >
                                                            <i class="ri-key-2-line" aria-hidden="true"></i>
                                                            <span class="txt">Rotate secret</span>
                                                        </button>
                                                        <button
                                                            type="button"
                                                            role="menuitem"
                                                            class="dropdown-item txt-danger"
                                                            onclick={() => {
                                                                closeMenu(e.id);
                                                                confirmDelete = e;
                                                                confirmOpen = true;
                                                            }}
                                                        >
                                                            <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                                            <span class="txt">Remove</span>
                                                        </button>
                                                    </div>
                                                {/if}
                                            </td>
                                        </tr>
                                    {/each}
                                </tbody>
                            </table>
                        </div>
                    {/if}
                </div>

                <div class="wrapper">
                    <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Deliveries</h2>

                    <div class="fields m-b-sm">
                        <div class="field">
                            <Select
                                id="delivery-state"
                                ariaLabel="Delivery state"
                                value={list.params.state}
                                onchange={(v) => list.set({ state: v, page: 1 })}
                                options={[
                                    { value: "pending", label: "Still trying" },
                                    { value: "dead", label: "Gave up" },
                                    { value: "delivered", label: "Delivered" },
                                    { value: "any", label: "Any state" },
                                ]}
                            />
                        </div>
                        <div class="field">
                            <Select
                                id="delivery-endpoint"
                                ariaLabel="Endpoint"
                                value={list.params.endpoint_id}
                                onchange={(v) => list.set({ endpoint_id: v, page: 1 })}
                                options={endpointOptions}
                            />
                        </div>
                    </div>

                    {#if loading && !deliveries.length}
                        <div class="block txt-center p-base"><span class="loader"></span></div>
                    {:else if !deliveries.length}
                        <div class="block txt-center p-base txt-hint">
                            Nothing here. With the filter on “Still trying”, that is the good answer.
                        </div>
                    {:else}
                        <div class="table-scroll">
                            <table class="table">
                                <thead>
                                    <tr>
                                        <th>Event</th>
                                        <th>State</th>
                                        <th>Attempts</th>
                                        <th>Last answer</th>
                                        <th>Created</th>
                                        <th class="min-width"></th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {#each deliveries as d (d.id)}
                                        <tr>
                                            <td>
                                                <span class="txt-code">{d.event_name}</span>
                                            </td>
                                            <td>
                                                <span class="label {stateClass(d.state)}">{d.state}</span>
                                            </td>
                                            <td class="min-width">
                                                {d.attempts}
                                                <span class="txt-hint txt-sm">of {MAX_ATTEMPTS}</span>
                                            </td>
                                            <td class="txt-hint">
                                                {#if d.last_status}
                                                    <span class="txt-code">{d.last_status}</span>
                                                {/if}
                                                {d.last_error ?? ""}
                                            </td>
                                            <td class="txt-hint" title={formatDate(d.created_at)}>
                                                {relativeTime(d.created_at)}
                                            </td>
                                            <td class="min-width txt-right">
                                                {#if writable && d.state !== "delivered"}
                                                    <button
                                                        type="button"
                                                        class="btn sm secondary"
                                                        title={d.state === "dead"
                                                            ? "Give it a fresh set of attempts"
                                                            : "Send it now rather than waiting out the backoff"}
                                                        onclick={() => retry(d)}
                                                    >
                                                        Retry
                                                    </button>
                                                {/if}
                                            </td>
                                        </tr>
                                    {/each}
                                </tbody>
                            </table>
                        </div>
                    {/if}
                </div>

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <Pager
                        {meta}
                        {loading}
                        noun="delivery"
                        plural="deliveries"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <Drawer open={addOpen} title="Add endpoint" onclose={() => (addOpen = false)}>
        <form
            class="block"
            onsubmit={(e) => {
                e.preventDefault();
                add();
            }}
        >
            <div class="field">
                <label for="endpoint-url">URL</label>
                <input
                    id="endpoint-url"
                    type="url"
                    placeholder="https://merchant.example.com/hooks"
                    bind:value={form.url}
                    required
                />
            </div>
            <div class="field">
                <label for="endpoint-events">Events</label>
                <input
                    id="endpoint-events"
                    type="text"
                    placeholder="order.*, cart.abandoned"
                    bind:value={form.events}
                />
                <div class="txt-hint txt-sm m-t-5">
                    An exact name (<code>order.paid</code>), a prefix (<code>order.*</code>) or
                    <code>*</code> for everything. Separate several with commas.
                </div>
            </div>
            <button type="submit" class="btn" disabled={saving} class:loading={saving}>
                Add endpoint
            </button>
        </form>
    </Drawer>

    <Confirm
        bind:open={confirmOpen}
        title="Remove this endpoint?"
        message="Its delivery log goes with it. Events already queued for it will not be sent."
        confirmLabel="Remove"
        danger
        onconfirm={() => remove(confirmDelete)}
    />
{/if}
