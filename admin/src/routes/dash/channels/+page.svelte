<script>
    /**
     * Storefronts over one catalogue.
     *
     * The thing this screen has to say out loud, because it is the opposite of
     * what the table implies: a product that names no channel sells on every
     * one. The Products column counts what has been *narrowed* to a channel,
     * not what it sells — and an operator reading "0 products" beside a live
     * channel would otherwise conclude the storefront is empty when it is
     * showing the entire catalogue.
     */
    import { api } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { fieldText } from "$lib/fieldtext.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    /* Seeing where the catalogue is sold, and opening or closing one of those
       places, are different jobs. */
    const readable = $derived(can("channels.read"));
    const writable = $derived(can("channels.write"));

    let loading = $state(true);
    let channels = $state([]);
    let open = $state(false);
    let editing = $state(null);
    let form = $state(blank());
    let saving = $state(false);
    let confirmOpen = $state(false);
    let pending = $state(null);

    function blank() {
        return { code: "", name: "", active: true, default: false };
    }

    async function load() {
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const result = await api.get("/api/admin/channels");
            channels = result.data ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }
    $effect(() => {
        load();
    });

    function edit(channel) {
        editing = channel ?? null;
        form = channel
            ? { code: channel.code, name: channel.name, active: channel.active, default: channel.default }
            : blank();
        open = true;
    }

    async function save() {
        const code = fieldText(form.code);
        const name = fieldText(form.name);
        if (!code || !name) {
            toast.error("A channel needs a code and a name.");
            return;
        }
        saving = true;
        try {
            const body = { code, name, active: form.active, default: form.default };
            if (editing) await api.patch(`/api/admin/channels/${editing.id}`, body);
            else await api.post("/api/admin/channels", body);
            open = false;
            await load();
            toast.success(editing ? "Channel saved." : "Channel created.");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function doDelete() {
        if (!pending) return;
        try {
            await api.delete(`/api/admin/channels/${pending.id}`);
            await load();
            toast.success("Channel deleted.");
        } catch (err) {
            toast.error(err);
        } finally {
            pending = null;
        }
    }
</script>

<svelte:head><title>Channels · GoCommerce</title></svelte:head>

<div class="page-content tw:bg-background tw:text-foreground">
    <nav class="breadcrumbs">
        <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Channels</div>
    </nav>

    {#if !readable}
        <NoAccess right="store.operate" />
    {:else}
        <div class="tw:flex tw:items-center tw:justify-between tw:mb-3">
            <div class="field-help tw:m-0 tw:max-w-2xl">
                One catalogue served as several storefronts, each able to differ in what is
                published to it and what that costs. They do not differ in currency — the store
                settles in one, and every order snapshots it.
            </div>
            <button type="button" class="btn sm" disabled={!writable} onclick={() => edit(null)}>
                <i class="ri-add-line" aria-hidden="true"></i>
                <span class="txt">New channel</span>
            </button>
        </div>

        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        <th>Name</th>
                        <th>Code</th>
                        <th class="col-field-type-number min-width">Narrowed to it</th>
                        <th class="min-width">State</th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each channels as channel (channel.id)}
                        <tr>
                            <td data-name="Name">
                                <strong>{channel.name}</strong>
                                {#if channel.default}
                                    <span class="label">default</span>
                                {/if}
                            </td>
                            <td data-name="Code"><code>{channel.code}</code></td>
                            <td class="col-field-type-number min-width" data-name="Narrowed to it">
                                {channel.products}
                            </td>
                            <td class="min-width" data-name="State">
                                <span class="label {channel.active ? 'success' : ''}">
                                    {channel.active ? "selling" : "closed"}
                                </span>
                            </td>
                            <td class="col-meta min-width">
                                <button type="button" class="btn sm secondary transparent" disabled={!writable}
                                        onclick={() => edit(channel)}>
                                    <span class="txt">Edit</span>
                                </button>
                                <button type="button" class="btn sm transparent row-delete" disabled={!writable}
                                        title="Delete" aria-label="Delete {channel.name}"
                                        onclick={() => { pending = channel; confirmOpen = true; }}>
                                    <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                </button>
                            </td>
                        </tr>
                    {/each}
                    {#if !loading && !channels.length}
                        <tr>
                            <td colspan="5" class="txt-center txt-hint p-base">
                                No channels. The whole catalogue sells through one storefront,
                                which is what every store starts as.
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>
        <div class="field-help">
            “Narrowed to it” counts the products pinned to this channel, not the products it
            sells. A product pinned to no channel at all sells on every one — publication is a
            narrowing you opt into, so a new channel starts by showing everything rather than
            nothing.
        </div>
    {/if}
</div>

<Drawer {open} size="sm"
        title={editing ? "Edit channel" : "New channel"}
        onclose={() => (open = false)}>
    <div class="field">
        <label for="ch-name">Name</label>
        <input id="ch-name" type="text" bind:value={form.name} placeholder="Wholesale" />
    </div>
    <div class="field m-t-sm">
        <label for="ch-code">Code</label>
        <input id="ch-code" type="text" bind:value={form.code} placeholder="wholesale" />
    </div>
    <div class="field-help">
        The code is what a storefront names itself as, in <code>?channel=</code> or the
        <code>X-Channel</code> header. A request naming a code this store does not have is
        refused rather than served the default, so a typo fails at the first request.
    </div>

    <label class="form-field form-field-toggle m-t-sm">
        <input type="checkbox" id="ch-active" bind:checked={form.active} />
        <label for="ch-active">Selling</label>
    </label>
    <div class="field-help">
        A closed channel refuses requests rather than answering with an empty catalogue, which
        would read as a broken import.
    </div>

    <label class="form-field form-field-toggle m-t-sm">
        <input type="checkbox" id="ch-default" bind:checked={form.default} />
        <label for="ch-default">The default</label>
    </label>
    <div class="field-help">
        Served to any request that names no channel. Setting it here moves the default rather
        than adding a second one.
    </div>

    {#snippet footer()}
        <button type="button" class="btn secondary" onclick={() => (open = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button type="button" class="btn" disabled={saving || !writable} onclick={save}>
            <span class="txt">{editing ? "Save" : "Create"}</span>
        </button>
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title="Delete this channel?"
    message={pending
        ? `"${pending.name}" will go, and the products narrowed to it go back to selling on every channel. Orders placed through it keep their history.`
        : ""}
    confirmLabel="Delete"
    danger
    onconfirm={doDelete}
/>
