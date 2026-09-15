<script>
    /**
     * Notification history: what the store told its shoppers, and whether
     * it went.
     *
     * The question that brings an operator here is one question — "did she
     * get her confirmation" — usually with the shopper on the phone. So the
     * list is laid out the way Litekart's is: who it went to, on which
     * channel, which message, whether it succeeded, and when — plus the
     * order it concerned and one repair, send it again through whatever the
     * store has now.
     *
     * Where the messages come from and what they say is under Setup Email
     * and Setup SMS, the two screens beneath this one in the navigation.
     */
    import { base } from "$app/paths";
    import { api, can, query, request } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatDate } from "$lib/format.js";
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
                toast.info("Sent again — to the log only; nothing delivers " + row.channel + " yet. See Setup " + (row.channel === "sms" ? "SMS" : "Email") + ".");
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

    /* Litekart's vocabulary: a green "success", a red "failed", and for a
       message nothing carried, "logged" — the state that explains why the
       shopper has nothing in their inbox. */
    function outcome(row) {
        if (row.status === "sent") return { label: "success", cls: "label-success", dot: "dot-success" };
        if (row.status === "failed") return { label: "failed", cls: "label-danger", dot: "dot-danger" };
        return { label: "logged", cls: "", dot: "" };
    }

    /* What the message is called: the template's title, or the raw event for
       one nobody registered wording for. */
    function subject(row) {
        return row.title || row.event;
    }

    const emptyMessage = $derived.by(() => {
        if (list.params.q || list.params.status || list.params.channel) return "Nothing matches.";
        return "Nothing has been sent yet — the first order's confirmation lands here.";
    });
</script>

<svelte:head><title>Notification History · GoCommerce</title></svelte:head>

<div class="page page-notifications shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Notification History</div>
            </nav>

            {#if readable}
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
                                { value: "sent", label: "Success" },
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
                            <th class="col-field-name-id">Email</th>
                            <th class="col-field-type-text">Phone</th>
                            <th class="col-field-type-text">Subject</th>
                            <th class="col-field-type-select min-width">Status</th>
                            <th class="col-field-type-date min-width">Created At</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            {@const o = outcome(row)}
                            <tr>
                                <td class="col-field-name-id" data-name="Email">
                                    {#if row.channel === "email"}
                                        <span class="txt-ellipsis" title={row.to}>{row.to}</span>
                                    {:else}
                                        <span class="txt-hint">—</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-text" data-name="Phone">
                                    {#if row.channel === "sms"}
                                        <span class="txt-code">{row.to}</span>
                                    {:else}
                                        <span class="txt-hint">—</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-text" data-name="Subject" title={row.error || row.backend || ""}>
                                    <div class="row-name row-name-stacked">
                                        <span class="txt-ellipsis">{subject(row)}</span>
                                        <span class="txt-hint txt-sm txt-ellipsis">
                                            {#if row.order_number}
                                                <a href="{base}/orders{query({ q: row.order_number })}" class="txt-code">{row.order_number}</a>
                                                ·
                                            {/if}
                                            {#if row.error}
                                                <span class="txt-danger">{row.error}</span>
                                            {:else if row.backend}
                                                via {row.backend}
                                            {:else}
                                                no delivery backend — the log only
                                            {/if}
                                            {#if row.resend_of}
                                                · resend of #{row.resend_of}
                                            {/if}
                                        </span>
                                    </div>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Status">
                                    <span class="label {o.cls} outcome"><span class="outcome-dot {o.dot}" aria-hidden="true"></span>{o.label}</span>
                                </td>
                                <td class="col-field-type-date min-width txt-hint" data-name="Created At">
                                    <span class="txt-nowrap">{formatDate(row.created_at)}</span>
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
                                <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr>
                                <td colspan="6" class="txt-center txt-hint p-base">{emptyMessage}</td>
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

<style>
    .outcome {
        display: inline-flex;
        align-items: center;
        gap: 6px;
    }
    .outcome-dot {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: var(--txtHintColor);
    }
    .dot-success {
        background: var(--successColor);
    }
    .dot-danger {
        background: var(--dangerColor);
    }
    .txt-nowrap {
        white-space: nowrap;
    }
</style>
