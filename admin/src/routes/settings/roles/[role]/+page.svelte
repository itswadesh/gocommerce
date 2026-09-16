<script>
    /**
     * One role, and what it may do.
     *
     * Laid out the way Litekart's role screen is: the name and a description
     * at the top, then Permissions as a grid of one row per resource with a
     * button per verb — dark, quiet when the role does not hold it, and a
     * green check when it does.
     *
     * Two things about this engine shape the departures. The set of roles is
     * fixed (rights.go) — owner, manager and staff, none added or deleted —
     * and the KEY of each is an identifier written on every operator's record,
     * so a rename changes what the store calls a role and never what accounts
     * are filed under. And a role left alone keeps *tracking* the shipped
     * default, for its rights and for its words alike, which is what makes
     * both Reset and a blank field mean "back to the engine's own".
     *
     * The name saves on its own button, apart from the rights. They are edited
     * at different moments, and one Save for both would make a rename overwrite
     * a colleague's grant made a second earlier.
     *
     * Every right is `resource.verb`, so the grid builds itself from what the
     * API sends and a right added to the engine needs no edit here.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { roles as rolesApi, auth, can, getRecord } from "$lib/api.js";
    import { learnRights, rightScope, rightsByResource, rightsBySection } from "$lib/rights.js";
    import { toast } from "$lib/toast.svelte.js";
    import DirtyGuard from "$lib/components/DirtyGuard.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const role = $derived(page.params.role);

    let loading = $state(true);
    let matrix = $state(null);
    let draft = $state([]);
    let busy = $state(false);

    const me = getRecord();
    const allowed = $derived(can("roles.write"));

    const row = $derived(matrix?.roles.find((r) => r.role === role) ?? null);

    /* The words come from the engine now, which is what lets a store change
       them: it ships a title and a sentence per role and stores the store's own
       over the top. The panel keeping its own copy is how the two drift. */
    const label = $derived(row?.title || role);
    const blurb = $derived(row?.description ?? "");

    /* The name and description as they are being edited, seeded from the row
       and re-seeded whenever a different role is loaded or a save answers. */
    let name = $state("");
    let about = $state("");
    let renaming = $state(false);
    const labelDirty = $derived(
        !!row && (name !== (row.title ?? "") || about !== (row.description ?? "")),
    );
    const grid = $derived(rightsByResource(matrix?.all_rights ?? []));
    /* The same rows, cut into the sidebar's sections: granting access is
       easier to think about in the shape the panel is already navigated in. */
    const sections = $derived(rightsBySection(matrix?.all_rights ?? []));
    const required = $derived(matrix?.required ?? []);

    /*
     * How many buttons a row holds: as many as the widest resource has, which
     * is four today (orders: read, write, fulfil, refund) and is measured
     * rather than assumed. A fixed four silently dropped anything past it, so
     * a fifth verb added to the engine would have vanished from the only
     * screen that grants it — and a right nobody can see is a right nobody
     * can take away.
     *
     * Every row gets the same count so the buttons line up in columns down
     * the grid; a resource with fewer fills from the left.
     */
    const columns = $derived(grid.reduce((most, g) => Math.max(most, g.rights.length), 1));
    const slots = (group) => {
        const out = [...group.rights];
        while (out.length < columns) out.push(null);
        return out;
    };

    $effect(() => {
        // Named so the effect re-runs when the address changes, which is what
        // makes moving between two roles reload rather than show the previous
        // one's buttons.
        role;
        load();
    });

    async function load() {
        if (!allowed) {
            loading = false;
            return;
        }
        loading = true;
        try {
            matrix = await rolesApi.matrix();
            // A module's rights arrive with their own words; the panel has no
            // table for a module it was not built beside.
            learnRights(matrix?.catalogue);
            const mine = matrix?.roles.find((r) => r.role === role);
            draft = [...(mine?.rights ?? [])];
            seedLabel(mine);
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function seedLabel(set) {
        name = set?.title ?? "";
        about = set?.description ?? "";
    }

    /* Blank either field to put the engine's own words back, which is why
       there is no separate reset: clearing the name IS the reset, and the
       hint under the pair says so. */
    async function rename() {
        if (renaming || !labelDirty) return;
        renaming = true;
        try {
            const saved = await rolesApi.rename(role, name.trim(), about.trim());
            matrix.roles = matrix.roles.map((r) => (r.role === saved.role ? saved : r));
            seedLabel(saved);
            toast.success(saved.title + " saved");
        } catch (err) {
            toast.error(err);
        } finally {
            renaming = false;
        }
    }

    const has = (right) => draft.includes(right);

    /*
     * The two locks the API also enforces, mirrored here so nothing looks
     * pressable that the server would only refuse. Required rights are the
     * floor every role keeps; roles.write is what got you to this screen, and
     * an operator who saves it away from their own role has no way back short
     * of a static admin token.
     */
    const locked = (right) =>
        !row?.configurable ||
        required.includes(right) ||
        (role === me?.role && right === "roles.write");

    function lockReason(right) {
        if (!row?.configurable) return "The engine fixes this role; it carries every right.";
        if (required.includes(right)) {
            return "Every role keeps this: a role without it can sign in and see nothing.";
        }
        return "This is your own role — removing this would lock you out of this screen.";
    }

    function toggle(right) {
        if (locked(right)) return;
        draft = has(right) ? draft.filter((r) => r !== right) : [...draft, right];
    }

    const same = (a, b) => {
        if (a.length !== b.length) return false;
        const x = [...a].sort();
        const y = [...b].sort();
        return x.every((v, i) => v === y[i]);
    };
    const dirty = $derived(!!row?.configurable && !same(draft, row?.rights ?? []));

    /*
     * The departure from the shipped default, measured against the DRAFT so
     * the counts move as buttons are pressed. Reset is the one control here
     * that changes several rights at once, so what it would do is named on it
     * rather than left to be discovered.
     */
    const defaults = $derived(row?.default ?? []);
    const added = $derived(draft.filter((r) => !defaults.includes(r)));
    const removed = $derived(defaults.filter((r) => !draft.includes(r)));
    const isDefault = $derived(same(draft, defaults));

    /** How one button departs from the default: "added", "removed" or "". */
    function diff(right) {
        if (!row?.configurable) return "";
        const inDraft = has(right);
        const inDefault = defaults.includes(right);
        if (inDraft && !inDefault) return "added";
        if (!inDraft && inDefault) return "removed";
        return "";
    }

    function buttonTitle(right) {
        if (locked(right)) return lockReason(right);
        const scope = rightScope(right);
        switch (diff(right)) {
            case "added":
                return `${scope}. Not in what the engine ships for ${label}.`;
            case "removed":
                return `${scope}. In what the engine ships for ${label}, and taken away.`;
            default:
                return scope;
        }
    }

    function resetTitle() {
        if (isDefault) return `${label} already matches what the engine ships for it.`;
        const parts = [];
        if (added.length) parts.push("take away " + added.join(", "));
        if (removed.length) parts.push("give back " + removed.join(", "));
        return "Reset would " + parts.join(", and ") + ".";
    }

    async function save() {
        if (busy) return;
        busy = true;
        try {
            applySaved(await rolesApi.save(role, draft));
            toast.success("Saved the " + label + " role");
            await refreshMeIfAffected();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    async function reset() {
        if (busy) return;
        busy = true;
        try {
            applySaved(await rolesApi.reset(role));
            toast.success(label + " is back on the defaults");
            await refreshMeIfAffected();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    function applySaved(set) {
        if (!set?.role) return;
        matrix.roles = matrix.roles.map((r) => (r.role === set.role ? set : r));
        draft = [...set.rights];
    }

    /*
     * The panel hides what it cannot do from the record it stored at sign-in.
     * Changing your own role's rights makes that record wrong, and the symptom
     * is a nav offering a screen the engine will refuse — so re-read it.
     */
    async function refreshMeIfAffected() {
        if (me?.role !== role) return;
        try {
            await auth.refresh();
        } catch {
            /* the next request corrects it; nothing here is worth a toast */
        }
    }
</script>

<svelte:head><title>{label} role · GoCommerce</title></svelte:head>

{#if !allowed}
    <NoAccess right="roles.write" what="the roles" />
{:else}
    <DirtyGuard
        {dirty}
        message="This role has rights changed that have not been saved. Leave and lose them?"
    />

    <div class="page page-role shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <a
                        class="breadcrumb-item tw:text-sm tw:text-muted-foreground"
                        href="{base}/settings/roles">Roles</a
                    >
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">
                        {label}
                    </div>
                </nav>

                <div class="page-header-primary-btns">
                    {#if row?.configurable}
                        <!-- Both carry an icon, because the page header
                             collapses its buttons to bare circles below
                             550px and a circle with only a word in it shows
                             a clipped word. -->
                        <button
                            type="button"
                            class="btn secondary"
                            disabled={busy || isDefault}
                            title={resetTitle()}
                            onclick={reset}
                        >
                            <i class="ri-restart-line" aria-hidden="true"></i>
                            <span class="txt">Reset to defaults</span>
                        </button>
                        <button type="button" class="btn" disabled={busy || !dirty} onclick={save}>
                            <i class="ri-save-line" aria-hidden="true"></i>
                            <span class="txt">{busy ? "Saving…" : "Save"}</span>
                        </button>
                    {/if}
                </div>
            </header>

            <section class="card role-card">
                <div class="role-card-head">
                    <!-- "Roles" is a link, not a caption. It sits where a
                         breadcrumb sits and reads like one, so it was being
                         clicked and doing nothing. -->
                    <h2 class="role-title">
                        <a class="role-title-up" href="{base}/settings/roles">Roles</a>
                        <span class="role-title-sep" aria-hidden="true">/</span>
                        {label}
                    </h2>
                    <a class="role-back" href="{base}/settings/roles">
                        <i class="ri-arrow-left-s-line" aria-hidden="true"></i>
                        <span>Roles</span>
                    </a>
                </div>

                <!-- The role's key is fixed — owner, manager and staff are
                     written on every operator's record — but what the store
                     calls it is not. -->
                <div class="field role-field">
                    <label for="role-name">Name</label>
                    <input
                        id="role-name"
                        type="text"
                        maxlength="60"
                        placeholder={row ? "e.g. " + role : ""}
                        bind:value={name}
                    />
                </div>

                <div class="field role-field">
                    <label for="role-desc">Description</label>
                    <textarea
                        id="role-desc"
                        rows="3"
                        maxlength="600"
                        placeholder="What this role is for, in your own words"
                        bind:value={about}
                    ></textarea>
                </div>
                <div class="role-field-actions">
                    <button
                        type="button"
                        class="btn sm"
                        disabled={renaming || !labelDirty}
                        onclick={rename}
                    >
                        <span class="txt">{renaming ? "Saving…" : "Save name"}</span>
                    </button>
                    {#if row?.title_customized}
                        <span class="label">Renamed</span>
                    {/if}
                </div>
                <div class="field-help role-field-help">
                    Only the name changes. Operators stay filed under
                    <code>{role}</code>, so renaming moves nobody. Leave a field empty to put the
                    engine's own words back.
                </div>

                {#if row?.configurable && (added.length || removed.length)}
                    <div class="alert info m-b-base">
                        <p>
                            Against what the engine ships for {label}:
                            {#if added.length}<strong>{added.length} added</strong>{/if}{#if added.length && removed.length},
                            {/if}{#if removed.length}<strong>{removed.length} taken away</strong
                                >{/if}.
                            {#if dirty}Not saved yet.{/if}
                        </p>
                    </div>
                {/if}

                <h3 class="perm-heading">Permissions</h3>

                <!-- The column count rides in a custom property so the CSS
                     does not have to hard-code a number the engine owns. -->
                <div class="perm-grid" style="--perm-cols: {columns}">
                    {#each sections as block (block.section)}
                        <!-- The sidebar's own sections, in the sidebar's own
                             order, so "can this person touch Products" is a
                             block to look at rather than rows to gather. -->
                        <div class="perm-section">{block.section}</div>
                        {#each block.rows as group (group.key)}
                            <div class="perm-row">
                            <div class="perm-name">{group.label}</div>
                            {#each slots(group) as item, i (item ? item.right : "gap-" + i)}
                                {#if item}
                                    <button
                                        type="button"
                                        class="perm-btn"
                                        class:on={has(item.right)}
                                        class:locked={locked(item.right)}
                                        class:added={diff(item.right) === "added"}
                                        class:removed={diff(item.right) === "removed"}
                                        aria-pressed={has(item.right)}
                                        disabled={locked(item.right)}
                                        title={buttonTitle(item.right)}
                                        onclick={() => toggle(item.right)}
                                    >
                                        <i
                                            class={has(item.right)
                                                ? "ri-checkbox-circle-fill"
                                                : "ri-checkbox-circle-line"}
                                            aria-hidden="true"
                                        ></i>
                                        <span class="txt">{item.label}</span>
                                    </button>
                                {:else}
                                    <span class="perm-empty" aria-hidden="true"></span>
                                {/if}
                            {/each}
                            </div>
                        {/each}
                    {/each}

                    {#if loading && !matrix}
                        <div class="p-base"><span class="skeleton-loader"></span></div>
                    {:else if matrix && !row}
                        <div class="txt-center txt-hint p-base">
                            This store has no role called “{role}”.
                        </div>
                    {/if}
                </div>
            </section>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt txt-hint">
                    A change lands on the next request the affected operator makes; nobody has to
                    sign in again.
                </span>
            </footer>
        </div>
    </div>
{/if}
