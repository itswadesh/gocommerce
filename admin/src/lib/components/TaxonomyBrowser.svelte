<script>
    /**
     * The Shopify taxonomy, browsed and switched on per node (D143).
     *
     * The master tree is the engine's, shared by every store and read-only;
     * what a store owns is which nodes it has activated, and each of those is
     * an ordinary category in the tree behind this drawer. A switch is the
     * engine's answer, not the click: it is drawn from what came back, so a
     * refused deactivation — products are still filed under it — stays on.
     *
     * One level at a time, like the category tree, because the master is
     * fourteen thousand nodes; the search reads all of them on the engine.
     */
    import { api, query } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { openBranch, closeBranch, applyActivation } from "$lib/taxonomy.js";
    import Drawer from "$lib/components/Drawer.svelte";

    let {
        open = false,
        writable = false,
        onclose,
        /** Called after any switch the engine accepted, so the tree behind can re-read. */
        onchange,
    } = $props();

    let rows = $state([]);
    let expanded = $state(new Set());
    let loading = $state(false);
    let busy = $state(null);

    let draft = $state("");
    let term = $state("");
    let matches = $state([]);
    let matchTotal = $state(0);
    let searching = $state(false);
    let searchReq = 0;

    const shown = $derived(term ? matches : rows);

    /* The roots, the first time the drawer opens. */
    $effect(() => {
        if (open && !rows.length && !loading) loadRoots();
    });

    async function loadRoots() {
        loading = true;
        try {
            rows = (await api.get("/api/admin/taxonomy")).data ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function toggle(node) {
        const index = rows.findIndex((r) => r.id === node.id);
        if (index < 0) return;
        const next = new Set(expanded);
        if (expanded.has(node.id)) {
            for (const r of rows) if (r.id.startsWith(node.id + "-")) next.delete(r.id);
            next.delete(node.id);
            rows = closeBranch(rows, index);
            expanded = next;
            return;
        }
        busy = node.id;
        try {
            const kids = (await api.get("/api/admin/taxonomy" + query({ parent: node.id }))).data ?? [];
            rows = openBranch(rows, index, kids);
            expanded = next.add(node.id);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = null;
        }
    }

    async function runSearch(event) {
        event?.preventDefault();
        term = draft.trim();
        const mine = ++searchReq;
        if (!term) return;
        searching = true;
        try {
            const res = await api.get("/api/admin/taxonomy" + query({ q: term, limit: 100 }));
            if (mine !== searchReq) return;
            matches = res.data ?? [];
            matchTotal = res.meta?.total ?? matches.length;
        } catch (err) {
            if (mine === searchReq) toast.error(err);
        } finally {
            if (mine === searchReq) searching = false;
        }
    }

    function clearSearch() {
        draft = "";
        term = "";
        searchReq++;
        matches = [];
    }

    async function flip(node) {
        if (busy) return;
        busy = node.id;
        const path = `/api/admin/taxonomy/${encodeURIComponent(node.id)}/activation`;
        try {
            if (node.active) {
                await api.delete(path);
                rows = applyActivation(rows, node, false);
                matches = applyActivation(matches, node, false);
                toast.success(`Deactivated ${node.name}`);
            } else {
                const res = await api.put(path);
                const created = res.data?.created ?? 0;
                rows = applyActivation(rows, node, true, res.data?.category);
                matches = applyActivation(matches, node, true, res.data?.category);
                toast.success(
                    created > 1 ? `Activated ${node.name} and ${created - 1} above it` : `Activated ${node.name}`,
                );
            }
            onchange?.();
        } catch (err) {
            // A 409 names what is in the way: products, or a category of the
            // store's own inside the branch.
            toast.error(err);
        } finally {
            busy = null;
        }
    }
</script>

<Drawer {open} title="Shopify taxonomy" size="md" {onclose}>
    <p class="txt-hint txt-sm m-b-sm">
        Switch on the categories your store uses. Switching one on adds the categories above
        it too, with their product fields. Switching one off removes it and everything under
        it from your store, and is refused while products are filed there.
    </p>

    <form class="fields m-b-sm" onsubmit={runSearch}>
        <div class="field">
            <input
                id="taxonomy-search"
                aria-label="Search the taxonomy"
                type="text"
                placeholder="Search 14,000 categories, e.g. t-shirts"
                autocomplete="off"
                bind:value={draft}
            />
        </div>
        {#if draft || term}
            <div class="field addon p-r-5">
                {#if draft.trim() !== term}
                    <button type="submit" class="btn sm pill warning">Search</button>
                {/if}
                <button type="button" class="btn sm pill secondary transparent" onclick={clearSearch}>
                    Clear
                </button>
            </div>
        {/if}
    </form>

    {#if term}
        <div class="txt-hint txt-sm m-b-5" aria-live="polite">
            {#if searching}Searching…{:else}{matchTotal} {matchTotal === 1 ? "match" : "matches"}{#if matchTotal > matches.length}, first {matches.length} shown{/if}{/if}
        </div>
    {/if}

    <ul class="list taxonomy-list" aria-busy={loading || searching}>
        {#each shown as node (node.id)}
            <li class="list-item taxonomy-row" in:rise={{ y: 4, duration: 150 }}>
                <div class="content">
                    {#if !term}
                        <span class="category-indent" class:nested={node.depth > 0} style="--depth: {node.depth}"></span>
                        {#if node.child_count > 0}
                            <button
                                type="button"
                                class="btn circle sm transparent secondary"
                                class:loading={busy === node.id && !expanded.has(node.id)}
                                aria-expanded={expanded.has(node.id)}
                                aria-label="{expanded.has(node.id) ? 'Collapse' : 'Expand'} {node.name}"
                                onclick={() => toggle(node)}
                            >
                                <i
                                    class={expanded.has(node.id) ? "ri-arrow-down-s-line" : "ri-arrow-right-s-line"}
                                    aria-hidden="true"
                                ></i>
                            </button>
                        {:else}
                            <span class="expand-spacer" aria-hidden="true"></span>
                        {/if}
                    {/if}
                    <span class="taxonomy-name">
                        <span class={term ? "txt-sm" : "txt-bold"}>{term ? node.full_name : node.name}</span>
                        {#if node.active && node.title && node.title !== node.name}
                            <span class="txt-hint txt-sm">shown as “{node.title}”</span>
                        {/if}
                    </span>
                </div>
                <div class="actions">
                    <div class="field">
                        <input
                            type="checkbox"
                            class="switch"
                            id="taxonomy-{node.id}"
                            aria-label="{node.active ? 'Deactivate' : 'Activate'} {node.full_name}"
                            checked={node.active}
                            disabled={!writable || busy === node.id}
                            onchange={(e) => {
                                // Drawn from the engine's answer, not the click.
                                e.currentTarget.checked = node.active;
                                flip(node);
                            }}
                        />
                        <label for="taxonomy-{node.id}"></label>
                    </div>
                </div>
            </li>
        {/each}
    </ul>

    {#if (loading || searching) && !shown.length}
        <div class="skeleton-loader m-t-sm"></div>
    {:else if term && !searching && !matches.length}
        <p class="txt-hint txt-center m-t-sm">No taxonomy category matches “{term}”.</p>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={onclose}>
            <span class="txt">Done</span>
        </button>
    {/snippet}
</Drawer>

<style>
    .taxonomy-row {
        min-height: 44px;
        padding-top: 4px;
        padding-bottom: 4px;
        transition: background var(--animationSpeed);
    }
    .taxonomy-list {
        margin: 0;
        padding: 0;
        list-style: none;
    }
    .taxonomy-name {
        display: flex;
        flex-wrap: wrap;
        align-items: baseline;
        gap: 0 6px;
        min-width: 0;
    }
</style>
