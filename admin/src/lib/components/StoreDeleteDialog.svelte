<script>
    /**
     * Deleting a store, in two steps (D224): first what goes, counted, then a last
     * warning with the store's address name typed back. The engine counts and the
     * engine refuses, so nothing on this screen decides who may delete.
     */
    import { stores } from "$lib/api.js";
    import { dismissable } from "$lib/dismiss.js";
    import { trapFocus } from "$lib/focus.js";
    import { portal } from "$lib/portal.js";
    import { rise } from "$lib/motion.js";

    /** store is the one being deleted; ondelete runs with the typed address name. */
    let { open = $bindable(false), store = null, ondelete } = $props();

    let step = $state(1);
    let counts = $state(null);
    let problem = $state("");
    let typed = $state("");
    let working = $state(false);

    const count = (n) => n.toLocaleString("en-IN");
    const size = (bytes) => {
        if (bytes < 1024 * 1024) return `${Math.ceil(bytes / 1024)} KB`;
        if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
        return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
    };
    const stats = $derived(
        counts
            ? [
                  ["Products", count(counts.products)],
                  ["Orders", count(counts.orders)],
                  ["Customers", count(counts.customers)],
                  ["Collections", count(counts.collections)],
                  ["Categories", count(counts.categories)],
                  ["Discounts", count(counts.discounts)],
                  ["Files", counts.files ? `${count(counts.files)} (${size(counts.file_bytes)})` : "0"],
                  ["Domains", count(counts.domains)],
              ]
            : [],
    );
    const matches = $derived(!!store && typed.trim() === store.slug);

    async function read() {
        counts = null;
        problem = "";
        try {
            counts = await stores.deletePreview(store.id);
        } catch (err) {
            problem = err?.message || "What this store holds could not be counted.";
        }
    }

    $effect(() => {
        if (!open) return;
        step = 1;
        typed = "";
        read();
    });

    async function remove(event) {
        event.preventDefault();
        if (!matches || working) return;
        working = true;
        problem = "";
        try {
            await ondelete?.(typed.trim());
            open = false;
        } catch (err) {
            problem = err?.message || "The store was not deleted.";
        } finally {
            working = false;
        }
    }
</script>

<div
    class="modal popup store-delete-dialog"
    data-modal-state={open ? "open" : "closed"}
    inert={!open}
    role="alertdialog"
    aria-modal="true"
    aria-labelledby="store-delete-title"
    tabindex="-1"
    use:portal
    use:trapFocus={open}
    use:dismissable={{ onclose: () => (open = false), enabled: open && !working }}
>
    {#if step === 1}
        <div class="modal-content" in:rise>
            <h5 id="store-delete-title" class="m-b-sm">Delete {store?.name ?? "this store"}?</h5>
            <p class="m-b-sm">This is deleted for good:</p>
            {#if counts}
                <dl class="stats">
                    {#each stats as [label, value] (label)}
                        <div class="stat">
                            <dt>{label}</dt>
                            <dd>{value}</dd>
                        </div>
                    {/each}
                </dl>
                <p class="m-t-sm">
                    So is everything else in this store: its settings, theme and pages, payment and delivery
                    setup, staff roles, notification history and reports.
                </p>
                <p class="txt-hint m-t-sm">
                    Not deleted: your other stores, and people's accounts.
                    {counts.team === 1 ? "1 person loses" : `${count(counts.team)} people lose`} access to this store.
                </p>
            {:else if problem}
                <p class="problem" role="alert"><i class="ri-error-warning-line" aria-hidden="true"></i> {problem}</p>
            {:else}
                <div class="block txt-center"><span class="loader" aria-label="Counting"></span></div>
            {/if}
        </div>
        <footer class="modal-footer">
            <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}>
                <span class="txt">Cancel</span>
            </button>
            {#if problem && !counts}
                <button type="button" class="btn secondary" onclick={read}><span class="txt">Try again</span></button>
            {:else}
                <button type="button" class="btn danger" disabled={!counts} onclick={() => (step = 2)}>
                    <span class="txt">Continue</span>
                </button>
            {/if}
        </footer>
    {:else}
        <form onsubmit={remove} class="confirm-step" in:rise>
            <div class="modal-content">
                <h5 id="store-delete-title" class="m-b-sm">Are you absolutely sure?</h5>
                <p class="m-b-sm">
                    Nobody can undo this, and nothing can be recovered afterwards. {store?.name ?? "This store"}
                    stops working straight away, for its customers too.
                </p>
                <div class="field">
                    <label for="store-delete-confirm">Type <code>{store?.slug ?? ""}</code> to confirm</label>
                    <input id="store-delete-confirm" type="text" autocomplete="off" spellcheck="false" bind:value={typed} disabled={working} />
                </div>
                {#if problem}
                    <p class="problem m-t-sm" role="alert"><i class="ri-error-warning-line" aria-hidden="true"></i> {problem}</p>
                {/if}
            </div>
            <footer class="modal-footer">
                <button type="button" class="btn transparent m-r-auto" disabled={working} onclick={() => (step = 1)}>
                    <span class="txt">Back</span>
                </button>
                <button type="submit" class="btn danger" class:loading={working} disabled={!matches || working}>
                    <i class="ri-delete-bin-line" aria-hidden="true"></i>
                    <span class="txt">Delete this store for good</span>
                </button>
            </footer>
        </form>
    {/if}
</div>

<style>
    .stats {
        display: grid;
        grid-template-columns: repeat(2, minmax(0, 1fr));
        gap: 8px 16px;
        margin: 0;
    }
    .stat {
        display: flex;
        justify-content: space-between;
        gap: 8px;
        padding-bottom: 4px;
        border-bottom: 1px solid var(--borderColor);
    }
    .stat dd {
        margin: 0;
        font-weight: 600;
        font-variant-numeric: tabular-nums;
        white-space: nowrap;
    }
    .problem {
        margin: 0;
        color: var(--dangerColor);
    }
    .confirm-step {
        display: contents;
    }
    @media (max-width: 480px) {
        .stats {
            grid-template-columns: minmax(0, 1fr);
        }
    }
</style>
