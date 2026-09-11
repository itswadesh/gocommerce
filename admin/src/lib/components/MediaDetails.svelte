<script>
    /**
     * One file, as a drawer: what it is, what it is called, and the two things
     * an operator can actually do to it.
     *
     * Alt text is the only mutable property a media record has, and until now
     * the panel had no field for it — every image shipped with an empty alt and
     * the thumbnail's own `alt` attribute silently fell back to the filename.
     * Deleting was the same story from the other end: `usage=unused` has always
     * been able to find orphans, and nothing could remove one.
     *
     * Two things about the engine shape this.
     *
     * PATCH takes `alt` as a plain string, not a pointer, so a body that omits
     * it CLEARS the value rather than leaving it alone. There is therefore no
     * "patch what changed" path here: the form always sends what is in the box,
     * and the box always starts as what the record holds.
     *
     * DELETE is refused with a 409 while any product still displays the file —
     * `product_media` references media with ON DELETE RESTRICT, and the message
     * names the count. That refusal is the whole safety story, so this does not
     * pre-empt it with a guess: it asks, sends, and shows what came back where
     * the operator is looking rather than only in a toast that scrolls away.
     */
    import { api } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { mediaLabel, kindIcon, fileType, fileSize, dimensions } from "$lib/media.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";

    let {
        open = false,
        item = null,
        /** A product editor shows this drawer for a file the product displays,
         *  where deleting is guaranteed to be refused. Offering the button
         *  there would be offering a 409. */
        allowDelete = true,
        onsaved,
        ondeleted,
        onclose,
    } = $props();

    let alt = $state("");
    let saving = $state(false);
    let deleting = $state(false);
    let refusal = $state("");
    let confirmOpen = $state(false);

    /* The form is re-seeded whenever the drawer is pointed at another file.
       Keyed on the id rather than on `open`, so re-opening the same file after
       a failed save does not throw away what was typed. */
    let seeded = $state(null);
    $effect(() => {
        if (!open || !item || seeded === item.id) return;
        alt = item.alt ?? "";
        refusal = "";
        seeded = item.id;
    });

    const dirty = $derived(!!item && alt !== (item.alt ?? ""));
    const writable = $derived(can("catalog.write"));

    async function save() {
        if (!item || saving) return;
        saving = true;
        try {
            // `alt` always travels, including as "". See the header: this field
            // is not a pointer on the engine's side, so an omitted key is an
            // instruction to clear rather than an instruction to skip.
            const saved = await api.patch(`/api/admin/media/${item.id}`, { alt });
            toast.success("Alt text saved");
            onsaved?.(saved);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function remove() {
        if (!item || deleting) return;
        deleting = true;
        refusal = "";
        try {
            await api.delete(`/api/admin/media/${item.id}`);
            toast.success(`Deleted ${mediaLabel(item)}`);
            ondeleted?.(item);
        } catch (err) {
            // 409 is the engine refusing while products still display it, and
            // its message already names how many. It stays on screen, because
            // the next thing the operator has to do is go and detach it.
            if (err.status === 409) refusal = err.message;
            else toast.error(err);
        } finally {
            deleting = false;
        }
    }

    async function copyURL() {
        try {
            await navigator.clipboard.writeText(item.url);
            toast.success("URL copied");
        } catch {
            // Clipboard access is denied outside a secure context, and there is
            // nothing the operator can do about it from here.
            toast.error("Could not copy the URL. It is " + item.url);
        }
    }
</script>

<Drawer {open} title={item ? item.filename || mediaLabel(item) : "File"} size="sm" {onclose}>
    {#if item}
        <div class="media-detail-preview">
            {#if item.kind === "image"}
                <img src={item.url} alt={mediaLabel(item)} />
            {:else if item.kind === "video"}
                <!-- svelte-ignore a11y_media_has_caption -->
                <video src={item.url} preload="metadata" controls></video>
            {:else}
                <i class={kindIcon(item.kind)} style="font-size: 44px" aria-hidden="true"></i>
            {/if}
        </div>

        <form
            id="media-details-form"
            onsubmit={(e) => (e.preventDefault(), save())}
            class="m-t-base"
        >
            <div class="field">
                <label for="media-alt">Alt text</label>
                <textarea id="media-alt" rows="2" disabled={!writable} bind:value={alt}></textarea>
            </div>
            <div class="field-help">
                What this file shows, for a reader who cannot see it and for a search engine
                that cannot either. Left empty, a storefront has only the filename to fall back
                on.
            </div>
        </form>

        <h6 class="section-title">
            <i class="ri-information-line" aria-hidden="true"></i>
            File
        </h6>

        <table class="table media-detail-facts">
            <tbody>
                <tr>
                    <td class="txt-hint">Type</td>
                    <td class="txt-right">{fileType(item)}</td>
                </tr>
                {#if item.size_bytes}
                    <tr>
                        <td class="txt-hint">Size</td>
                        <td class="txt-right">{fileSize(item.size_bytes)}</td>
                    </tr>
                {/if}
                {#if dimensions(item)}
                    <tr>
                        <td class="txt-hint">Dimensions</td>
                        <td class="txt-right">{dimensions(item)}</td>
                    </tr>
                {/if}
                <tr>
                    <td class="txt-hint">Added</td>
                    <td class="txt-right">{formatDate(item.created_at)}</td>
                </tr>
                <tr>
                    <td class="txt-hint">URL</td>
                    <td class="txt-right">
                        <div class="inline-flex gap-sm media-detail-url">
                            <span class="txt-ellipsis txt-code txt-sm" title={item.url}>
                                {item.url}
                            </span>
                            <button
                                type="button"
                                class="btn circle sm transparent secondary"
                                title="Copy the URL"
                                aria-label="Copy the URL"
                                onclick={copyURL}
                            >
                                <i class="ri-file-copy-line" aria-hidden="true"></i>
                            </button>
                        </div>
                    </td>
                </tr>
            </tbody>
        </table>

        {#if refusal}
            <div class="field-help error m-t-sm">{refusal}</div>
        {/if}

        {#if allowDelete && writable}
            <button
                type="button"
                class="btn sm transparent danger m-t-sm"
                class:loading={deleting}
                disabled={deleting}
                onclick={() => ((refusal = ""), (confirmOpen = true))}
            >
                <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                <span class="txt">Delete file</span>
            </button>
            <div class="field-help">
                Deleting removes the record and, for a file uploaded here, the bytes. A file any
                product still displays is refused until it is detached from them.
            </div>
        {/if}
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Close</span>
        </button>
        <button
            type="submit"
            form="media-details-form"
            class="btn"
            class:loading={saving}
            disabled={saving || !dirty || !writable}
        >
            <span class="txt">Save</span>
        </button>
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title="Delete this file?"
    message={item
        ? `${mediaLabel(item)} will be removed from the library for good, along with the stored file if this store holds it. Anything already pointing at its URL — a page, an export, a storefront cache — will break. If a product still displays it, the store will refuse and say how many.`
        : ""}
    confirmLabel="Delete"
    danger
    onconfirm={remove}
/>

<style>
    /*
     * The preview. `.media-tile` is a 110px square built for a grid; a details
     * drawer is the one place a file is worth looking at properly, so this is
     * its own box — the same surface and radius, sized to the drawer.
     */
    .media-detail-preview {
        display: flex;
        align-items: center;
        justify-content: center;
        aspect-ratio: 16 / 10;
        overflow: hidden;
        color: var(--surfaceTxtHintColor);
        background: var(--surfaceAlt1Color);
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
    }
    .media-detail-preview img,
    .media-detail-preview video {
        max-width: 100%;
        max-height: 100%;
        object-fit: contain;
    }

    /*
     * The label column of the facts table.
     *
     * `.table td` breaks words, which is right for a list of orders and wrong
     * in a drawer this narrow: without this the column collapses to one
     * character per line and "Added" is drawn as five stacked letters.
     */
    .media-detail-facts td:first-child {
        width: 1%;
        white-space: nowrap;
    }

    /* Without a width the flex row measures the URL, and a long one pushes the
       copy button off the drawer. */
    .media-detail-url {
        max-width: 100%;
        align-items: center;
    }
    .media-detail-url > span {
        min-width: 0;
    }
</style>
