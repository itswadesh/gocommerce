<script>
    /**
     * Notifications: what the store told its shoppers, and whether it went.
     *
     * The question that brings an operator here is one question — "did she
     * get her confirmation" — usually with the shopper on the phone. So the
     * list leads with the recipient and the order, says plainly which of
     * three things happened (sent, only logged because nothing delivers on
     * that channel, or refused by the backend with its error), and offers
     * one repair: send it again, through whatever the store has now.
     *
     * It sits in the main nav beside Carts and Orders rather than under
     * Settings with the outbox, because it is part of the conversation with
     * a customer, not a diagnostic of the platform.
     */
    import { base } from "$app/paths";
    import { api, can, query, request } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const PER_PAGE = 25;
    const readable = $derived(can("orders.read"));
    const writable = $derived(can("orders.write"));

    /* The filters and the page live in the URL, so a link to "the failed
       ones" reopens on the failed ones. */
    const list = listState({ q: "", channel: "", status: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);
    let draft = $state(list.params.q);
    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let resending = $state(0);

    $effect(() => {
        list.params;
        if (!readable) return;
        load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/notifications" +
                    query({
                        q: list.params.q,
                        channel: list.params.channel,
                        status: list.params.status,
                        page: list.page,
                        limit: perPage,
                    }),
            );
            rows = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draft.trim() });
    }

    async function resend(row) {
        resending = row.id;
        try {
            const again = await request("POST", `/api/admin/notifications/${row.id}/resend`);
            if (again.status === "failed") {
                toast.warning(`Sent again and refused: ${again.error}`);
            } else if (again.status === "logged") {
                toast.info("Sent again — to the log only; this store has no delivery backend for " + row.channel);
            } else {
                toast.success(`Sent again to ${row.to}`);
            }
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            resending = 0;
        }
    }

    function statusClass(status) {
        if (status === "sent") return "label-success";
        if (status === "failed") return "label-danger";
        return "";
    }
    function statusLabel(status) {
        if (status === "sent") return "Sent";
        if (status === "failed") return "Failed";
        return "Logged only";
    }

    const emptyMessage = $derived.by(() => {
        if (list.params.q || list.params.status || list.params.channel) return "Nothing matches.";
        return "Nothing has been sent yet — the first order's confirmation lands here.";
    });
</script>

<svelte:head><title>Notifications · GoCommerce</title></svelte:head>

<div class="page page-notifications shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Notifications</div>
            </nav>

            {#if readable}
                <form class="fields searchbar" onsubmit={submitSearch}>
                    <div class="field">
                        <input
                            type="text"
                            class="p-l-20"
                            placeholder="Search by email, phone or order number"
                            bind:value={draft}
                        />
                    </div>
                    {#if draft || list.params.q}
                        <div class="field addon p-r-5">
                            {#if draft !== list.params.q}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button
                                type="button"
                                class="btn sm pill secondary transparent"
                                onclick={() => ((draft = ""), list.set({ q: "" }))}
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
                            id="notification-channel"
                            ariaLabel="Channel"
                            value={list.params.channel}
                            options={[
                                { value: "", label: "Email and SMS" },
                                { value: "email", label: "Email" },
                                { value: "sms", label: "SMS" },
                            ]}
                            onchange={(v) => list.set({ channel: v })}
                        />
                    </div>
                    <div class="field">
                        <Select
                            id="notification-status"
                            ariaLabel="Status"
                            value={list.params.status}
                            options={[
                                { value: "", label: "Any outcome" },
                                { value: "sent", label: "Sent" },
                                { value: "failed", label: "Failed" },
                                { value: "logged", label: "Logged only" },
                            ]}
                            onchange={(v) => list.set({ status: v })}
                        />
                    </div>
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="orders.read" what="the notifications a store sent" />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">To</th>
                            <th class="col-field-type-select min-width">Event</th>
                            <th class="col-field-type-select min-width">Order</th>
                            <th class="col-field-type-select min-width">Status</th>
                            <th class="col-field-type-text">Backend / error</th>
                            <th class="col-field-type-date min-width">When</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr>
                                <td class="col-field-name-id" data-name="To">
                                    <div class="row-name">
                                        <span class="txt-bold txt-ellipsis">{row.to}</span>
                                        <span class="txt-hint txt-sm row-handle">
                                            {row.resend_of ? `${row.channel} · resend of #${row.resend_of}` : row.channel}
                                        </span>
                                    </div>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Event">
                                    <span class="txt-code txt-sm">{row.event}</span>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Order">
                                    {#if row.order_number}
                                        <a
                                            href="{base}/orders{query({ q: row.order_number })}"
                                            class="txt-code"
                                            style="white-space: nowrap"
                                        >
                                            {row.order_number}
                                        </a>
                                    {:else}
                                        <span class="txt-hint">—</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-select min-width" data-name="Status">
                                    <span class="label {statusClass(row.status)}">{statusLabel(row.status)}</span>
                                </td>
                                <td class="col-field-type-text" data-name="Backend / error" title={row.error || row.backend}>
                                    {#if row.error}
                                        <span class="txt-danger txt-sm txt-ellipsis">{row.error}</span>
                                    {:else if row.backend}
                                        <span class="txt-hint txt-sm">{row.backend}</span>
                                    {:else}
                                        <span class="txt-hint txt-sm">no delivery backend — the log only</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-date min-width txt-hint" data-name="When" title={formatDate(row.created_at)}>
                                    {relativeTime(row.created_at)}
                                </td>
                                <td class="col-meta min-width">
                                    {#if writable}
                                        <button
                                            type="button"
                                            class="btn sm secondary transparent"
                                            class:loading={resending === row.id}
                                            disabled={resending === row.id}
                                            title="Send again through what the store has now"
                                            onclick={() => resend(row)}
                                        >
                                            <i class="ri-send-plane-line" aria-hidden="true"></i>
                                            <span class="txt">Resend</span>
                                        </button>
                                    {/if}
                                </td>
                            </tr>
                        {/each}

                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="7"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr>
                                <td colspan="7" class="txt-center txt-hint p-base">{emptyMessage}</td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager
                    {meta}
                    {loading}
                    noun="notification"
                    {perPage}
                    onpage={(n) => list.setPage(n)}
                    onperpage={(n) => list.set({ limit: n })}
                />
                <div class="flex-fill"></div>
                <ThemeToggle />
            </footer>
        {/if}
    </div>
</div>
