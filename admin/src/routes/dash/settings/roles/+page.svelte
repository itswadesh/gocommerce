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
     * A store makes roles of its own here (D80), the KitCommerce admin's way:
     * a name first, then the rights on the role's own page, where there is
     * room for the matrix. Deleting is on that page too, offered only once
     * nobody holds the role, which is why the list counts holders.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { roles as rolesApi, can, getRecord } from "$lib/api.js";
    import { learnRights, rightLabel } from "$lib/rights.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Drawer from "$lib/components/Drawer.svelte";

    let loading = $state(true);
    let matrix = $state(null);
    let search = $state("");

    const me = getRecord();

    /* roles.write is what the nav gates this link on, and it is the only right
       the screen has: there is no read-only view of the matrix on the API.
       Said again here so a typed address refuses before the request. */
    const allowed = $derived(can("roles.write"));

    /* The name and the sentence come from the API, because a store may change
       both. The panel keeping its own copy is how a renamed role went on
       showing the engine's word here while the role's own screen showed the
       store's. */

    /* Every right, not the first six and a number.
       The engine has twenty-one of them and the names are short, so the whole
       set wraps into two or three lines at this width — and "+7 more" was
       hiding exactly the part an operator scans this table for, which is what
       one role has that another does not. */

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
            learnRights(matrix?.catalogue);
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
                label: row.title || row.role,
                blurb: row.description ?? "",
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


    /* The new-role drawer. The key is shown before saving because it is
       permanent (written on every operator in the role) and the engine
       refuses rather than tidies one that is not already in shape. */
    let addOpen = $state(false);
    let creating = $state(false);
    let draft = $state({ name: "", description: "" });
    let draftError = $state("");
    const draftKey = $derived(
        draft.name
            .toLowerCase()
            .trim()
            .replace(/[^a-z0-9]+/g, "_")
            .replace(/^[^a-z]+|_+$/g, "")
            .slice(0, 40),
    );

    function openAdd() {
        draft = { name: "", description: "" };
        draftError = "";
        addOpen = true;
    }

    async function createRole(event) {
        event.preventDefault();
        if (!draft.name.trim() || !draftKey) {
            draftError = "Give the role a name that starts with a letter.";
            return;
        }
        creating = true;
        draftError = "";
        try {
            await rolesApi.create({
                key: draftKey,
                title: draft.name.trim(),
                description: draft.description.trim(),
                rights: [],
            });
            toast.success("Role created");
            addOpen = false;
            open(draftKey);
        } catch (err) {
            draftError = err?.message ?? String(err);
        } finally {
            creating = false;
        }
    }

    function open(role) {
        goto(`${base}/dash/settings/roles/${role}`);
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

                <div class="btns-group">
                    <button type="button" class="btn" aria-label="Add role" title="Add role" onclick={openAdd}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">Add role</span>
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

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Name</th>
                            <th>Rights</th>
                            <th class="col-field-type-number min-width">Holders</th>
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
                                        <div class="right-chips">
                                            {#each row.rights as right (right)}
                                                <span class="right-chip" title={rightLabel(right)}
                                                    >{right}</span
                                                >
                                            {/each}
                                        </div>
                                    {/if}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Holders">
                                    {row.holders ?? 0}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Against defaults">
                                    {#if !row.configurable}
                                        <span class="txt-hint" title="The engine fixes this role.">Fixed</span>
                                    {:else if !row.builtin}
                                        <span class="txt-hint" title="This store's own role.">Own role</span>
                                    {:else if row.customised}
                                        <span class="label">Customised</span>
                                    {:else}
                                        <span class="txt-hint">Defaults</span>
                                    {/if}
                                </td>
                            </tr>
                        {/each}

                        {#if loading && !matrix}
                            <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                        {:else if !rows.length}
                            <tr>
                                <td colspan="4" class="txt-center txt-hint p-base">
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
                    {#if me?.role}You are {rows.find((r) => r.role === me.role)?.label ?? me.role}.{/if}
                </span>
            </footer>
        </div>
    </div>

    <Drawer open={addOpen} title="New role" size="sm" onclose={() => (addOpen = false)}>
        <form id="role-form" onsubmit={createRole}>
            <div class="field required" class:error={!!draftError}>
                <label for="role_name">Name</label>
                <!-- svelte-ignore a11y_autofocus -->
                <input id="role_name" type="text" maxlength="60" required autofocus bind:value={draft.name} />
                {#if draftKey}
                    <div class="field-help">
                        Key: <code>{draftKey}</code>. It is permanent, written on everybody in the role.
                    </div>
                {/if}
                {#if draftError}<div class="help-block help-block-error">{draftError}</div>{/if}
            </div>
            <div class="field">
                <label for="role_description">Description</label>
                <textarea id="role_description" rows="3" maxlength="600" bind:value={draft.description}
                ></textarea>
                <div class="field-help">
                    You choose its rights on the next screen. It starts with seeing the catalogue,
                    which every role keeps.
                </div>
            </div>
        </form>

        {#snippet footer()}
            <button type="button" class="btn transparent m-r-auto" onclick={() => (addOpen = false)}>
                <span class="txt">Cancel</span>
            </button>
            <button type="submit" form="role-form" class="btn" class:loading={creating} disabled={creating}>
                <span class="txt">Create role</span>
            </button>
        {/snippet}
    </Drawer>
{/if}
