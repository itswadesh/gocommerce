<script>
    /**
     * The files a buyer downloads once their order is paid (D254, ext/digital).
     *
     * A file goes up the moment it is chosen — there is nothing to "save" about
     * bytes — and Replace and Remove act at once too, each on every buyer who
     * already has it, which the buttons say. The download limit and expiry are
     * ordinary fields and wait for the page's one Save button, like the upsell
     * cards: the page reads `dirty`, calls `save()`, and Discard calls `reset()`.
     */
    import { flip } from "svelte/animate";
    import { ApiError, apiErrorFrom, api, request } from "$lib/api.js";
    import { getToken } from "$lib/session.svelte.js";
    import { storeHeaders } from "$lib/store-scope.js";
    import { downloadBinary } from "$lib/download.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise, flipDuration } from "$lib/motion.js";

    let {
        productId,
        variants = [],
        physical = $bindable(true),
        tracked = $bindable(false),
        disabled = false,
        dirty = $bindable(false),
    } = $props();

    let files = $state([]);
    let limit = $state("");
    let days = $state("");
    let saved = $state('["",""]');
    let loaded = $state(false);
    let uploading = $state(0);
    let target = $state("");
    let confirming = $state(0);
    let busy = $state(0);
    let picker = $state(null);
    let replacer = $state(null);
    let replacing = 0;

    $effect(() => {
        dirty = loaded && JSON.stringify([String(limit), String(days)]) !== saved;
    });

    $effect(() => {
        if (productId) load(productId);
    });

    async function load(id) {
        loaded = false;
        try {
            const out = await api.get(`/api/admin/x/digital/products/${id}/files`);
            files = out?.files ?? [];
            take(out?.rule);
            loaded = true;
        } catch (err) {
            toast.error(err);
        }
    }

    function take(rule) {
        limit = rule?.download_limit ?? "";
        days = rule?.expiry_days ?? "";
        saved = JSON.stringify([String(limit), String(days)]);
    }

    function whole(v, max) {
        const s = String(v ?? "").trim();
        if (s === "") return null;
        const n = Number(s);
        return Number.isInteger(n) && n >= 1 && n <= max ? n : NaN;
    }

    /** The first field that cannot be saved, as a sentence, or "". */
    export function invalid() {
        if (Number.isNaN(whole(limit, 1000))) return "Download limit is a whole number from 1 to 1000, or empty.";
        if (Number.isNaN(whole(days, 3650))) return "Days to download is a whole number from 1 to 3650, or empty.";
        return "";
    }

    export async function save() {
        if (!dirty) return;
        const rule = await request("PUT", `/api/admin/x/digital/products/${productId}/rule`, {
            body: { download_limit: whole(limit, 1000), expiry_days: whole(days, 3650) },
        });
        take(rule);
    }

    export function reset() {
        [limit, days] = JSON.parse(saved);
    }

    /**
     * send posts one file as multipart. Not request(): it JSON-encodes any body
     * that is not a string, and a multipart body must keep its boundary — the
     * same reason the media library has its own fetch.
     */
    async function send(method, path, file, extra = {}) {
        const body = new FormData();
        for (const [k, v] of Object.entries(extra)) body.append(k, v);
        body.append("file", file);
        const token = getToken();
        let response;
        try {
            response = await fetch(path, {
                method,
                body,
                headers: { ...storeHeaders(), ...(token ? { Authorization: "Bearer " + token } : {}) },
            });
        } catch {
            throw new ApiError(0, "network_error", "Could not reach the store. Is it still running?");
        }
        if (!response.ok) throw await apiErrorFrom(response);
        return (await response.json())?.data;
    }

    async function add(event) {
        const chosen = [...(event.currentTarget.files ?? [])];
        event.currentTarget.value = "";
        for (const file of chosen) {
            uploading++;
            try {
                const made = await send("POST", `/api/admin/x/digital/products/${productId}/files`, file,
                    target ? { variant_id: target } : {});
                files = [...files, made];
                toast.success(`${made.filename} is ready for buyers.`);
            } catch (err) {
                toast.error(err);
            } finally {
                uploading--;
            }
        }
    }

    function pickReplacement(id) {
        replacing = id;
        replacer?.click();
    }

    async function replace(event) {
        const file = event.currentTarget.files?.[0];
        event.currentTarget.value = "";
        if (!file || !replacing) return;
        const id = replacing;
        busy = id;
        try {
            const updated = await send("PUT", `/api/admin/x/digital/files/${id}/content`, file);
            files = files.map((f) => (f.id === id ? updated : f));
            toast.success("Replaced. Everyone who bought it now gets the new file.");
        } catch (err) {
            toast.error(err);
        } finally {
            busy = 0;
        }
    }

    async function remove(id) {
        if (confirming !== id) {
            confirming = id;
            return;
        }
        confirming = 0;
        busy = id;
        try {
            await request("DELETE", `/api/admin/x/digital/files/${id}`);
            files = files.filter((f) => f.id !== id);
            toast.success("Removed from the product and from every buyer.");
        } catch (err) {
            toast.error(err);
        } finally {
            busy = 0;
        }
    }

    function size(bytes) {
        if (bytes >= 1 << 20) return (bytes / (1 << 20)).toFixed(1) + " MB";
        if (bytes >= 1 << 10) return Math.round(bytes / (1 << 10)) + " KB";
        return bytes + " bytes";
    }

    function forWhom(f) {
        if (!f.variant_id) return "Every variant";
        const v = variants.find((v) => v.id === f.variant_id);
        return v ? v.label || v.sku || "One variant" : "A removed variant";
    }
