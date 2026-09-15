<script>
    /**
     * What each role may do in this store.
     *
     * The engine ships a default set per role (rights.go) and this screen is
     * the store's departure from it. Two things follow from that and shape the
     * whole page: a role left alone keeps *tracking* the default rather than
     * freezing a copy, so "using defaults" and "customised" are worth saying
     * out loud; and a change lands on the affected operator's next request, so
     * nobody has to be signed out and nobody should be told to sign in again.
     *
     * It is drawn as a matrix — rights down the side, roles across — because
     * that is the question the screen exists to answer. "May staff refund?" and
     * "what has manager got that staff has not?" are each one glance along a
     * line here; as three stacked lists they were a screenful apart and could
     * only be compared from memory.
     *
     * Both axes come from the API, so a right added to the engine appears here
     * without this file changing. The groups below are layout only, and
     * anything they do not name falls through to "Other" rather than
     * disappearing.
     */
    import { roles as rolesApi, auth, can, getRecord, rights as myRights } from "$lib/api.js";
    import { rightScope } from "$lib/rights.js";
    import { toast } from "$lib/toast.svelte.js";
    import DirtyGuard from "$lib/components/DirtyGuard.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import SettingsSidebar from "$lib/components/SettingsSidebar.svelte";

    let loading = $state(true);
    let matrix = $state(null);
    /** role -> the rights as edited here, before saving. */
    let draft = $state({});
    let busy = $state("");

    const me = getRecord();

    /* roles.write is what the settings sidebar gates this link on, and it is the
       only right the screen has: there is no read-only view of the matrix on the
       API. Said again here so a typed address refuses before the request. */
    const allowed = $derived(can("roles.write"));

    const ROLE_LABEL = { owner: "Owner", manager: "Manager", staff: "Staff" };

    /*
     * Layout only, as the header says — the sentence under each right's name
     * comes from rightScope(). This screen used to hold a copy of that table,
     * and the copy had fallen a right behind: store.operate had no sentence
     * and no group, so the right that carries the outbox and the maintenance
     * sweeps rendered under "Other" with a blank help line. A Go test pins
     * rights.js against core/rights.go; a fourth copy here was outside it.
     */
    const GROUPS = [
        { title: "Catalog", rights: ["catalog.read", "catalog.write"] },
        { title: "Inventory", rights: ["inventory.read", "inventory.write"] },
        { title: "Discounts", rights: ["discounts.read", "discounts.write"] },
        { title: "Tax", rights: ["taxes.read", "taxes.write"] },
        { title: "Locations", rights: ["locations.read", "locations.write"] },
        {
            title: "Orders",
            rights: ["orders.read", "orders.write", "orders.fulfill", "orders.refund"],
        },
        { title: "Customers", rights: ["customers.read"] },
        { title: "Team and access", rights: ["team.read", "team.write", "roles.write"] },
        { title: "Data", rights: ["data.export", "data.import"] },
        { title: "Store", rights: ["store.operate"] },
    ];

    /** The groups, filtered to what this engine actually has, plus anything it
     *  has that the list above never heard of. */
    const grouped = $derived.by(() => {
        const all = matrix?.all_rights ?? [];
        const named = new Set(GROUPS.flatMap((g) => g.rights));
        const out = GROUPS.map((g) => ({
            title: g.title,
            rights: g.rights.filter((r) => all.includes(r)),
        })).filter((g) => g.rights.length);
        const rest = all.filter((r) => !named.has(r));
        return rest.length ? [...out, { title: "Other", rights: rest }] : out;
    });

    $effect(() => {
        load();
    });

    async function load() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!allowed) {
            loading = false;
            return;
        }
        loading = true;
        try {
            matrix = unwrap(await rolesApi.matrix());
            draft = {};
            for (const row of matrix.roles) draft[row.role] = [...row.rights];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    const unwrap = (result) => result.data ?? result;
    const rowFor = (role) => matrix?.roles.find((r) => r.role === role);
    const has = (role, right) => (draft[role] ?? []).includes(right);

    /*
     * The two locks the API also enforces, mirrored here so nothing looks
     * clickable that the server would only refuse. Required rights are the
     * floor every role keeps; roles.write is what got you to this screen, and
     * an operator who saves it away from their own role has no way back short
     * of a static admin token.
     */
    const required = $derived(matrix?.required ?? []);
    const locked = (role, right) =>
        required.includes(right) || (role === me?.role && right === "roles.write");

    const lockReason = (role, right) =>
        required.includes(right)
            ? "Every role keeps this: a role without it can sign in and see nothing."
            : "This is your own role — removing this would lock you out of this screen.";

    function toggle(role, right) {
        if (locked(role, right)) return;
        const current = draft[role] ?? [];
        draft[role] = current.includes(right)
            ? current.filter((r) => r !== right)
            : [...current, right];
    }

    const same = (a, b) => {
        if (a.length !== b.length) return false;
        const x = [...a].sort();
        const y = [...b].sort();
        return x.every((v, i) => v === y[i]);
    };
    const dirty = (role) => !same(draft[role] ?? [], rowFor(role)?.rights ?? []);

    /*
     * Whether ANY role has unticked or newly ticked boxes waiting.
     *
     * The screen used to render a bare "unsaved" chip and nothing else: no
     * warning on leaving, no shortcut, no way back. Each role saves on its own
     * button, so there is no single Save for a SaveBar to carry — but the thing
     * a SaveBar is really for, not throwing the work away silently, does not
     * depend on there being one.
     */
    const anyDirty = $derived(
        (matrix?.roles ?? []).some((row) => row.configurable && dirty(row.role)),
    );

    /** Whether the draft already matches the engine's defaults, which is what
     *  "reset" would produce — so the button has nothing left to offer. */
    const isDefault = (role) => same(draft[role] ?? [], rowFor(role)?.default ?? []);

    /*
     * The departure from the shipped default.
     *
     * `default` was fetched and used for nothing but disabling the Reset
     * button, which meant an operator could be told a role was "customised"
     * and never told how — and could press Reset without being able to see
     * what it would do. That is the one control on this screen that changes
     * several rights at once.
     *
     * Measured against the DRAFT, not against the saved rights, so the marks
     * move as boxes are ticked. While a role is dirty the header carries an
     * "unsaved" flag beside these numbers, which is what keeps "using
     * defaults" (saved state) and a non-zero count (on-screen state) from
     * reading as a contradiction.
     */
    const defaultsFor = (role) => rowFor(role)?.default ?? [];
    const added = (role) => (draft[role] ?? []).filter((r) => !defaultsFor(role).includes(r));
    const removed = (role) => defaultsFor(role).filter((r) => !(draft[role] ?? []).includes(r));

    /**
     * How one cell departs from the default: "added", "removed" or "".
     *
     * Always "" for a role the store cannot configure. Owner carries every
     * right by construction, so a diff against its default is noise on twenty
     * cells that can never move.
     */
    function cellDiff(role, right) {
        const row = rowFor(role);
        if (!row?.configurable) return "";
        const inDraft = has(role, right);
        const inDefault = (row.default ?? []).includes(right);
        if (inDraft && !inDefault) return "added";
        if (!inDraft && inDefault) return "removed";
        return "";
    }

    /* The lock reason wins when there is one: a cell that cannot be changed
       needs to say why before it says how it differs. */
    function cellTitle(role, right) {
        const row = rowFor(role);
        if (row?.configurable && locked(role, right)) return lockReason(role, right);
        switch (cellDiff(role, right)) {
            case "added":
                return `${right} is not in what the engine ships for ${ROLE_LABEL[role] ?? role}.`;
            case "removed":
                return (
                    `${right} is in what the engine ships for ${ROLE_LABEL[role] ?? role}` +
                    " and has been taken away."
                );
            default:
                return null;
        }
    }

    /** What Reset would actually do, named right by right, on the control that
     *  would do it. */
    function resetTitle(role) {
        const gained = added(role);
        const lost = removed(role);
        if (!gained.length && !lost.length) {
            return `${ROLE_LABEL[role] ?? role} already matches what the engine ships for it.`;
        }
        const parts = [];
        if (gained.length) parts.push("take away " + gained.join(", "));
        if (lost.length) parts.push("give back " + lost.join(", "));
        return "Reset would " + parts.join(", and ") + ".";
    }

    /** The same comparison as a sentence, for the counts in the header. */
    function diffTitle(role) {
        const parts = [];
        if (added(role).length) parts.push("added " + added(role).join(", "));
        if (removed(role).length) parts.push("removed " + removed(role).join(", "));
        if (!parts.length) return null;
        const name = ROLE_LABEL[role] ?? role;
        return `Against what the engine ships for ${name}: ${parts.join("; ")}.`;
    }

    async function save(role) {
        if (busy) return;
        busy = role;
        try {
            applySaved(unwrap(await rolesApi.save(role, draft[role] ?? [])));
            toast.success("Saved the " + (ROLE_LABEL[role] ?? role) + " role");
            await refreshMeIfAffected(role);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    async function reset(role) {
        if (busy) return;
        busy = role;
        try {
            applySaved(unwrap(await rolesApi.reset(role)));
            toast.success((ROLE_LABEL[role] ?? role) + " is back on the defaults");
            await refreshMeIfAffected(role);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    function applySaved(set) {
        matrix.roles = matrix.roles.map((r) => (r.role === set.role ? set : r));
        draft[set.role] = [...set.rights];
    }

    /*
     * The panel hides what it cannot do from the record it stored at sign-in.
     * Changing your own role's rights makes that record wrong, and the symptom
     * is a nav offering a screen the engine will refuse — so re-read it.
     */
    async function refreshMeIfAffected(role) {
        if (me?.role !== role) return;
        try {
            await auth.refresh();
        } catch {
            /* the next request corrects it; nothing here is worth a toast */
        }
    }
</script>

<svelte:head><title>Roles · GoCommerce</title></svelte:head>

{#if !allowed}
    <NoAccess right="roles.write" what="the role matrix" />
{:else}
<DirtyGuard
    dirty={anyDirty}
    message="A role has rights ticked that have not been saved. Leave and lose them?"
/>

<div class="page page-roles shopify-skin">
    <SettingsSidebar />

    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Roles</div>
            </nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    onclick={load}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>
        </header>

        {#if loading}
            <div class="block txt-center"><span class="loader lg"></span></div>
        {:else if matrix}
            <div class="roles-intro field-help">
                What each role may do in this store. A change applies on the next request somebody
                makes — nobody has to sign out. A role left on its defaults keeps tracking them, so
                a right added to it in a later release arrives on its own. Owner is fixed and
                always carries every right, so a store that has narrowed itself too far still has
                a way back.
            </div>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table roles-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-right">Right</th>
                            {#each matrix.roles as row (row.role)}
                                {@const gained = added(row.role)}
                                {@const lost = removed(row.role)}
                                <th class="col-role" class:mine={row.role === me?.role}>
                                    <span class="role-name">
                                        {ROLE_LABEL[row.role] ?? row.role}
                                        {#if row.role === me?.role}
                                            <span class="label sm">you</span>
                                        {/if}
                                    </span>
                                    <span class="role-state">
                                        {#if !row.configurable}
                                            <i class="ri-lock-line" aria-hidden="true"></i>
                                            <span>fixed</span>
                                        {:else if row.customized}
                                            <span class="label warning">customised</span>
                                        {:else}
                                            <span>using defaults</span>
                                        {/if}
                                    </span>
                                    <span class="role-count">
                                        {(draft[row.role] ?? []).length} of {matrix.all_rights.length}
                                    </span>
                                    {#if row.configurable && (gained.length || lost.length)}
                                        <span class="role-diff" title={diffTitle(row.role)}>
                                            {#if gained.length}
                                                <span class="added">{gained.length} added</span>
                                            {/if}
                                            {#if lost.length}
                                                <span class="removed">{lost.length} removed</span>
                                            {/if}
                                        </span>
                                    {/if}
                                    {#if row.configurable}
                                        <span class="role-actions">
                                            <button
                                                type="button"
                                                class="btn sm"
                                                disabled={busy === row.role || !dirty(row.role)}
                                                onclick={() => save(row.role)}
                                            >
                                                <span class="txt">Save</span>
                                            </button>
                                            <button
                                                type="button"
                                                class="btn sm transparent secondary"
                                                title={resetTitle(row.role)}
                                                disabled={busy === row.role ||
                                                    (!row.customized && isDefault(row.role))}
                                                onclick={() => reset(row.role)}
                                            >
                                                <span class="txt">Reset</span>
                                            </button>
                                        </span>
                                        {#if dirty(row.role)}
                                            <span class="role-dirty">unsaved</span>
                                        {/if}
                                    {/if}
                                </th>
                            {/each}
                        </tr>
                    </thead>

                    <tbody>
                        {#each grouped as group (group.title)}
                            <tr class="group-row">
                                <th colspan={matrix.roles.length + 1} scope="colgroup">
                                    {group.title}
                                </th>
                            </tr>
                            {#each group.rights as right (right)}
                                <tr>
                                    <td class="col-right">
                                        <code class="right-name">{right}</code>
                                        <span class="right-help">{rightScope(right)}</span>
                                    </td>
                                    {#each matrix.roles as row (row.role)}
                                        {@const diff = cellDiff(row.role, right)}
                                        <td
                                            class="col-role"
                                            class:mine={row.role === me?.role}
                                            class:diff-added={diff === "added"}
                                            class:diff-removed={diff === "removed"}
                                            title={cellTitle(row.role, right)}
                                        >
                                            <div class="field">
                                                <input
                                                    type="checkbox"
                                                    id="{row.role}-{right}"
                                                    checked={row.configurable
                                                        ? has(row.role, right)
                                                        : true}
                                                    disabled={!row.configurable ||
                                                        locked(row.role, right) ||
                                                        busy === row.role}
                                                    onchange={() => toggle(row.role, right)}
                                                />
                                                <!-- The label is the visible control: PocketBase
                                                     hides the box and draws it here, so the text
                                                     that names the cell is for a screen reader. -->
                                                <label for="{row.role}-{right}">
                                                    <span class="cell-name">
                                                        {right} for {ROLE_LABEL[row.role] ?? row.role}
                                                        {#if diff === "added"}
                                                            — added to the default
                                                        {:else if diff === "removed"}
                                                            — taken away from the default
                                                        {/if}
                                                    </span>
                                                </label>
                                            </div>
                                            <!-- Shape as well as colour: the two states have to
                                                 be told apart without relying on green and red,
                                                 and the sentence above carries them for a screen
                                                 reader, so this glyph is decoration. -->
                                            {#if diff}
                                                <span class="diff-mark" aria-hidden="true">
                                                    {diff === "added" ? "+" : "−"}
                                                </span>
                                            {/if}
                                        </td>
                                    {/each}
                                </tr>
                            {/each}
                        {/each}
                    </tbody>

                </table>
            </div>

            <div class="roles-intro field-help">
                <span class="diff-mark added" aria-hidden="true">+</span>
                marks a right this store has added to what the engine ships for that role;
                <span class="diff-mark removed" aria-hidden="true">−</span>
                marks one it has taken away. Reset puts a role back to the shipped set — hover it
                to see exactly which rights that would move.
            </div>

            {#if !myRights()}
                <div class="roles-intro field-help">
                    You are signed in with a static admin token, which carries every right and has
                    no role. Nothing here applies to it.
                </div>
            {/if}
        {/if}

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
        </footer>
    </div>
</div>

{/if}
