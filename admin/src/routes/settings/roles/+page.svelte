<script>
    /**
     * The roles, as a list.
     *
     * This screen used to be the whole matrix — twenty-one rights down the
     * side, three roles across — which answered "what has manager got that
     * staff has not" in one glance and answered "what may staff do" by
     * reading a column of twenty-one checkboxes. The list answers the second
     * question, which is the one an operator actually arrives with, and hands
     * the first to the role's own screen where there is room for it.
     *
     * A role is not created or deleted here, because the engine's roles are
     * fixed (rights.go): owner, manager, staff. What a store changes is what
     * each one carries, which is what the row's chips show and what the
     * screen behind it edits.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { roles as rolesApi, can, getRecord } from "$lib/api.js";
    import { rightLabel } from "$lib/rights.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";

    let loading = $state(true);
    let matrix = $state(null);
    let search = $state("");

    const me = getRecord();

    /* roles.write is what the nav gates this link on, and it is the only right
       the screen has: there is no read-only view of the matrix on the API.
       Said again here so a typed address refuses before the request. */
    const allowed = $derived(can("roles.write"));

    const ROLE_LABEL = { owner: "Owner", manager: "Manager", staff: "Staff" };
    const ROLE_BLURB = {
        owner: "Everything, including who else may do what.",
        manager: "Runs the shop: the catalogue, the orders, the money going back out.",
        staff: "Works the orders, and cannot send money out or change who has access.",
    };

    /* How many chips a row shows before it stops counting them out. Six is
       what fits on one line at the width this table gets; the rest become a
       number, the way Litekart's roles list does it. */
    const CHIP_CAP = 6;

    $effect(() => {
        load();
    });

    async function load() {
        if (!allowed) {
            loading = false;
            return;
        }
        loading = true;
        try {
            // request() already unwraps the {data} envelope, so there is no second
            // .data to reach through; asking for one silently yields null.
            matrix = await rolesApi.matrix();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    const rows = $derived.by(() => {
        const all = matrix?.roles ?? [];
        const needle = search.trim().toLowerCase();
        return all
            .map((row) => ({
                ...row,
                label: ROLE_LABEL[row.role] ?? row.role,
                blurb: ROLE_BLURB[row.role] ?? "",
                // A role that carries every right says so in one word. Twenty-one
                // chips is not a summary of anything.
                everything: row.rights.length === (matrix?.all_rights?.length ?? 0),
                customised: !same(row.rights, row.default ?? []),
            }))
            .filter(
                (row) =>
                    !needle ||
                    row.label.toLowerCase().includes(needle) ||
                    row.role.includes(needle) ||
                    row.rights.some((r) => r.includes(needle)),
            );
    });

    function same(a, b) {
        if (a.length !== b.length) return false;
        const x = [...a].sort();
        const y = [...b].sort();
        return x.every((v, i) => v === y[i]);
    }

    const shown = (row) => row.rights.slice(0, CHIP_CAP);
    const extra = (row) => Math.max(0, row.rights.length - CHIP_CAP);

    function open(role) {
        goto(`${base}/settings/roles/${role}`);
    }

    /* A row is a link, so Enter and Space open it the way a link would. */
    function onRowKey(event, role) {
        if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            open(role);
        }
    }
</script>

<svelte:head><title>Roles · GoCommerce</title></svelte:head>

{#if !allowed}
    <NoAccess right="roles.write" what="the roles" />
{:else}
    <div class="page page-roles shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">
                        {matrix ? `${matrix.roles.length} roles` : "Roles"}
                    </div>
                </nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        disabled={loading}
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                <!-- Inside the header, which is where .fields.searchbar is
                     laid out; below it the icon and the input stack. -->
                <div class="fields searchbar">
                    <div class="field">
                        <input
                            type="text"
                            class="p-l-20"
                            placeholder="Search roles and rights"
                            aria-label="Search roles"
                            bind:value={search}
                        />
                    </div>
                    {#if search}
                        <div class="field addon p-r-5">
                            <button
                                type="button"
                                class="btn sm pill secondary transparent"
                                onclick={() => (search = "")}>Clear</button
                            >
                        </div>
                    {/if}
                </div>
            </header>

            <!-- No "Add role" button, and that is the engine rather than an
                 omission: the roles are fixed in rights.go, and a store
                 changes what they carry rather than how many there are. -->
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Name</th>
                            <th>Rights</th>
                            <th class="col-field-type-number min-width">Against defaults</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.role)}
                            <tr
                                class="row-handle"
                                tabindex="0"
                                role="link"
                                aria-label="Open the {row.label} role"
                                onclick={() => open(row.role)}
                                onkeydown={(e) => onRowKey(e, row.role)}
                            >
                                <td class="col-field-name-id" data-name="Name">
                                    <div class="row-name row-name-stacked">
                                        <span class="txt-bold">{row.label}</span>
                                        <span class="txt-hint txt-sm">{row.blurb}</span>
                                    </div>
                                </td>
                                <td data-name="Rights">
                                    {#if row.everything}
                                        <span class="label">Everything</span>
                                    {:else if !row.rights.length}
                                        <span class="txt-hint">Nothing</span>
                                    {:else}
                                        <div class="token-list flex flex-wrap gap-sm">
                                            {#each shown(row) as right (right)}
                                                <span class="label" title={rightLabel(right)}>{right}</span>
                                            {/each}
                                            {#if extra(row)}
                                                <span class="label txt-hint">+{extra(row)} more</span>
                                            {/if}
                                        </div>
                                    {/if}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Against defaults">
                                    {#if !row.configurable}
                                        <span class="txt-hint" title="The engine fixes this role.">Fixed</span>
                                    {:else if row.customised}
                                        <span class="label">Customised</span>
                                    {:else}
                                        <span class="txt-hint">Defaults</span>
                                    {/if}
                                </td>
                            </tr>
                        {/each}

                        {#if loading && !matrix}
                            <tr><td colspan="3"><span class="skeleton-loader"></span></td></tr>
                        {:else if !rows.length}
                            <tr>
                                <td colspan="3" class="txt-center txt-hint p-base">
                                    No role matches that.
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt txt-hint">
                    A change lands on the next request the affected operator makes; nobody has to
                    sign in again.
                    {#if me?.role}You are {ROLE_LABEL[me.role] ?? me.role}.{/if}
                </span>
            </footer>
        </div>
    </div>
{/if}
