<script>
    /**
     * The Meilisearch plugin's sync, inside its settings drawer: when the
     * index last caught up, how much it holds, what went wrong last, and a
     * button to rebuild it now. The worker indexes a store on its own once the
     * plugin is on, so the button is for "right now", not a step to remember.
     */
    import { can, request } from "$lib/api.js";
    import { relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";

    let { enabled = false } = $props();

    let status = $state(null);
    let rebuilding = $state(false);

    async function load() {
        try {
            status = await request("GET", "/api/admin/x/meilisearch/status");
        } catch (err) {
            toast.error(err);
        }
    }

    $effect(() => {
        if (can("catalog.read")) load();
    });

    async function rebuild() {
        rebuilding = true;
        try {
            const res = await request("POST", "/api/admin/x/meilisearch/reindex");
            toast.success(`Index rebuilt: ${res.indexed} products written, ${res.removed} removed`);
        } catch (err) {
            toast.error(err);
        } finally {
            rebuilding = false;
            load();
        }
    }
</script>

{#if status}
    <div class="meili-status m-b-base" aria-live="polite">
        <h6 class="section-title">Index</h6>
        {#if status.configured && !status.host}
            <div class="field-help m-b-sm">
                This install runs Meilisearch for its stores, so switching the plugin on is all it
                needs. Fill the fields below only to use a Meilisearch of the store's own.
            </div>
        {/if}
        {#if !enabled}
            <div class="txt-hint txt-sm">Off. Search answers from the product list until it is switched on.</div>
        {:else}
            <dl class="meili-facts txt-sm">
                <dt>Last sync</dt>
                <dd>{status.syncing ? "Syncing now…" : status.last_sync ? relativeTime(status.last_sync) : "Not yet"}</dd>
                <dt>Written</dt>
                <dd>{status.indexed} documents since the server started</dd>
            </dl>
            {#if status.last_error}
                <div class="alert danger m-t-sm" role="alert"><p>{status.last_error}</p></div>
            {/if}
            {#if can("catalog.write")}
                <button
                    type="button"
                    class="btn sm secondary m-t-sm"
                    class:loading={rebuilding}
                    disabled={rebuilding || !status.configured}
                    onclick={rebuild}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                    <span class="txt">Rebuild index</span>
                </button>
            {/if}
        {/if}
    </div>
{/if}

<style>
    .meili-facts {
        display: grid;
        grid-template-columns: auto 1fr;
        gap: 4px 12px;
        margin: 0;
    }
    .meili-facts dt {
        color: var(--txtHintColor);
    }
    .meili-facts dd {
        margin: 0;
    }
</style>
