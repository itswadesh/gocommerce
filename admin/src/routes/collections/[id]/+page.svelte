<script>
    /**
     * One collection: its fields, and the curated list of products in it.
     *
     * The curation is the reason this page exists. `product_collections` carries
     * two orders — `position`, where the collection sits in a product's own
     * list, and `member_position`, where the product sits inside the collection
     * — and only the first of them had a writer. The product listing orders a
     * collection by the second, so until something could write it every
     * storefront's "curated" order was product id ascending.
     *
     * `PUT /api/admin/collections/{id}/products` takes the whole ordered array
     * and writes each index, so this page holds the entire membership and sends
     * it in one request. That is also its one limit: a collection larger than
     * the cap below cannot be curated here, because a PUT of the rows in hand
     * would delete the ones that are not. The page says so rather than
     * pretending, and the API is still there for a list that long.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page as route } from "$app/state";
    import { api, request, query } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate } from "$lib/format.js";
    import CollectionEditor from "$lib/components/CollectionEditor.svelte";
    import CollectionProducts from "$lib/components/CollectionProducts.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import SaveBar from "$lib/components/SaveBar.svelte";

    /* 200 is the engine's MaxLimit, so this is five requests at worst. The cap
       is where "hold the whole membership in a page" stops being reasonable —
       past it the PUT this screen builds would be a thousand ids long, and the
       operator would be dragging rows through a list nobody can read. */
    const MEMBER_LIMIT = 200;
    const MEMBER_CAP = 1000;

    const id = $derived(route.params.id);

    let collection = $state(null);
    let members = $state([]);
    /* The membership as the engine last confirmed it, held as the rows and not
       as their ids: Discard has to put back a product that was removed, and an
       id alone cannot draw one. */
    let savedRows = $state([]);
    let memberTotal = $state(0);
    let loading = $state(true);
    let saving = $state(false);
    let notFound = $state(false);

    let editorOpen = $state(false);
    let confirmOpen = $state(false);

    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));
    /* Fewer rows were read than the collection holds, so the ordered array this
       page would PUT is not the membership — it is a prefix of it, and sending
       it would delete the tail. Everything that writes is off in that case. */
    const partial = $derived(memberTotal > savedRows.length);
    const dirty = $derived(
        savedRows.length !== members.length ||
            members.some((p, i) => p.id !== savedRows[i].id),
    );

    $effect(() => {
        id;
        load();
    });

    async function load() {
        loading = true;
        notFound = false;
        // The screen renders NoAccess without this right, so firing the
        // request first would bury that explanation under a 403 toast.
        if (!readable) {
            loading = false;
            return;
        }
        try {
            collection = await api.get(`/api/admin/collections/${id}`);
            await loadMembers();
        } catch (err) {
            if (err.status === 404) notFound = true;
            else toast.error(err);
        } finally {
            loading = false;
        }
    }

    /**
     * The whole membership, in the order the engine serves it, up to the cap.
     *
     * Paged rather than asked for at once because the listing's limit is 200,
     * and read in full rather than a window because the write is a replacement:
     * a page of a membership is not a membership.
     */
    async function loadMembers() {
        let pageNo = 1;
        let all = [];
        for (;;) {
            const result = await api.get(
                `/api/admin/collections/${id}/products` +
                    query({ page: pageNo, limit: MEMBER_LIMIT }),
            );
            const rows = result.data ?? [];
            all = [...all, ...rows];
            memberTotal = result.meta?.total ?? all.length;
            if (!rows.length || all.length >= memberTotal || all.length >= MEMBER_CAP) break;
            pageNo++;
        }
        members = all;
        savedRows = [...all];
    }

    async function save() {
        if (saving || !dirty) return;
        saving = true;
        try {
            // 204, so there is nothing to take back as the new truth; the ids
            // just sent are it.
            await request("PUT", `/api/admin/collections/${id}/products`, {
                body: { product_ids: members.map((p) => p.id) },
            });
            savedRows = [...members];
            memberTotal = members.length;
            toast.success("Collection saved");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function reset() {
        members = [...savedRows];
    }

    function saved(updated) {
        collection = updated;
        editorOpen = false;
    }

    async function doDelete() {
        try {
            await api.delete(`/api/admin/collections/${id}`);
            toast.success(`Deleted ${collection.title}`);
            goto(`${base}/collections`);
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>{collection?.title || "Collection"} · GoCommerce</title></svelte:head>

{#if !can("catalog.read")}
    <NoAccess right="catalog.read" what="collections" />
{:else}
    <!-- The compact card skin the product and page editors use: three editors
         that look unrelated is worse than one shared class. -->
    <div class="page page-collection shopify-skin skin-recessed">
        <div class="page-content full-height">
            <SaveBar
                dirty={dirty && writable && !partial}
                {saving}
                message="Unsaved order"
                onsave={save}
                ondiscard={reset}
            />

            <header class="page-header">
                <nav class="breadcrumbs">
                    <a href="{base}/collections">Collections</a>
                    <div>{collection?.title || "…"}</div>
                </nav>

                {#if collection && writable}
                    <div class="page-header-primary-btns">
                        <button
                            type="button"
                            class="btn secondary"
                            onclick={() => (editorOpen = true)}
                        >
                            <i class="ri-edit-line" aria-hidden="true"></i>
                            <span class="txt">Edit details</span>
                        </button>
                        <button
                            type="button"
                            class="btn secondary"
                            onclick={() => (confirmOpen = true)}
                        >
                            <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                            <span class="txt">Delete</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if notFound}
                <div class="txt-center txt-hint p-base">
                    No collection with that id. It may have been deleted.
                    <a href="{base}/collections">Back to collections</a>
                </div>
            {:else if loading && !collection}
                <div class="block txt-center p-base"><span class="loader lg"></span></div>
            {:else if collection}
                <div class="wrapper">
                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-stack-line" aria-hidden="true"></i>
                            Products
                        </h6>

                        <CollectionProducts
                            bind:members
                            disabled={!writable || partial}
                            {partial}
                        />

                        <div class="field-help">
                            This order is the collection's own, and a storefront reading
                            <code class="txt-code">/api/collections/{collection.slug}</code>
                            gets the products in it. It is not the order the collections
                            themselves appear in — that is each collection's position.
                        </div>
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                            Details
                        </h6>

                        <table class="table">
                            <tbody>
                                <tr>
                                    <td class="txt-hint">Slug</td>
                                    <td class="txt-right txt-code">{collection.slug}</td>
                                </tr>
                                <tr>
                                    <td class="txt-hint">Position</td>
                                    <td class="txt-right">{collection.position}</td>
                                </tr>
                                <tr>
                                    <td class="txt-hint">Updated</td>
                                    <td class="txt-right">{formatDate(collection.updated_at)}</td>
                                </tr>
                            </tbody>
                        </table>

                        {#if collection.description}
                            <p class="txt-sm m-t-sm">{collection.description}</p>
                        {:else}
                            <div class="field-help">
                                No description. A storefront has nothing to put at the head of
                                this collection.
                            </div>
                        {/if}
                    </section>
                </div>
            {/if}

            <footer class="page-footer">
                <span class="txt">
                    {#if collection}
                        {memberTotal}
                        {memberTotal === 1 ? "product" : "products"}
                    {/if}
                </span>
            </footer>
        </div>
    </div>

    <CollectionEditor
        open={editorOpen}
        {collection}
        onsaved={saved}
        onclose={() => (editorOpen = false)}
    />

    <Confirm
        bind:open={confirmOpen}
        title="Delete this collection?"
        message={collection
            ? `${collection.title} will be removed and the ${memberTotal} ${memberTotal === 1 ? "product" : "products"} in it lose that grouping. The products themselves are untouched, and a storefront linking to /collections/${collection.slug} will stop finding anything.`
            : ""}
        confirmLabel="Delete"
        danger
        onconfirm={doDelete}
    />
{/if}
