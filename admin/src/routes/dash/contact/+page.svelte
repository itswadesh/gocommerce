<script>
    /**
     * Contact: what shoppers sent through the storefront's form, and the
     * questions they asked about a product, in one inbox.
     *
     * Three states — new, replied, archived — and a filter for the kind. The
     * drawer shows the message whole, the order or product it is about, the
     * thread of replies, and a reply box: the reply goes to the shopper from
     * the store's own email provider, and only once the provider has taken it
     * does it join the thread and mark the message replied. A reply that did
     * not go says why beside the button and keeps its text. The mailto stays
     * for an operator who would rather answer from their own mail.
     */
    import { base } from "$app/paths";
    import { api, can, query, request } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { rightLabel } from "$lib/rights.js";
    import { KIND_OPTIONS, kindParam, mailtoHref, productFacts, replySubject, sentByLabel } from "$lib/contact.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 25;
    const readable = $derived(can("contact.read"));
    const operates = $derived(can("contact.write"));
    const mayReply = $derived(can("contact.reply"));
    /* Trusted only where the store could have been asked: the probe is made
       for an operator holding contact.read or customers.read (modules.svelte.js),
       and for anybody else the rights answer is the true one. */
    const missing = $derived(modulesKnown() && !hasModule("contact") && (readable || can("customers.read")));

    const list = listState({ q: "", status: "new", kind: "all", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);
    let draft = $state(list.params.q);
    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let open = $state(null);
    let notes = $state("");
    let saving = $state(false);
    let removing = $state(null);
    /** The thread is read with the message when the drawer opens. */
    let threadLoading = $state(false);

    let subject = $state("");
    let reply = $state("");
    let replying = $state(false);
    /** Held for a beat after it lands, so the button can say it worked. */
    let sent = $state(false);
    /** The engine's own sentence for a reply that did not go. */
    let refusal = $state("");

    const filtered = $derived(!!list.params.q || list.params.status !== "new" || list.params.kind !== "all");

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
                        kind: kindParam(list.params.kind),
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

    async function show(row) {
        open = row;
        notes = row.notes ?? "";
        subject = replySubject(row);
        reply = "";
        refusal = "";
        sent = false;
        threadLoading = true;
        try {
            const full = await api.get(`/api/admin/x/contact/messages/${row.id}`);
            // The operator may have opened another message meanwhile.
            if (open?.id === row.id) open = full;
        } catch (err) {
            toast.error(err);
        } finally {
            if (open?.id === row.id) threadLoading = false;
        }
    }

    /** replace puts the engine's copy of a message back in the list and the drawer. */
    function replace(updated) {
        rows = rows.map((r) => (r.id === updated.id ? { ...updated, replies: undefined } : r));
        if (open?.id === updated.id) open = updated;
    }

    async function setStatus(row, status) {
        saving = true;
        try {
            const updated = await request("PATCH", `/api/admin/x/contact/messages/${row.id}`, { body: { status, notes } });
            replace(updated);
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
            replace(updated);
            toast.success("Notes saved");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function sendReply(e) {
        e.preventDefault();
        if (!open || replying || !reply.trim()) return;
        const id = open.id;
        replying = true;
        refusal = "";
        try {
            const updated = await request("POST", `/api/admin/x/contact/messages/${id}/reply`, {
                body: { subject: subject.trim(), body: reply },
            });
            replace(updated);
            if (open?.id === id) {
                reply = "";
                subject = replySubject(updated);
                sent = true;
                setTimeout(() => (sent = false), 2000);
            }
            toast.success(`Reply sent to ${updated.email}`);
        } catch (err) {
            // 409: no email provider. 422: the provider refused it or could not
            // be reached. 403: a demo, or a role re-cut since the page loaded.
            // The words are the engine's and belong beside the button pressed.
            if (err.status === 403 || err.status === 409 || err.status === 422) refusal = err.message;
            else toast.error(err);
        } finally {
            replying = false;
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

    function drawerTitle(msg) {
        if (!msg) return "Message";
        if (msg.kind === "enquiry" && msg.product) return "Enquiry about " + msg.product.title;
        return msg.subject || "Message from " + msg.name;
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
                        <input
                            type="text"
                            class="p-l-20"
                            placeholder="Search by name, email, product or order"
                            aria-label="Search messages"
                            bind:value={draft}
                        />
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
                <div class="page-header-primary-btns contact-filters">
                    <div class="field">
                        <Select
                            id="contact-kind"
                            ariaLabel="Kind"
                            value={list.params.kind}
                            options={KIND_OPTIONS}
                            onchange={(v) => list.set({ kind: v })}
                        />
                    </div>
                    <div class="field">
                        <Select
                            id="contact-status"
                            ariaLabel="Status"
                            value={list.params.status}
                            options={[
                                { value: "new", label: "New" },
                                { value: "replied", label: "Replied" },
                                { value: "archived", label: "Archived" },
                                { value: "any", label: "Every status" },
                            ]}
                            onchange={(v) => list.set({ status: v })}
                        />
                    </div>
                </div>
            {/if}
        </header>

        {#if !readable && !missing}
            <NoAccess right="contact.read" what="the contact inbox" />
        {:else if missing}
            <ModuleMissing module="contact" what="The contact inbox is served by ext/contact, and this binary does not have it." />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">From</th>
                            <th class="col-field-type-text">About</th>
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
                                <td class="col-field-type-text" data-name="About" title={row.body}>
                                    <div class="row-name">
                                        {#if row.kind === "enquiry" && row.product}
                                            <span class="contact-about contact-lead">
                                                <span class="label label-info contact-kind">Enquiry</span>
                                                <span class="contact-about-title">{row.product.title}</span>
                                            </span>
                                        {:else}
                                            <span class="txt-ellipsis contact-lead">{row.subject || "(no subject)"}</span>
                                        {/if}
                                        <span class="txt-hint txt-sm txt-ellipsis">{row.body}</span>
                                    </div>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Order">
                                    {#if row.order_number}
                                        <a href="{base}/dash/orders{query({ q: row.order_number })}" class="txt-code" style="white-space: nowrap" onclick={(e) => e.stopPropagation()}>{row.order_number}</a>
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
                            <tr class="empty-row">
                                <td colspan="6">
                                    {#if filtered}
                                        <EmptyState
                                            icon="ri-filter-off-line"
                                            title="No messages match these filters"
                                            text="Try another kind, status or search, or clear them to go back to new messages."
                                            onclear={() => {
                                                draft = "";
                                                list.clear();
                                            }}
                                        />
                                    {:else}
                                        <EmptyState
                                            icon="ri-mail-line"
                                            title="No new messages"
                                            text="Messages from the storefront's contact form and questions shoppers ask about a product land here. The form posts to /x/contact/messages, a product page's enquiry to /x/contact/enquiries."
                                        />
                                    {/if}
                                </td>
                            </tr>
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

<Drawer open={!!open} size="md" title={drawerTitle(open)} onclose={() => (open = null)}>
    {#if open}
        <div class="flex gap-10 m-b-sm">
            <div class="flex-fill contact-from">
                <div class="txt-bold">{open.name}</div>
                <div class="txt-hint txt-sm">
                    <a href="mailto:{open.email}">{open.email}</a>{#if open.phone}{" · " + open.phone}{/if}
                    · {formatDate(open.created_at)}
                </div>
                {#if open.order_number}
                    <div class="txt-hint txt-sm">About order <a href="{base}/dash/orders{query({ q: open.order_number })}" class="txt-code">{open.order_number}</a></div>
                {/if}
            </div>
            <span class="label {statusClass(open.status)}">{STATUS[open.status]}</span>
        </div>

        {#if open.kind === "enquiry" && open.product}
            <a class="contact-product" href="{base}/dash/products/{open.product.id}">
                <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                <span class="contact-product-text">
                    <span class="txt-hint txt-sm">Asked about</span>
                    <span class="txt-bold contact-product-title">{open.product.title}</span>
                    {#if productFacts(open.product).length}
                        <span class="txt-hint txt-sm">{productFacts(open.product).join(" · ")}</span>
                    {/if}
                </span>
                <i class="ri-arrow-right-s-line contact-product-go" aria-hidden="true"></i>
            </a>
        {/if}

        <div class="contact-body">{open.body}</div>

        <section class="contact-thread" aria-label="Replies" aria-busy={threadLoading}>
            {#if open.replies?.length}
                <h6 class="contact-section-title">Replies</h6>
                <ol class="contact-replies">
                    {#each open.replies as r (r.id)}
                        <li class="contact-reply" in:rise>
                            <div class="txt-hint txt-sm">
                                <span class="txt-bold">{sentByLabel(r.sent_by)}</span>
                                · <span title={formatDate(r.created_at)}>{relativeTime(r.created_at)}</span>
                            </div>
                            <div class="txt-sm contact-reply-subject">{r.subject}</div>
                            <div class="contact-reply-body">{r.body}</div>
                        </li>
                    {/each}
                </ol>
            {:else if threadLoading}
                <span class="skeleton-loader"></span>
            {/if}
        </section>

        {#if mayReply}
            <form class="contact-composer" onsubmit={sendReply}>
                <h6 class="contact-section-title">Reply to {open.name}</h6>
                {#if refusal}
                    <div class="alert warning contact-refusal" role="alert" transition:rise>
                        <p>{refusal}</p>
                        <p>Nothing was sent, and your reply is still here.</p>
                    </div>
                {/if}
                <div class="field">
                    <label for="contact-reply-subject">Subject</label>
                    <input id="contact-reply-subject" type="text" maxlength="200" bind:value={subject} />
                </div>
                <div class="field m-t-sm">
                    <label for="contact-reply-body">Message</label>
                    <textarea id="contact-reply-body" rows="5" maxlength="5000" bind:value={reply} placeholder="Write your reply"></textarea>
                </div>
                <p class="txt-hint txt-sm m-t-5">Sent by email from the store's address; their answer comes back to it.</p>
                <div class="contact-composer-actions">
                    <button type="submit" class="btn contact-send" class:loading={replying} disabled={replying || sent || !reply.trim()}>
                        <i class={sent ? "ri-check-line" : "ri-send-plane-line"} aria-hidden="true"></i>
                        <span class="txt">{sent ? "Sent" : replying ? "Sending…" : "Send reply"}</span>
                    </button>
                    <a class="btn transparent secondary" href={mailtoHref(open)}>
                        <i class="ri-mail-open-line" aria-hidden="true"></i>
                        <span class="txt">Open in my mail app</span>
                    </a>
                    <span class="tw:sr-only" aria-live="polite">{sent ? "Reply sent" : ""}</span>
                </div>
            </form>
        {:else}
            <div class="contact-composer">
                <p class="txt-hint txt-sm">Your role does not carry “{rightLabel("contact.reply")}”, so replies are sent from your own mail.</p>
                <a class="btn secondary m-t-5" href={mailtoHref(open)}>
                    <i class="ri-mail-send-line" aria-hidden="true"></i>
                    <span class="txt">Reply by email</span>
                </a>
            </div>
        {/if}

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
        {#if open && operates}
            <button type="button" class="btn transparent danger m-r-auto contact-foot" onclick={() => (removing = open)}>
                <span class="txt">Delete</span>
            </button>
            {#if open.status !== "archived"}
                <button type="button" class="btn secondary contact-foot" disabled={saving} onclick={() => setStatus(open, "archived")}><span class="txt">Archive</span></button>
            {/if}
            {#if open.status !== "replied"}
                <button type="button" class="btn secondary contact-foot" disabled={saving} onclick={() => setStatus(open, "replied")}><span class="txt">Mark replied</span></button>
            {:else}
                <button type="button" class="btn secondary contact-foot" disabled={saving} onclick={() => setStatus(open, "new")}><span class="txt">Mark new</span></button>
            {/if}
        {:else if open}
            <button type="button" class="btn transparent m-l-auto" onclick={() => (open = null)}><span class="txt">Close</span></button>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    open={!!removing}
    title="Delete this message?"
    message="Gone for good, with its replies. Archive it instead to keep the record."
    confirmLabel="Delete"
    danger
    onconfirm={remove}
    oncancel={() => (removing = null)}
/>

<style>
    .contact-filters {
        flex-wrap: wrap;
        gap: 10px;
    }
    /* An enquiry's label sits beside its product, on a phone's card too,
       where the list otherwise stacks every span of a name on its own line. */
    .row-name > .contact-about {
        display: flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
    }
    .contact-about-title {
        min-width: 0;
        overflow: hidden;
        white-space: nowrap;
        text-overflow: ellipsis;
    }
    /* The subject or the product leads and the message's first line takes
       what is left, rather than both being cut to the same few letters. */
    @media (min-width: 601px) {
        .row-name > .contact-lead {
            flex-shrink: 0;
            max-width: 65%;
        }
    }
    @media (max-width: 600px) {
        .row-name > .contact-about {
            align-items: flex-start;
        }
        .contact-about-title {
            white-space: normal;
            overflow-wrap: anywhere;
        }
    }
    /* Three buttons share the drawer's foot; on the narrowest phones they
       need the room PocketBase's padding takes. */
    @media (max-width: 420px) {
        .contact-foot {
            padding-left: 14px;
            padding-right: 14px;
        }
    }
    .contact-kind {
        flex-shrink: 0;
    }
    .contact-from {
        min-width: 0;
        overflow-wrap: anywhere;
    }
    .contact-body,
    .contact-reply-body {
        white-space: pre-wrap;
        overflow-wrap: anywhere;
        line-height: 1.5;
    }
    .contact-body {
        padding: var(--smSpacing);
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt2Color, rgba(0, 0, 0, 0.04));
    }
    .contact-product {
        display: flex;
        align-items: center;
        gap: 10px;
        margin-bottom: var(--smSpacing);
        padding: 10px var(--smSpacing);
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
        color: inherit;
        text-decoration: none;
        transition:
            border-color 150ms ease-out,
            background-color 150ms ease-out;
    }
    .contact-product:hover,
    .contact-product:focus-visible {
        border-color: var(--surfaceAlt5Color);
        background: var(--surfaceAlt2Color, rgba(0, 0, 0, 0.04));
    }
    .contact-product:focus-visible {
        outline: 2px solid var(--ring, var(--primaryColor));
        outline-offset: 2px;
    }
    .contact-product > i:first-child {
        font-size: 1.25rem;
        color: var(--surfaceTxtHintColor);
    }
    .contact-product-text {
        display: flex;
        flex-direction: column;
        min-width: 0;
        flex: 1;
    }
    .contact-product-title {
        overflow-wrap: anywhere;
    }
    .contact-product-go {
        color: var(--surfaceTxtHintColor);
        transition: transform 150ms ease-out;
    }
    .contact-section-title {
        margin: var(--smSpacing) 0 8px;
        font-size: 0.8125rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        color: var(--surfaceTxtHintColor);
    }
    .contact-replies {
        list-style: none;
        margin: 0;
        padding: 0;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .contact-reply {
        padding: 10px var(--smSpacing);
        border-left: 3px solid var(--successColor);
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color, rgba(0, 0, 0, 0.02));
    }
    .contact-reply-subject {
        margin: 2px 0 4px;
        font-weight: 600;
        overflow-wrap: anywhere;
    }
    .contact-composer {
        margin-top: var(--smSpacing);
        padding-top: var(--smSpacing);
        border-top: 1px solid var(--surfaceAlt3Color);
    }
    .contact-composer .alert {
        margin-bottom: 12px;
    }
    .contact-composer-actions {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px;
        margin-top: var(--smSpacing);
    }
    .contact-send {
        min-width: 9.5rem;
    }
    .contact-composer-actions .btn,
    .contact-foot {
        transition:
            color 150ms ease-out,
            background-color 150ms ease-out,
            transform 150ms ease-out;
    }

    @media (prefers-reduced-motion: no-preference) {
        .contact-product:hover .contact-product-go {
            transform: translateX(3px);
        }
        .contact-composer-actions .btn:active:not(:disabled),
        .contact-foot:active:not(:disabled) {
            transform: scale(0.97);
        }
        /* A refusal shakes once where the operator is looking: the answer is
           "no", said beside the button that asked. */
        .contact-refusal {
            animation: contactShake 280ms ease-out;
        }
    }
    @keyframes contactShake {
        20% {
            transform: translateX(-4px);
        }
        45% {
            transform: translateX(3px);
        }
        70% {
            transform: translateX(-2px);
        }
    }
</style>