</script>

<section class="card digital-card">
    <h6 class="section-title">
        <i class="ri-download-cloud-2-line" aria-hidden="true"></i>
        Files buyers download
    </h6>
    <p class="txt-hint txt-sm m-b-sm">
        Each buyer can download these once their order is paid: from their order page, their
        account, and the email that says it is ready. The files are kept private; nobody can open
        one without a link made for their own order.
    </p>

    {#if files.length && (physical || tracked)}
        <div class="alert warning m-b-sm digital-physical" transition:rise>
            <span>
                {physical
                    ? "This product is still marked as posted, so checkout will ask for a delivery address."
                    : "This product still counts stock, so it sells out when the count reaches zero."}
            </span>
            <button
                type="button"
                class="btn sm secondary"
                {disabled}
                onclick={() => {
                    physical = false;
                    tracked = false;
                }}
            >
                It is a download only
            </button>
        </div>
    {/if}

    {#if files.length}
        <ul class="digital-files">
            {#each files as f (f.id)}
                <li class="digital-file" class:busy={busy === f.id} animate:flip={{ duration: flipDuration() }} transition:rise>
                    <i class="ri-file-text-line txt-hint" aria-hidden="true"></i>
                    <div class="digital-name">
                        <span class="txt-bold txt-ellipsis">{f.filename}</span>
                        <span class="txt-hint txt-sm">{size(f.size_bytes)} · {forWhom(f)}</span>
                    </div>
                    <div class="inline-flex gap-5">
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            aria-label="Download {f.filename}"
                            onclick={() => downloadBinary(`/api/admin/x/digital/files/${f.id}/content`, f.filename)}
                        >
                            <i class="ri-download-line" aria-hidden="true"></i>
                        </button>
                        <button
                            type="button"
                            class="btn sm transparent secondary"
                            disabled={disabled || busy === f.id}
                            onclick={() => pickReplacement(f.id)}
                        >
                            Replace
                        </button>
                        <button
                            type="button"
                            class="btn sm transparent"
                            class:danger={confirming === f.id}
                            class:secondary={confirming !== f.id}
                            disabled={disabled || busy === f.id}
                            onclick={() => remove(f.id)}
                            onblur={() => confirming === f.id && (confirming = 0)}
                        >
                            {confirming === f.id ? "Remove for every buyer?" : "Remove"}
                        </button>
                    </div>
                </li>
            {/each}
        </ul>
    {:else if loaded}
        <p class="txt-hint txt-sm m-b-sm">No files yet.</p>
    {/if}

    <div class="digital-add">
        {#if variants.length > 1}
            <div class="field">
                <label for="digital-target">For</label>
                <select id="digital-target" bind:value={target} {disabled}>
                    <option value="">Every variant</option>
                    {#each variants as v (v.id)}
                        <option value={String(v.id)}>{v.label || v.sku}</option>
                    {/each}
                </select>
            </div>
        {/if}
        <button
            type="button"
            class="btn secondary"
            class:btn-loading={uploading > 0}
            disabled={disabled || uploading > 0 || !loaded}
            onclick={() => picker?.click()}
        >
            <i class="ri-upload-2-line" aria-hidden="true"></i>
            <span>{uploading > 0 ? "Uploading…" : "Upload files"}</span>
        </button>
        <span class="txt-hint txt-sm">Up to 100 MB each.</span>
        <input bind:this={picker} type="file" multiple hidden onchange={add} />
        <input bind:this={replacer} type="file" hidden onchange={replace} />
    </div>

    <div class="grid digital-rule m-t-sm">
        <div class="col-sm-6 field">
            <label for="digital-limit">Downloads per file</label>
            <input
                id="digital-limit"
                type="number"
                min="1"
                max="1000"
                inputmode="numeric"
                placeholder="No limit"
                {disabled}
                bind:value={limit}
            />
        </div>
        <div class="col-sm-6 field">
            <label for="digital-days">Days to download after payment</label>
            <input
                id="digital-days"
                type="number"
                min="1"
                max="3650"
                inputmode="numeric"
                placeholder="No end"
                {disabled}
                bind:value={days}
            />
        </div>
    </div>
    <div class="field-help">For orders paid from now on. Empty means no limit.</div>
</section>

<style>
    .digital-files {
        list-style: none;
        margin: 0 0 var(--smSpacing);
        padding: 0;
        display: flex;
        flex-direction: column;
        gap: 6px;
    }
    .digital-file {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 8px 10px;
        border: 1px solid var(--baseAlt2Color);
        border-radius: var(--baseRadius);
        transition: opacity 0.2s, transform 0.2s;
    }
    .digital-file.busy {
        opacity: 0.55;
    }
    .digital-name {
        display: flex;
        flex-direction: column;
        min-width: 0;
        flex: 1;
    }
    .digital-add {
        display: flex;
        flex-wrap: wrap;
        align-items: flex-end;
        gap: 10px;
    }
    .digital-physical {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
    }
    @media (max-width: 480px) {
        .digital-file {
            flex-wrap: wrap;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .digital-file {
            transition: none;
        }
    }
</style>
