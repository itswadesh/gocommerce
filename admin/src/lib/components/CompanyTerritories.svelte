<script>
    /**
     * A dealer's territories: the areas whose enquiries are sent to it.
     *
     * An area is a country, a state in it, and a postcode prefix in that, and
     * it has exactly one dealer — the engine refuses an area another dealer
     * already holds and names them, so that refusal is shown as the engine
     * words it rather than as a generic failure. Two dealers can still cover
     * one address at different depths ({US} and {US, CA}); the most specific
     * wins, which the help text says once so nobody has to learn it from a
     * misrouted lead.
     *
     * The page rides in the URL as `tpage`, beside the orders' own `page`, so
     * the two tables on one screen page independently.
     */
    import { api, query } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate } from "$lib/format.js";
    import { COUNTRIES } from "$lib/countries.js";
    import { areaLabel, countryName } from "$lib/b2b.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    let { companyId, companyName = "", writable = false } = $props();

    const PER_PAGE = 25;
    const list = listState({ tpage: 1 });

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let reqId = 0;

    let form = $state({ country: "", state: "", postal_prefix: "" });
    let adding = $state(false);
    let addError = $state("");

    let confirmOpen = $state(false);
    let pending = $state(null);

    const countryOptions = COUNTRIES.map((c) => ({ value: c.value, label: `${c.label} (${c.value})`, short: c.label }));

    $effect(() => {
        companyId;
        list.params;
        load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const result = await api.get(
                `/api/admin/x/b2b/companies/${companyId}/territories` +
                    query({ page: list.params.tpage, limit: PER_PAGE }),
            );
            if (mine !== reqId) return;
            rows = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function add(event) {
        event.preventDefault();
        if (adding || !writable) return;
        addError = "";
        if (!form.country) {
            addError = "Pick the country first; a state and a postcode prefix narrow it.";
            return;
        }
        adding = true;
        try {
            const t = await api.post(`/api/admin/x/b2b/companies/${companyId}/territories`, {
                country: form.country,
                state: form.state.trim(),
                postal_prefix: form.postal_prefix.trim(),
            });
            toast.success(`${companyName || "This dealer"} now covers ${areaLabel(t)}`);
            // The country stays: the next territory is usually a neighbour.
            form = { country: form.country, state: "", postal_prefix: "" };
            await load();
        } catch (err) {
            // 409 is another dealer holding exactly this area, and the engine's
            // sentence names them — the thing the operator needs next.
            addError = err.message;
            toast.error(err);
        } finally {
            adding = false;
        }
    }

    function askRemove(t) {
        pending = t;
        confirmOpen = true;
    }

    async function remove() {
        if (!pending) return;
        try {
            await api.delete(`/api/admin/x/b2b/companies/${companyId}/territories/${pending.id}`);
            toast.success(`Took back ${areaLabel(pending)}`);
            // The last row of a later page going leaves that page empty.
            if (rows.length === 1 && list.params.tpage > 1) list.set({ tpage: list.params.tpage - 1 });
            else await load();
        } catch (err) {
            toast.error(err);
        } finally {
            pending = null;
        }
    }
</script>

<p class="field-help m-b-sm">
    Enquiries from the storefront's dealer form are sent to the dealer whose territory covers the
    address. The most specific territory wins — a state outranks a whole country, and a longer
    postcode prefix outranks a shorter one — and a closed dealer is skipped. A territory belongs to
    one dealer.
</p>

<div class="page-table-wrapper tw:rounded-xl tw:border m-b-sm" class:faded={loading && rows.length > 0}>
    <table class="table responsive-table">
        <thead>
            <tr>
                <th class="col-field-name-id">Country</th>
                <th class="min-width">State</th>
                <th class="min-width">Postcodes</th>
                <th class="min-width">Added</th>
                {#if writable}<th class="min-width" aria-label="Remove"></th>{/if}
            </tr>
        </thead>
        <tbody>
            {#if loading && !rows.length}
                <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
            {/if}
            {#each rows as t (t.id)}
                <tr>
                    <td class="col-field-name-id" data-name="Country">
                        <div class="row-name row-name-stacked">
                            <span class="txt-bold">{countryName(t.country)}</span>
                            <span class="txt-hint txt-sm txt-code">{t.country}</span>
                        </div>
                    </td>
                    <td class="min-width" data-name="State">
                        {#if t.state}{t.state}{:else}<span class="txt-hint">Every state</span>{/if}
                    </td>
                    <td class="min-width" data-name="Postcodes">
                        {#if t.postal_prefix}
                            <span class="txt-code">{t.postal_prefix}…</span>
                        {:else}
                            <span class="txt-hint">Any</span>
                        {/if}
                    </td>
                    <td class="min-width txt-hint txt-sm" data-name="Added">
                        {formatDate(t.created_at, { withTime: false })}
                    </td>
                    {#if writable}
                        <td class="min-width b2b-actions">
                            <button
                                type="button"
                                class="btn circle sm transparent danger"
                                title="Take back {areaLabel(t)}"
                                aria-label="Take back {areaLabel(t)}"
                                onclick={() => askRemove(t)}
                            >
                                <i class="ri-delete-bin-line" aria-hidden="true"></i>
                            </button>
                        </td>
                    {/if}
                </tr>
            {/each}
            {#if !loading && !rows.length}
                <tr>
                    <td colspan="5" class="txt-hint txt-center p-base">
                        No territories. Enquiries are only sent to a dealer that has one; without, they
                        stay with the store.
                    </td>
                </tr>
            {/if}
        </tbody>
    </table>
</div>

{#if meta?.total_pages > 1}
    <div class="b2b-pager m-b-sm">
        <Pager {meta} {loading} noun="territory" plural="territories" onpage={(n) => list.set({ tpage: n })} />
    </div>
{/if}

{#if writable}
    <form class="b2b-inline-form" onsubmit={add} novalidate>
        <div class="field b2b-wide" class:error={!!addError && !form.country}>
            <label for="territory-country">Country</label>
            <Select
                id="territory-country"
                bind:value={form.country}
                options={countryOptions}
                placeholder="Pick a country"
                onchange={() => (addError = "")}
            />
        </div>
        <div class="field b2b-grow">
            <label for="territory-state">State</label>
            <input
                id="territory-state"
                type="text"
                autocomplete="off"
                placeholder="Every state"
                bind:value={form.state}
                oninput={() => (addError = "")}
            />
        </div>
        <div class="field b2b-fixed">
            <label for="territory-prefix">Postcodes starting</label>
            <input
                id="territory-prefix"
                type="text"
                class="txt-code"
                autocomplete="off"
                placeholder="Any"
                bind:value={form.postal_prefix}
                oninput={() => (addError = "")}
            />
        </div>
        <button type="submit" class="btn" class:loading={adding} disabled={adding}>
            <i class="ri-map-pin-add-line" aria-hidden="true"></i>
            <span class="txt">Add territory</span>
        </button>
    </form>
    <div class="field-help m-b-base">
        {#if addError}
            <span class="txt-danger b2b-notice" role="alert">{addError}</span>
        {:else}
            A state in the United States, Canada, Australia or India can be typed as its code or its
            name — "California" is stored as CA, and an enquiry from either reaches it. Elsewhere it is
            matched as written. Only an enquiry naming a state reaches a state's territory, and spaces
            and hyphens in a postcode are ignored.
        {/if}
    </div>
{/if}

<Confirm
    bind:open={confirmOpen}
    title="Take back {pending ? areaLabel(pending) : 'this territory'}?"
    message="New enquiries from there go to whichever dealer covers it next, or stay with the store. Leads already sent stay where they are."
    confirmLabel="Take back"
    danger
    onconfirm={remove}
/>
