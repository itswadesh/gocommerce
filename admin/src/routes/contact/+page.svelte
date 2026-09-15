<script>
    /**
     * Contact: the messages shoppers sent through the storefront's form.
     *
     * An inbox with three states — new, replied, archived — and a drawer
     * that shows the message whole, the order it mentions, a place for
     * notes, and a mailto that opens the operator's own mail client with
     * the subject filled in. Replying happens there; this records that it
     * did.
     */
    import { base } from "$app/paths";
    import { api, can, query, request } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
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

    const PER_PAGE = 25;
    const readable = $derived(can("customers.read"));
    const operates = $derived(can("store.operate"));
    const missing = $derived(modulesKnown() && !hasModule("contact"));

    const list = listState({ q: "", status: "new", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);
    let draft = $state(list.params.q);
    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let open = $state(null);
    let notes = $state("");
    let saving = $state(false);
    let removing = $state(null);

    $effect(() => {
        list.params;
        if (readable && hasModule("contact")) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/x/contact/messages" +
                    query({
                        q: list.params.q,
                        status: list.params.status === "any" ? "" : list.params.status,
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

    function show(row) {
        open = row;
        notes = row.notes ?? "";
    }

    async function setStatus(row, status) {
        saving = true;
        try {
            const updated = await request("PATCH", `/api/admin/x/contact/messages/${row.id}`, { body: { status, notes } });
            rows = rows.map((r) => (r.id === updated.id ? updated : r));
            if (open?.id === updated.id) open = updated;
            toast.success(status === "replied" ? "Marked replied" : status === "archived" ? "Archived" : "Marked new");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function saveNotes() {
        if (!open) return;
        saving = true;
        try {
            const updated = await request("PATCH", `/api/admin/x/contact/messages/${open.id}`, { body: { notes } });
            rows = rows.map((r) => (r.id === updated.id ? updated : r));
            open = updated;
            toast.success("Notes saved");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function remove() {
        if (!removing) return;
        try {
            await request("DELETE", `/api/admin/x/contact/messages/${removing.id}`);
            if (open?.id === removing.id) open = null;
            toast.success("Message deleted");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            removing = null;
        }
    }

    function statusClass(status) {
        if (status === "new") return "label-warning";
        if (status === "replied") return "label-success";
        return "";
    }
    const STATUS = { new: "New", replied: "Replied", archived: "Archived" };

    function mailto(row) {
        const subject = encodeURIComponent("Re: " + (row.subject || "your message"));
        return `mailto:${row.email}?subject=${subject}`;
    }
</script>

<svelte:head><title>Contact · GoCommerce</title></svelte:head>

<div class="page page-contact shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Contact</div>
            </nav>

            {#if readable && !missing}
                <form class="fields searchbar" onsubmit={submitSearch}>
                    <div class="field">
                        <input type="text" class="p-l-20" placeholder="Search by name, email, subject or order number" bind:value={draft} />
                    </div>
                    {#if draft || list.params.q}
                        <div class="field addon p-r-5">
                            {#if draft !== list.params.q}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button type="button" class="btn sm pill secondary transparent" onclick={() => ((draft = ""), list.set({ q: "" }))}>Clear</button>
                        </div>
                    {/if}
                </form>
                <div class="page-header-primary-btns">
                    <div class="field">
                        <Select
                            id="contact-status"
                            ariaLabel="Status"
                            value={list.params.status}
                            options={[
                                { value: "new", label: "New" },
                                { value: "replied", label: "Replied" },
                                { value: "archived", label: "Archived" },
                                { value: "any", label: "Everything" },
                            ]}
                            onchange={(v) => list.set({ status: v })}
                        />
                    </div>
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="customers.read" what="the contact inbox" />
        {:else if missing}
            <ModuleMissing module="contact" what="The contact inbox is served by ext/contact, and this binary does not have it." />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">From</th>
                            <th class="col-field-type-text">Subject</th>
                            <th class="col-field-type-select min-width">Order</th>
                            <th class="col-field-type-select min-width">Status</th>
                            <th class="col-field-type-date min-width">Received</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr class="handle" tabindex="0" onclick={() => show(row)} onkeydown={(e) => rowKey(e, () => show(row))}>
                                <td class="col-field-name-id" data-name="From">
                                    <div class="row-name">
                                        <span class="txt-bold txt-ellipsis">{row.name}</span>
                                        <span class="txt-hint txt-sm row-handle">{row.email}</span>
                                    </div>
                                </td>
                                <td class="col-field-type-text" data-name="Subject" title={row.body}>
                                    <div class="row-name">
                                        <span class="txt-ellipsis">{row.subject || "(no subject)"}</span>
                                        <span class="txt-hint txt-sm txt-ellipsis">{row.body}</span>
                                    </div>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Order">
                                    {#if row.order_number}
                                        <a href="{base}/orders{query({ q: row.order_number })}" class="txt-code" style="white-space: nowrap" onclick={(e) => e.stopPropagation()}>{row.order_number}</a>
                                    {:else}
                                        <span class="txt-hint">—</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-select min-width" data-name="Status"><span class="label {statusClass(row.status)}">{STATUS[row.status]}</span></td>
                                <td class="col-field-type-date min-width txt-hint" data-name="Received" title={formatDate(row.created_at)}>{relativeTime(row.created_at)}</td>
                                <td class="col-meta min-width"><i class="ri-arrow-right-s-line" aria-hidden="true"></i></td>
                            </tr>
                        {/each}
                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr><td colspan="6" class="txt-center txt-hint p-base">{list.params.status === "new" ? "Nothing new. The storefront's form posts to /x/contact/messages." : "Nothing here."}</td></tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager {meta} {loading} noun="message" {perPage} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<Drawer open={!!open} size="md" title={open ? open.subject || "Message from " + open.name : "Message"} onclose={() => (open = null)}>
    {#if open}
        <div class="flex gap-10 m-b-sm">
            <div class="flex-fill">
                <div class="txt-bold">{open.name}</div>
                <div class="txt-hint txt-sm">
                    <a href="mailto:{open.email}">{open.email}</a>{#if open.phone} · {open.phone}{/if}
                    · {formatDate(open.created_at)}
                </div>
                {#if open.order_number}
                    <div class="txt-hint txt-sm">About order <a href="{base}/orders{query({ q: open.order_number })}" class="txt-code">{open.order_number}</a></div>
                {/if}
            </div>
            <span class="label {statusClass(open.status)}">{STATUS[open.status]}</span>
        </div>
        <div class="contact-body">{open.body}</div>
        <div class="field m-t-base">
            <label for="contact-notes">Notes</label>
            <textarea id="contact-notes" rows="3" bind:value={notes} disabled={!operates} placeholder="What was done, for the next person who opens this"></textarea>
        </div>
        {#if operates}
            <button type="button" class="btn sm secondary" class:loading={saving} disabled={saving || notes === (open.notes ?? "")} onclick={saveNotes}>
                <span class="txt">Save notes</span>
            </button>
        {/if}
    {/if}
    {#snippet footer()}
        {#if open}
            {#if operates}
                <button type="button" class="btn transparent danger m-r-auto" onclick={() => (removing = open)}>
                    <span class="txt">Delete</span>
                </button>
                {#if open.status !== "archived"}
                    <button type="button" class="btn secondary" disabled={saving} onclick={() => setStatus(open, "archived")}><span class="txt">Archive</span></button>
                {/if}
                {#if open.status !== "replied"}
                    <button type="button" class="btn secondary" disabled={saving} onclick={() => setStatus(open, "replied")}><span class="txt">Mark replied</span></button>
                {:else}
                    <button type="button" class="btn secondary" disabled={saving} onclick={() => setStatus(open, "new")}><span class="txt">Mark new</span></button>
                {/if}
            {/if}
            <a class="btn" href={mailto(open)}>
                <i class="ri-mail-send-line" aria-hidden="true"></i>
                <span class="txt">Reply by email</span>
            </a>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    open={!!removing}
    title="Delete this message?"
    message="Gone for good. Archive it instead to keep the record."
    confirmLabel="Delete"
    danger
    onconfirm={remove}
    oncancel={() => (removing = null)}
/>

<style>
    .contact-body {
        white-space: pre-wrap;
        line-height: 1.5;
        padding: var(--smSpacing);
        border-radius: var(--baseRadius);
        background: var(--surfaceAlt2Color, rgba(0, 0, 0, 0.04));
    }
</style>
