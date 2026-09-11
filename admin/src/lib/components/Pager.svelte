<script>
    /**
     * The page control at the foot of a list.
     *
     * Every number it shows is read off the response envelope and none is
     * computed here: `ListMeta` derives `page` and `total_pages` server-side
     * precisely so a UI drawing "3 of 12" never has to do the arithmetic, and
     * the panel was reading only `meta.total` out of it.
     *
     * It lives inside the `.page-footer` every list screen already has, beside
     * the count it replaces. It cannot extend `.load-more-btn`, which is
     * PocketBase's and frozen, so the two rules it needs are in gocommerce.css.
     */
    import { pluralize } from "$lib/format.js";

    let { meta = null, noun = "item", plural = "", loading = false, onpage } = $props();

    const page = $derived(meta?.page ?? 1);
    const pages = $derived(meta?.total_pages ?? 0);
    const total = $derived(meta?.total ?? 0);
    const first = $derived(total === 0 ? 0 : (meta?.offset ?? 0) + 1);
    const last = $derived(Math.min((meta?.offset ?? 0) + (meta?.limit ?? 0), total));

    function go(n) {
        const wanted = Math.min(Math.max(Math.round(n) || 1, 1), Math.max(pages, 1));
        if (wanted !== page) onpage?.(wanted);
    }

    /*
     * A typed page that is out of range snaps back to the current one rather
     * than navigating: the operator is mid-edit in the box and an out-of-range
     * request would only come back empty.
     */
    function commit(event) {
        const typed = parseInt(event.currentTarget.value, 10);
        if (!Number.isFinite(typed) || typed < 1 || typed > pages) {
            event.currentTarget.value = String(page);
            return;
        }
        go(typed);
    }
</script>

<span class="txt">
    {#if meta}
        Showing {first}–{last} of {total}
        {pluralize(total, noun, plural)}
    {:else}
        …
    {/if}
</span>

{#if pages > 1}
    <nav class="list-pager" aria-label="Pages">
        <button
            type="button"
            class="btn circle sm transparent secondary"
            aria-label="Previous page"
            disabled={page <= 1 || loading}
            onclick={() => go(page - 1)}
        >
            <i class="ri-arrow-left-s-line" aria-hidden="true"></i>
        </button>

        <input
            type="number"
            class="pager-page"
            min="1"
            max={pages}
            value={page}
            aria-label="Page number"
            disabled={loading}
            onchange={commit}
        />

        <span class="txt">of {pages}</span>

        <button
            type="button"
            class="btn circle sm transparent secondary"
            aria-label="Next page"
            disabled={page >= pages || loading}
            onclick={() => go(page + 1)}
        >
            <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
        </button>
    </nav>
{/if}
