<script>
    /**
     * One role, and what it may do.
     *
     * The rights are drawn as a grid — one row per resource, one pill per verb
     * — because every right the engine has is `resource.verb`, and the flat
     * list of dotted names made "what may this role do to orders" mean finding
     * four rows that happened to share a prefix.
     *
     * Two things about the model shape the page. A role left alone keeps
     * *tracking* the engine's default rather than freezing a copy of it, so
     * "using defaults" and "customised" are worth saying out loud and Reset is
     * a real verb. And a change lands on the affected operator's next request,
     * so nobody is signed out and nobody should be told to sign in again.
     *
     * The grid is built from what the API sends, so a right added to the
     * engine appears here without this file changing.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { roles as rolesApi, auth, can, getRecord } from "$lib/api.js";
    import { rightScope, rightsByResource } from "$lib/rights.js";
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

    const ROLE_LABEL = { owner: "Owner", manager: "Manager", staff: "Staff" };
    const ROLE_BLURB = {
        owner: "Can do everything, including deciding who else can.",
        manager:
            "Runs the shop: the catalogue, the orders, the money going back out. Cannot change the store's configuration or the team, which is what separates running the shop from owning it.",
        staff: "Works the orders. Can see what is being sold and move an order along, and cannot send money out, change prices, or alter who has access.",
    };

    const label = $derived(ROLE_LABEL[role] ?? role);
    const row = $derived(matrix?.roles.find((r) => r.role === role) ?? null);
    const grid = $derived(rightsByResource(matrix?.all_rights ?? []));
    const required = $derived(matrix?.required ?? []);

    $effect(() => {
        // Named so the effect re-runs when the address changes, which is what
        // makes the back-and-forward between two roles reload rather than show
        // the previous one's boxes.
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
            // request() already unwraps the {data} envelope, so there is no second
            // .data to reach through; asking for one silently yields null.
            matrix = await rolesApi.matrix();
            draft = [...(matrix?.roles.find((r) => r.role === role)?.rights ?? [])];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    const has = (right) => draft.includes(right);

    /*
     * The two locks the API also enforces, mirrored here so nothing looks
     * clickable that the server would only refuse. Required rights are the
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
     * the counts move as pills are pressed. Reset is the one control here
     * that changes several rights at once, so what it would do is named on it
     * rather than left to be discovered.
     */
    const defaults = $derived(row?.default ?? []);
    const added = $derived(draft.filter((r) => !defaults.includes(r)));
    const removed = $derived(defaults.filter((r) => !draft.includes(r)));
    const isDefault = $derived(same(draft, defaults));

    /** How one pill departs from the default: "added", "removed" or "". */
    function diff(right) {
        if (!row?.configurable) return "";
        const inDraft = has(right);
        const inDefault = defaults.includes(right);
        if (inDraft && !inDefault) return "added";
        if (!inDraft && inDefault) return "removed";
        return "";
    }

    function pillTitle(right) {
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
        if (!set.role) return;
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
                    <a class="btn secondary" href="{base}/settings/roles">
                        <i class="ri-arrow-left-line" aria-hidden="true"></i>
                        <span class="txt">Roles</span>
                    </a>
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

            <p class="txt-hint m-b-base tw:max-w-[80ch]">
                {ROLE_BLURB[role] ?? ""}
                {#if row && !row.configurable}
                    This role is fixed: it carries every right the engine has, and always will.
                {:else if row}
                    What the engine ships for it is the starting point. Changes here are this
                    store's own, and Reset puts the role back on the shipped set.
                {/if}
            </p>

            {#if row?.configurable && (added.length || removed.length)}
                <div class="alert info m-b-base">
                    <p>
                        Against what the engine ships for {label}:
                        {#if added.length}<strong>{added.length} added</strong>{/if}{#if added.length && removed.length},
                        {/if}{#if removed.length}<strong>{removed.length} taken away</strong>{/if}.
                        {#if dirty}Not saved yet.{/if}
                    </p>
                </div>
            {/if}

            <h6 class="section-title">
                <i class="ri-shield-keyhole-line" aria-hidden="true"></i>
                Rights
            </h6>

            <!-- One row per resource, one pill per verb. A pill is a toggle:
                 pressed means the role holds that right, and the check going
                 green is the only state worth colouring, because "not granted"
                 is the ordinary case on most rows. -->
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table right-grid">
                    <tbody>
                        {#each grid as group (group.key)}
                            <tr>
                                <td class="col-field-name-id" data-name="Area">
                                    <span class="txt-bold">{group.label}</span>
                                </td>
                                <!-- No data-name: the stacked phone layout would
                                     repeat "Rights" above every row of pills, under a
                                     heading that already says it. -->
                                <td>
                                    <div class="flex flex-wrap gap-sm">
                                        {#each group.rights as item (item.right)}
                                            <button
                                                type="button"
                                                class="right-pill"
                                                class:on={has(item.right)}
                                                class:locked={locked(item.right)}
                                                class:added={diff(item.right) === "added"}
                                                class:removed={diff(item.right) === "removed"}
                                                aria-pressed={has(item.right)}
                                                disabled={locked(item.right)}
                                                title={pillTitle(item.right)}
                                                onclick={() => toggle(item.right)}
                                            >
                                                <i
                                                    class={has(item.right)
                                                        ? "ri-checkbox-circle-fill"
                                                        : "ri-checkbox-blank-circle-line"}
                                                    aria-hidden="true"
                                                ></i>
                                                <span class="txt">{item.label}</span>
                                            </button>
                                        {/each}
                                    </div>
                                </td>
                            </tr>
                        {/each}

                        {#if loading && !matrix}
                            <tr><td colspan="2"><span class="skeleton-loader"></span></td></tr>
                        {:else if matrix && !row}
                            <tr>
                                <td colspan="2" class="txt-center txt-hint p-base">
                                    This store has no role called “{role}”.
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
                </span>
            </footer>
        </div>
    </div>
{/if}
