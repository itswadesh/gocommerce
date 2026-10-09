<script>
    /**
     * One catalogue: its name, and the categories and products it allows.
     *
     * A category brings everything filed under it and under the categories
     * beneath it, read from the tree as it stands when a basket is checked —
     * so the help says that moving a category moves its products in or out,
     * rather than leaving somebody to learn it from a refused order.
     *
     * The lists are saved whole: the engine replaces them, so the form holds
     * the full set and the save bar appears when it differs from what was
     * loaded. A category or product deleted from the store since is still
     * listed, in red, because it allows nothing and somebody should take it
     * out. `new` creates on the first save and moves to the catalogue's own
     * address.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page } from "$app/state";
    import { api } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, pluralize } from "$lib/format.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import SaveBar from "$lib/components/SaveBar.svelte";
    import TargetPicker from "$lib/components/TargetPicker.svelte";

    const id = $derived(page.params.id);
    const creating = $derived(id === "new");
    const readable = $derived(can("companies.read"));
    const writable = $derived(can("companies.write"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let catalogue = $state(null);
    let loading = $state(true);
    let notFound = $state(false);
    let saving = $state(false);
    let nameError = $state("");
    let confirmOpen = $state(false);

    let form = $state(blank());
    let saved = $state(blank());
    let categoryNames = $state(new Map());
    let productNames = $state(new Map());
    let missingCategories = $state(new Set());
    let missingProducts = $state(new Set());

    function blank() {
        return { name: "", description: "", category_ids: [], product_ids: [] };
    }

    const same = (a, b) => a.length === b.length && [...a].sort((x, y) => x - y).every((v, i) => v === [...b].sort((x, y) => x - y)[i]);
    const dirty = $derived(
        form.name.trim() !== saved.name ||
            form.description.trim() !== saved.description ||
            !same(form.category_ids, saved.category_ids) ||
            !same(form.product_ids, saved.product_ids),
    );

    $effect(() => {
        id;
        if (readable && hasModule("b2b")) load();
    });

    async function load() {
        notFound = false;
        if (creating) {
            catalogue = null;
            form = blank();
            saved = blank();
            loading = false;
            return;
        }
        loading = true;
        try {
            take(await api.get(`/api/admin/x/b2b/catalogues/${id}`));
        } catch (err) {
            if (err.status === 404) notFound = true;
            else toast.error(err);
        } finally {
            loading = false;
        }
    }

    /** The engine's answer becomes both the form and what it is compared with. */
    function take(c) {
        catalogue = c;
        const snapshot = {
            name: c.name,
            description: c.description ?? "",
            category_ids: [...c.category_ids],
            product_ids: [...c.product_ids],
        };
        saved = snapshot;
        form = { ...snapshot, category_ids: [...snapshot.category_ids], product_ids: [...snapshot.product_ids] };
        categoryNames = new Map(c.categories.map((m) => [m.id, m.missing ? `Category ${m.id} (deleted)` : m.name]));
        productNames = new Map(c.products.map((m) => [m.id, m.missing ? `Product ${m.id} (deleted)` : m.name]));
        missingCategories = new Set(c.categories.filter((m) => m.missing).map((m) => m.id));
        missingProducts = new Set(c.products.filter((m) => m.missing).map((m) => m.id));
    }

    function reset() {
        form = { ...saved, category_ids: [...saved.category_ids], product_ids: [...saved.product_ids] };
        nameError = "";
    }

    async function save() {
        if (saving || !writable) return;
        nameError = "";
        if (!form.name.trim()) {
            nameError = "A catalogue needs a name.";
            return;
        }
        const body = {
            name: form.name.trim(),
            description: form.description.trim(),
            category_ids: form.category_ids,
            product_ids: form.product_ids,
        };
        saving = true;
        try {
            if (creating) {
                const c = await api.post("/api/admin/x/b2b/catalogues", body);
                toast.success(`Added ${c.name}`);
                // The form is clean now, so the guard lets the move through.
                take(c);
                await goto(`${base}/dash/b2b/catalogues/${c.id}`, { replaceState: true });
            } else {
                take(await api.patch(`/api/admin/x/b2b/catalogues/${id}`, body));
                toast.success(`Saved ${form.name.trim()}`);
            }
        } catch (err) {
            if (err.status === 409 || /name/i.test(err.message ?? "")) nameError = err.message;
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function doDelete() {
        try {
            await api.delete(`/api/admin/x/b2b/catalogues/${id}`);
            toast.success(`Deleted ${catalogue.name}`);
            saved = form;
            await goto(`${base}/dash/b2b/catalogues`);
        } catch (err) {
            // 409 names the companies held to it, which is what to fix next.
            toast.error(err);
        }
    }

    const title = $derived(creating ? "New catalogue" : catalogue?.name || "Catalogue");
</script>

<svelte:head><title>{title} · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="companies.read" what="catalogues" />
{:else}
    <div class="page page-catalogue b2b-page shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <SaveBar dirty={dirty && writable && !notFound} {saving} saveLabel={creating ? "Add catalogue" : "Save"} onsave={save} ondiscard={reset} />

            <header class="page-header">
                <nav class="breadcrumbs">
                    <a class="breadcrumb-item tw:text-sm" href="{base}/dash/b2b/catalogues">Catalogues</a>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">{title}</div>
                </nav>
                <div class="flex-fill"></div>
                {#if catalogue && writable}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn transparent danger" aria-label="Delete catalogue" onclick={() => (confirmOpen = true)}>
                            <i class="ri-delete-bin-line" aria-hidden="true"></i>
                            <span class="txt">Delete</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Catalogues are served by ext/b2b, and this binary does not have it." />
            {:else if notFound}
                <div class="wrapper sm m-auto txt-center p-t-base">
                    <h5 class="m-b-xs">This catalogue does not exist</h5>
                    <p class="txt-hint">It may have been deleted.</p>
                    <a href="{base}/dash/b2b/catalogues" class="btn secondary m-t-sm"><span class="txt">All catalogues</span></a>
                </div>
            {:else if loading}
                <span class="skeleton-loader"></span>
                <span class="skeleton-loader"></span>
            {:else}
                {#if catalogue}
                    <p class="field-help m-b-base b2b-notice">
                        {#if catalogue.company_count}
                            {catalogue.company_count}
                            {pluralize(catalogue.company_count, "company is", "companies are")} held to it: a change here
                            reaches their next basket and their next checkout.
                        {:else}
                            No company is held to it yet. Choose it on a company's page.
                        {/if}
                        Changed {formatDate(catalogue.updated_at)}.
                    </p>
                {/if}

                <form class="b2b-select-fit b2b-catalogue-form" onsubmit={(e) => { e.preventDefault(); save(); }} novalidate>
                    <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base">
                        <div class="field required" class:error={!!nameError}>
                            <label for="cat-name">Name</label>
                            <input id="cat-name" type="text" autocomplete="off" disabled={!writable} bind:value={form.name} oninput={() => (nameError = "")} />
                        </div>
                        {#if nameError}<div class="field-help error b2b-notice">{nameError}</div>{/if}
                        <div class="field m-t-sm">
                            <label for="cat-description">Description</label>
                            <textarea id="cat-description" rows="2" disabled={!writable} bind:value={form.description}></textarea>
                        </div>
                        <div class="field-help">For the store's own team: who it is for and why.</div>
                    </section>

                    <h2 class="section-title">
                        <i class="ri-node-tree" aria-hidden="true"></i>
                        Categories
                    </h2>
                    <div class="field">
                        <TargetPicker
                            id="cat-categories"
                            kind="categories"
                            bind:value={form.category_ids}
                            names={categoryNames}
                            missing={missingCategories}
                            disabled={!writable}
                        />
                    </div>
                    <p class="field-help m-b-base">
                        Each brings every product filed under it and under the categories beneath it, as the tree
                        stands when a basket is checked: move a category, and its products move in or out with it.
                    </p>

                    <h2 class="section-title">
                        <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                        Products
                    </h2>
                    <div class="field">
                        <TargetPicker
                            id="cat-products"
                            kind="products"
                            bind:value={form.product_ids}
                            names={productNames}
                            missing={missingProducts}
                            disabled={!writable}
                        />
                    </div>
                    <p class="field-help m-b-base">
                        One by one, wherever they are filed. A product in a chosen category needs no entry here.
                    </p>
                </form>
            {/if}
        </div>
    </div>

    <Confirm
        bind:open={confirmOpen}
        title={`Delete ${catalogue?.name ?? "this catalogue"}?`}
        message="A catalogue a company is held to cannot be deleted: give those companies another one, or none, first."
        confirmLabel="Delete"
        danger
        onconfirm={doDelete}
    />
{/if}
