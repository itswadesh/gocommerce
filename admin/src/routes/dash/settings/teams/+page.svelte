<script>
    /**
     * Managing the people who can sign in to this panel — PocketBase's
     * `_superusers` collection, in the shape this engine needs.
     *
     * The password field is write-only in both directions: the API never sends
     * a hash back, and this form never shows one. Changing a password signs
     * that operator out everywhere, which is stated on the form rather than
     * discovered afterwards.
     */
    import { auth, can, getRecord, roles as rolesApi } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { pageSlice } from "$lib/clientpage.js";
    import { selection } from "$lib/selection.svelte.js";
    import { runBulk } from "$lib/bulk.js";
    import { formatDate, relativeTime, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import DirtyGuard from "$lib/components/DirtyGuard.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import Confirm from "$lib/components/Confirm.svelte";

    /*
     * team.read is the sidebar's gate on this screen; team.write is what every
     * control on it needs. They are separate rights (rights.go), and the panel
     * used to honour neither here: a team.read-only operator got a fully armed
     * screen — Invite, Create directly, the inline role picker, sign out
     * everywhere, Remove, Revoke — where every single button answered 403.
     *
     * The engine refuses by name, so the screen can say the same thing before
     * the request instead of after it.
     */
    const readable = $derived(can("team.read"));
    const writable = $derived(can("team.write"));

    let loading = $state(true);
    let saving = $state(false);
    let superusers = $state([]);
    let invitations = $state([]);

    let inviteOpen = $state(false);
    /*
     * Dokploy's "Add Invitation": how they arrive first — a link emailed to
     * them, or a password set now — then the email and the role. There is no
     * email-provider picker: the link goes out by the store's own provider
     * (Resend, where it is installed).
     */
    let invite = $state({ method: "link", email: "", role: "staff", password: "" });
    let inviting = $state(false);
    /** The invitation a row action is working on, so its own row shows it. */
    let busyInvite = $state("");
    // The invitation just created, while the drawer that reports it is open:
    // whether it was emailed, and the link to copy either way.
    let issued = $state(null);
    let copied = $state(false);
    /** Accepted and revoked invitations are history; shown on request. */
    let showAllInvites = $state(false);

    const INVITE_METHODS = [
        { value: "link", label: "Invitation link — emailed to them" },
        { value: "password", label: "Set a password now" },
    ];

    let editorOpen = $state(false);
    let editing = $state(null); // null = creating
    let form = $state({ email: "", password: "", role: "staff" });

    /*
     * The store's roles, from the store (D80). A store names and makes its own
     * now, so a list compiled into the panel would offer roles that do not
     * exist and miss the ones that do. Each option carries the store's own
     * sentence for the role, because the picker is where an operator decides
     * how much of the store somebody gets. Vendor is left out: a vendor's
     * login is made from the vendor's page, which knows whose it is.
     */
    let roleNames = $state([]);
    const roleOptions = $derived(
        roleNames
            .filter((n) => n.role !== "vendor")
            .map((n) => ({
                value: n.role,
                short: n.title,
                label: n.description ? `${n.title} — ${n.description}` : n.title,
            })),
    );
    const roleName = (role) => roleNames.find((n) => n.role === role)?.title ?? role;
    let errors = $state({});

    let confirmOpen = $state(false);
    let pendingDelete = $state(null);

    const me = getRecord();

    $effect(() => {
        load();
    });

    async function load() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const [people, invites, names] = await Promise.all([
                auth.list(),
                auth.invitations(),
                rolesApi.names(),
            ]);
            superusers = people.data;
            invitations = invites.data ?? [];
            roleNames = names ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /** Outstanding ones: waiting, or lapsed and worth resending. Accepted and
     *  revoked invitations are history. */
    const outstanding = $derived(
        invitations.filter((i) => i.status === "pending" || i.status === "expired"),
    );
    const listedInvites = $derived(showAllInvites ? invitations : outstanding);
    const STATUS_TONE = { pending: "info", accepted: "success", expired: "warning", revoked: "" };

    /* Summed over the rows rather than asked for: the listing already carries
       every count, and a second request for a total nobody can act on
       separately would only be another thing to get out of step. */
    const signedIn = $derived(superusers.reduce((n, su) => n + (su.sessions ?? 0), 0));

    // --------------------------------------------------------------- paging

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the page. */
    const PER_PAGE = 25;

    const list = listState({ page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    /*
     * The page is cut here rather than asked for, and that is the engine's
     * shape rather than a shortcut: GET /api/admin/superusers takes no page
     * parameter, its SQL has no LIMIT, and it reports
     * ListMeta{Total: len(list), Limit: len(list)} (core/superusers_http.go) —
     * the whole table, every time. A shop that has taken on forty people over
     * three years still has to be able to reach the fortieth, and the footer
     * counted them all while the table showed whatever fitted.
     */
    const paged = $derived(pageSlice(superusers, { page: list.page, limit: perPage }));

    // ------------------------------------------------------------ selection

    /*
     * Two selections, and only ever one of them holding anything.
     *
     * The people and the outstanding invitations are different kinds of row
     * with different actions, and there is one bulk bar. Ticking in either list
     * clears the other, so what the bar is offering is never in doubt — the
     * alternative is two sticky bars stacked over one footer, each describing
     * half of a selection.
     */
    const selPeople = selection();
    const selInvites = selection();

    const pickPerson = (id) => (selInvites.clear(), selPeople.toggle(id));
    const pickAllPeople = (rows) => (selInvites.clear(), selPeople.toggleAll(rows));
    const pickInvite = (id) => (selPeople.clear(), selInvites.toggle(id));
    const pickAllInvites = (rows) => (selPeople.clear(), selInvites.toggleAll(rows));

    let bulkBusy = $state(false);
    let bulkRemoveOpen = $state(false);

    const pickedPeople = $derived(selPeople.pick(superusers));
    const pickedInvites = $derived(selInvites.pick(outstanding));

    /*
     * Never yourself, in either action.
     *
     * "Sign out everywhere" includes the browser it is pressed in (the engine
     * is explicit about that — see handleRevokeMySessions), and removing your
     * own account ends the session mid-run, so every call after it would 401
     * and the operator would be looking at a login form with no idea how much
     * of their selection had gone through. Your own row keeps both buttons,
     * where it is one deliberate act rather than a side effect of a tick.
     */
    const actionable = $derived(pickedPeople.filter((su) => !(me && su.id === me.id)));
    const includesMe = $derived(pickedPeople.length !== actionable.length);

    async function runOver(rows, fn, describe, noun = "superuser") {
        if (!rows.length) return;
        bulkBusy = true;
        try {
            await runBulk(rows, fn, { describe, noun, label: (row) => row.email });
        } finally {
            bulkBusy = false;
        }
        selPeople.clear();
        selInvites.clear();
        await load();
    }

    const bulkSignOut = () =>
        runOver(actionable, (su) => auth.revokeSessions(su.id), "Signed out everywhere");

    const bulkRemove = () => runOver(actionable, (su) => auth.remove(su.id), "Removed");

    /*
     * Revoking is the only bulk action an invitation has. Resending cancels
     * the link it replaces, which is a decision about one person at a time.
     */
    const bulkRevoke = () =>
        runOver(
            pickedInvites,
            (inv) => auth.revokeInvitation(inv.id),
            "Revoked",
            "invitation",
        );

    // ----------------------------------------------------------- dirty state

    /*
     * What has been typed into the two drawers and not saved.
     *
     * Both used to be thrown away in silence by Escape, a click on the dimmed
     * page, or any navigation off the screen — and the shortest route out of a
     * half-filled form was the one that said nothing. `role` counts as typing
     * only while creating, because it is the field that is not there when
     * editing somebody.
     */
    const editorDirty = $derived(
        editorOpen &&
            (form.email.trim() !== (editing ? editing.email : "") ||
                form.password !== "" ||
                (!editing && form.role !== "staff")),
    );

    const inviteDirty = $derived(
        inviteOpen &&
            !issued &&
            (invite.email.trim() !== "" || invite.password !== "" || invite.role !== defaultRole),
    );

    const anyDirty = $derived(editorDirty || inviteDirty);

    /*
     * Adding somebody. An address that already has an account elsewhere (D83)
     * joins straight away by either method and keeps the password it has.
     */
    async function submitInvite(event) {
        event?.preventDefault();
        if (inviting) return;
        errors = {};
        const email = invite.email.trim().toLowerCase();
        if (!email) {
            errors.invite = "An email is required.";
            return;
        }
        if (superusers.some((su) => su.email.toLowerCase() === email)) {
            errors.invite = `${email} is already on the team.`;
            return;
        }
        if (!roleOptions.some((r) => r.value === invite.role)) {
            errors.invite = "Choose a role.";
            return;
        }
        if (invite.method === "password" && invite.password.length < 8) {
            errors.password = "A password of at least 8 characters is required.";
            return;
        }
        inviting = true;
        try {
            if (invite.method === "password") {
                await auth.create(email, invite.password, invite.role);
                toast.success(`${email} added to the team`);
                closeInvite({ deliberate: true });
            } else {
                const result = await auth.invite(email, invite.role, true);
                issued = result.data ?? result;
                copied = false;
            }
            await load();
        } catch (err) {
            errors.invite = err.message;
        } finally {
            inviting = false;
        }
    }

    /* Staff by default, the least a new person can be given; a store that
       deleted Staff gets its first role short of owner instead. */
    const defaultRole = $derived(
        roleOptions.find((r) => r.value === "staff")?.value ??
            roleOptions.find((r) => r.value !== "owner")?.value ??
            "",
    );

    function openInvite() {
        invite = { method: "link", email: "", role: defaultRole, password: "" };
        issued = null;
        copied = false;
        errors = {};
        inviteOpen = true;
    }

    async function copyLink() {
        try {
            await navigator.clipboard.writeText(issued.accept_url);
            copied = true;
        } catch {
            // Clipboard access is refused in plenty of ordinary situations —
            // an insecure origin, a browser setting. The link is on screen and
            // selectable, so say that rather than pretending it worked.
            toast.info("Copy it from the box above");
        }
    }

    async function clipboard(text) {
        try {
            await navigator.clipboard.writeText(text);
            return true;
        } catch {
            return false;
        }
    }

    /**
     * Copy a fresh link for an invitation. The store keeps only a link's
     * hash, so the old one cannot be shown again: this issues a new one, and
     * the old one stops working. Nothing is emailed.
     */
    async function copyInviteLink(inv) {
        if (busyInvite) return;
        busyInvite = inv.id;
        try {
            const result = await auth.invite(inv.email, inv.role, false);
            const fresh = result.data ?? result;
            if (await clipboard(fresh.accept_url)) {
                toast.success(`New link for ${inv.email} copied — the old one no longer works`);
            } else {
                issued = fresh;
                invite = { method: "link", email: inv.email, role: inv.role, password: "" };
                inviteOpen = true;
            }
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            busyInvite = "";
        }
    }

    /** Email the invitation again, with a new link; the old one stops working. */
    async function resendInvite(inv) {
        if (busyInvite) return;
        busyInvite = inv.id;
        try {
            const result = await auth.invite(inv.email, inv.role, true);
            const fresh = result.data ?? result;
            if (fresh.emailed) {
                toast.success(`Invitation emailed again to ${inv.email}`);
            } else {
                // Not sent: show why, with the link to copy instead.
                issued = fresh;
                copied = false;
                invite = { method: "link", email: inv.email, role: inv.role, password: "" };
                inviteOpen = true;
            }
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            busyInvite = "";
        }
    }

    async function revokeInvite(inv) {
        try {
            await auth.revokeInvitation(inv.id);
            toast.success(`Invitation to ${inv.email} revoked`);
            await load();
        } catch (err) {
            toast.error(err);
        }
    }

    /**
     * A half-typed invitation is worth one question. Cancel passes
     * `deliberate` and is never asked; Escape, the backdrop and the header's X
     * arrive here saying nothing. A sent invitation has nothing to lose — its
     * link can be copied again from the list.
     */
    function closeInvite({ deliberate = false } = {}) {
        if (!deliberate && inviteDirty && !window.confirm(DISCARD)) return;
        inviteOpen = false;
    }

    /** One sentence for both drawers, so the question reads the same whichever
     *  one is open. */
    const DISCARD = "You have unsaved changes. Close this and lose them?";

    /** The editor's own dismissal, guarded the same way. The footer's Cancel
     *  closes it outright, because that button already says what it is for. */
    function closeEditor() {
        if (editorDirty && !window.confirm(DISCARD)) return;
        editorOpen = false;
    }

    async function signOutEverywhere(su, event) {
        event.stopPropagation();
        try {
            const result = await auth.revokeSessions(su.id);
            const n = result.revoked ?? 0;
            toast.success(
                n === 0
                    ? `${su.email} was not signed in anywhere`
                    : `Signed ${su.email} out of ${n} ${n === 1 ? "device" : "devices"}`,
            );
        } catch (err) {
            toast.error(err);
        }
    }

/**
     * Changing a role is its own request, and its own confirmation.
     *
     * The engine refuses to leave the store without an owner, so the failure
     * that matters is already handled there — this just carries the reason back
     * and puts the picker where it was.
     */
    async function changeRole(su, role) {
        if (role === su.role) return;
        const was = su.role;
        su.role = role;
        try {
            await auth.setRole(su.id, role);
            toast.success(`${su.email} is now ${roleName(role).toLowerCase()}`);
            // Their own rights just changed: re-read the record so the nav and
            // the buttons match what the engine will now allow. The record is a
            // rune, so writing it is enough — this used to reload the document
            // because can() read localStorage and nothing was watching it.
            if (me && su.id === me.id) {
                await auth.refresh();
            }
        } catch (err) {
            su.role = was;
            superusers = [...superusers];
            toast.error(err);
        }
    }

    function openEdit(su) {
        editing = su;
        form = { email: su.email, password: "", role: su.role };
        errors = {};
        editorOpen = true;
    }

    async function save(event) {
        event?.preventDefault();
        if (saving) return;

        errors = {};
        if (!form.email.trim()) errors.email = "An email is required.";
        if (!editing && form.password.length < 8) {
            errors.password = "A password of at least 8 characters is required.";
        }
        if (editing && form.password && form.password.length < 8) {
            errors.password = "A password must be at least 8 characters.";
        }
        /*
         * POST /api/admin/superusers turns an omitted or empty role into
         * OWNER — the opposite of what an invitation with no role defaults to.
         * So the create path refuses to send one it cannot name, rather than
         * letting a blank travel and be read as the most powerful answer.
         */
        if (!editing && !roleOptions.some((r) => r.value === form.role)) {
            errors.role = "Choose a role.";
        }
        if (Object.keys(errors).length) return;

        saving = true;
        try {
            if (editing) {
                const body = {};
                if (form.email.trim() !== editing.email) body.email = form.email.trim();
                if (form.password) body.password = form.password;
                if (!Object.keys(body).length) {
                    editorOpen = false;
                    return;
                }
                await auth.update(editing.id, body);
                toast.success(
                    form.password
                        ? "Password changed — their other sessions were signed out"
                        : "Superuser saved",
                );
            } else {
                await auth.create(form.email.trim(), form.password, form.role);
                toast.success("Superuser created");
            }
            editorOpen = false;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function askDelete(su, event) {
        event.stopPropagation();
        pendingDelete = su;
        confirmOpen = true;
    }

    async function doDelete() {
        try {
            await auth.remove(pendingDelete.id);
            toast.success(`Removed ${pendingDelete.email}`);
            await load();
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>Team · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="team.read" what="the team" />
{:else}
<!-- The drawers are guarded against dismissal on their own; this is the other
     half — the breadcrumb, the settings rail and anything else that navigates
     away while a form is half filled in. -->
<DirtyGuard
    dirty={anyDirty}
    message="You have started {inviteDirty ? 'an invitation' : 'a superuser'} and not saved it. Leave and lose it?"
/>

<div class="page page-superusers shopify-skin">

    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Superusers</div>
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

            <div class="page-header-primary-btns">
                <!-- One entry point, Dokploy's "Add Invitation": how the
                     person arrives, their email and their role, in one form. -->
                {#if writable}
                    <button
                        type="button"
                        class="btn"
                        aria-label="Invite member"
                        title="Invite member"
                        onclick={openInvite}
                    >
                        <i class="ri-user-add-line" aria-hidden="true"></i>
                        <span class="txt">Invite member</span>
                    </button>
                {/if}
            </div>
        </header>

        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        {#if writable}
                            <th class="col-bulk-select min-width">
                                <div class="field">
                                    <input
                                        id="select-all-superusers"
                                        type="checkbox"
                                        checked={selPeople.allSelected(paged.rows)}
                                        onchange={() => pickAllPeople(paged.rows)}
                                    />
                                    <label
                                        for="select-all-superusers"
                                        aria-label="Select everyone on this page"
                                    ></label>
                                </div>
                            </th>
                        {/if}
                        <th class="col-field-name-id">Email</th>
                        <th class="col-field-type-select">Role</th>
                        <th class="col-field-type-number min-width">Sessions</th>
                        <th class="col-field-type-date">Created</th>
                        <th class="col-field-type-date">Updated</th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each paged.rows as su (su.id)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={() => openEdit(su)}
                            onkeydown={(e) => rowKey(e, () => openEdit(su))}
                        >
                            {#if writable}
                                <!-- stopPropagation rather than a guard inside
                                     the row handler: ticking a box must not also
                                     open the editor. -->
                                <td
                                    class="col-bulk-select min-width"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <div class="field">
                                        <input
                                            id="select-superuser-{su.id}"
                                            type="checkbox"
                                            checked={selPeople.has(su.id)}
                                            onchange={() => pickPerson(su.id)}
                                        />
                                        <label
                                            for="select-superuser-{su.id}"
                                            aria-label="Select {su.email}"
                                        ></label>
                                    </div>
                                </td>
                            {/if}
                            <td class="col-field-name-id" data-name="Email">
                                <span class="txt-bold">{su.email}</span>
                                {#if me && su.id === me.id}
                                    <span class="label sm">you</span>
                                {/if}
                            </td>
                            <!-- The picker is the control, not a link into the
                                 editor: a role is one choice from three, and
                                 opening a drawer to make it would be a step for
                                 nothing. stopPropagation because the row itself
                                 opens the editor. -->
                            <td
                                class="col-field-type-select"
                                data-name="Role"
                                onclick={(e) => e.stopPropagation()}
                            >
                                {#if writable}
                                    <div class="field">
                                        <Select
                                            id="role-{su.id}"
                                            ariaLabel="Role for {su.email}"
                                            value={su.role}
                                            onchange={(role) => changeRole(su, role)}
                                            options={roleOptions}
                                        />
                                    </div>
                                {:else}
                                    <!-- Still shown, because reading who holds
                                         which role is exactly what team.read
                                         is for; only the changing of it goes. -->
                                    <span class="label">{roleName(su.role)}</span>
                                {/if}
                            </td>
                            <td class="col-field-type-number min-width" data-name="Sessions">
                                {#if su.sessions}
                                    <span class="label success">{su.sessions}</span>
                                    <span class="txt-hint txt-sm">
                                        {relativeTime(su.newest_session)}
                                    </span>
                                {:else}
                                    <span class="txt-hint">—</span>
                                {/if}
                            </td>
                            <!-- Relative, with the timestamp as the tooltip, which
                                 is what Orders does with the same column. Two
                                 absolute timestamps side by side answered a
                                 question nobody asks — what matters here is
                                 "recently" or "ages ago", and the exact minute is
                                 one hover away. They were also tight enough to
                                 clip: "Sep 13, 2026, 11:47 AM" measured 142px in a
                                 142px box, so a longer month or a two-digit hour
                                 lost its last glyph. -->
                            <td
                                class="col-field-type-date txt-hint txt-sm"
                                data-name="Created"
                                title={formatDate(su.created_at)}
                            >
                                {relativeTime(su.created_at)}
                            </td>
                            <td
                                class="col-field-type-date txt-hint txt-sm"
                                data-name="Updated"
                                title={formatDate(su.updated_at)}
                            >
                                {relativeTime(su.updated_at)}
                            </td>
                            <td class="col-meta min-width">
                                <!-- Enabled whatever the count says: a number
                                     read a few minutes ago must never disable a
                                     security control. The title is what changes. -->
                                {#if writable}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        aria-label="Sign {su.email} out everywhere"
                                        title={su.sessions
                                            ? "Sign out everywhere"
                                            : "Not signed in anywhere"}
                                        onclick={(e) => signOutEverywhere(su, e)}
                                    >
                                        <i class="ri-logout-circle-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                {#if writable && superusers.length > 1}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary row-delete"
                                        aria-label="Remove {su.email}"
                                        title="Remove"
                                        onclick={(e) => askDelete(su, e)}
                                    >
                                        <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !superusers.length}
                        {#each Array(3) as _, i (i)}
                            <tr><td colspan={writable ? 7 : 6}><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {/if}
                </tbody>
            </table>
        </div>

        <!-- The column must not be read as a last sign-in, because the data
             cannot support that: expired sessions are deleted, so a dash means
             nobody is signed in at the moment. -->
        <div class="field-help">
            Sessions are the ones open right now. They end on sign-out, on a password change, and
            on their own after 14 days — so a dash means nobody is signed in at the moment, not
            that nobody ever has been.
        </div>

        {#if invitations.length}
            <!-- Below the team rather than beside it: these are people who are
                 not here yet, and mixing them into the list would say they are.
                 Laid out as Dokploy's Invitations card: email, role, status,
                 when it expires, and what can be done with it. -->
            <section class="m-t-base invitations" aria-labelledby="invitations-title">
                <h2
                    id="invitations-title"
                    class="tw:mt-6 tw:mb-3 tw:flex tw:flex-wrap tw:items-center tw:gap-2 tw:text-sm tw:font-semibold"
                >
                    Invitations
                    {#if writable && outstanding.length > 1}
                        <button
                            type="button"
                            class="btn sm transparent secondary"
                            onclick={() => pickAllInvites(outstanding)}
                        >
                            <span class="txt">
                                {selInvites.allSelected(outstanding) ? "Select none" : "Select all"}
                            </span>
                        </button>
                    {/if}
                    {#if invitations.length > outstanding.length}
                        <button
                            type="button"
                            class="btn sm transparent secondary m-l-auto"
                            aria-pressed={showAllInvites}
                            onclick={() => (showAllInvites = !showAllInvites)}
                        >
                            <span class="txt">{showAllInvites ? "Waiting only" : "Show all"}</span>
                        </button>
                    {/if}
                </h2>
                {#if listedInvites.length}
                    <div class="page-table-wrapper tw:rounded-xl tw:border">
                        <table class="table responsive-table">
                            <thead>
                                <tr>
                                    {#if writable}<th class="col-bulk-select min-width"></th>{/if}
                                    <th class="col-field-name-id">Email</th>
                                    <th class="col-field-type-select">Role</th>
                                    <th class="col-field-type-select">Status</th>
                                    <th class="col-field-type-date">Expires</th>
                                    <th class="col-meta min-width">Actions</th>
                                </tr>
                            </thead>
                            <tbody>
                                {#each listedInvites as inv (inv.id)}
                                    {@const open = inv.status === "pending" || inv.status === "expired"}
                                    <tr class="invite-row" class:busy={busyInvite === inv.id}>
                                        {#if writable}
                                            <td class="col-bulk-select min-width">
                                                {#if open}
                                                    <div class="field invite-select">
                                                        <input
                                                            id="select-invite-{inv.id}"
                                                            type="checkbox"
                                                            checked={selInvites.has(inv.id)}
                                                            onchange={() => pickInvite(inv.id)}
                                                        />
                                                        <label
                                                            for="select-invite-{inv.id}"
                                                            aria-label="Select the invitation to {inv.email}"
                                                        ></label>
                                                    </div>
                                                {/if}
                                            </td>
                                        {/if}
                                        <td class="col-field-name-id" data-name="Email">
                                            <span class="txt">{inv.email}</span>
                                            {#if inv.invited_by}
                                                <div class="txt-hint txt-sm">invited by {inv.invited_by}</div>
                                            {/if}
                                        </td>
                                        <td class="col-field-type-select" data-name="Role">
                                            <span class="label">{roleName(inv.role)}</span>
                                        </td>
                                        <td class="col-field-type-select" data-name="Status">
                                            <span class="label {STATUS_TONE[inv.status] ?? ''}">{inv.status}</span>
                                        </td>
                                        <td
                                            class="col-field-type-date"
                                            data-name="Expires"
                                            title={formatDate(inv.expires_at)}
                                        >
                                            {#if inv.status === "accepted"}
                                                <span class="txt-hint">joined {relativeTime(inv.accepted_at)}</span>
                                            {:else if inv.status === "revoked"}
                                                <span class="txt-hint">revoked {relativeTime(inv.revoked_at)}</span>
                                            {:else}
                                                {formatDate(inv.expires_at)}
                                                {#if inv.status === "expired"}<span class="txt-hint">(expired)</span>{/if}
                                            {/if}
                                        </td>
                                        <td class="col-meta min-width" data-name="Actions">
                                            {#if writable && open}
                                                <div class="inline-flex gap-5">
                                                    <button
                                                        type="button"
                                                        class="btn circle sm transparent secondary"
                                                        class:loading={busyInvite === inv.id}
                                                        disabled={!!busyInvite}
                                                        aria-label="Copy a new link for {inv.email}"
                                                        title="Copy link — issues a new one; the old link stops working"
                                                        onclick={() => copyInviteLink(inv)}
                                                    >
                                                        <i class="ri-links-line" aria-hidden="true"></i>
                                                    </button>
                                                    <button
                                                        type="button"
                                                        class="btn circle sm transparent secondary"
                                                        disabled={!!busyInvite}
                                                        aria-label="Email the invitation to {inv.email} again"
                                                        title="Resend email — with a new link"
                                                        onclick={() => resendInvite(inv)}
                                                    >
                                                        <i class="ri-mail-send-line" aria-hidden="true"></i>
                                                    </button>
                                                    <button
                                                        type="button"
                                                        class="btn circle sm transparent secondary"
                                                        disabled={!!busyInvite}
                                                        aria-label="Revoke the invitation to {inv.email}"
                                                        title="Revoke"
                                                        onclick={() => revokeInvite(inv)}
                                                    >
                                                        <i class="ri-close-line" aria-hidden="true"></i>
                                                    </button>
                                                </div>
                                            {/if}
                                        </td>
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    </div>
                {:else}
                    <p class="txt-hint txt-sm">Nobody is waiting to join. Show all to see past invitations.</p>
                {/if}
            </section>
        {/if}

        {#if writable}
            <!-- One bar, because only one of the two selections can be holding
                 anything. Each button says how many of the selection it can act
                 on, since your own account is excluded from both. -->
            {#if selPeople.count}
                <BulkBar
                    count={selPeople.count}
                    noun="superuser"
                    onclear={() => selPeople.clear()}
                >
                    <button
                        type="button"
                        class="btn sm secondary"
                        disabled={bulkBusy || !actionable.length}
                        title={includesMe
                            ? "Your own sessions are not ended here — signing yourself out everywhere takes this browser with it, and the button on your own row says so"
                            : "End every session these operators hold"}
                        onclick={bulkSignOut}
                    >
                        <i class="ri-logout-circle-line" aria-hidden="true"></i>
                        <span class="txt">Sign out everywhere ({actionable.length})</span>
                    </button>
                    <button
                        type="button"
                        class="btn sm secondary txt-danger"
                        disabled={bulkBusy ||
                            !actionable.length ||
                            superusers.length - actionable.length < 1}
                        title={includesMe
                            ? "You cannot remove yourself in bulk — that would end this session mid-run"
                            : "They lose access immediately, and every session they hold ends with them"}
                        onclick={() => (bulkRemoveOpen = true)}
                    >
                        <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                        <span class="txt">Remove ({actionable.length})</span>
                    </button>
                </BulkBar>
            {:else}
                <BulkBar
                    count={selInvites.count}
                    noun="invitation"
                    onclear={() => selInvites.clear()}
                >
                    <!-- Revoke alone: a resend answers with a link shown once
                         and cancels the one it replaces, so a run over eight
                         rows would destroy eight live links and show none of
                         the replacements. -->
                    <button
                        type="button"
                        class="btn sm secondary txt-danger"
                        disabled={bulkBusy}
                        title="The links stop working immediately"
                        onclick={bulkRevoke}
                    >
                        <i class="ri-close-line" aria-hidden="true"></i>
                        <span class="txt">Revoke</span>
                    </button>
                </BulkBar>
            {/if}
        {/if}

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <Pager
                meta={paged.meta}
                {loading}
                noun="superuser"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <span class="txt">
                {#if outstanding.length}{outstanding.length} invited{/if}{#if signedIn}{outstanding.length
                        ? " · "
                        : ""}{signedIn} signed in{/if}
            </span>
        </footer>
    </div>
</div>

<Drawer
    open={editorOpen}
    title={editing ? editing.email : "New superuser"}
    size="sm"
    onclose={closeEditor}
>
    <form id="superuser-form" onsubmit={save}>
        <div class="field required" class:error={!!errors.email}>
            <label for="su_email">Email</label>
            <input id="su_email" type="email" autocomplete="off" bind:value={form.email} />
        </div>
        {#if errors.email}<div class="field-help error">{errors.email}</div>{/if}

        <div class="field m-t-sm" class:required={!editing} class:error={!!errors.password}>
            <label for="su_password">
                {editing ? "New password" : "Password"}
            </label>
            <input
                id="su_password"
                type="password"
                autocomplete="new-password"
                placeholder={editing ? "Leave empty to keep the current one" : ""}
                bind:value={form.password}
            />
        </div>
        {#if errors.password}
            <div class="field-help error">{errors.password}</div>
        {:else}
            <div class="field-help">
                At least 8 characters.{#if editing}
                    Changing it signs this operator out of every other session.{/if}
            </div>
        {/if}

        <!--
            A sibling of the password field, not a child of its hint.

            It was emitted inside the `.field-help` div AND inside the `{:else}`
            branch above, which had two consequences on the one form in the
            panel that grants store-wide access: the picker was drawn in hint
            typography, and it disappeared the moment a short password put a
            message in `errors.password` — mid-entry, with no indication it had
            ever been there. The server defaults an omitted role to owner, the
            opposite of what an invitation defaults to, so a form that can lose
            its role field is a form that can quietly mint an owner. save()
            sends `form.role` explicitly for the same reason.
        -->
        {#if !editing}
            <div class="field m-t-sm required">
                <label for="su-role">Role</label>
                <Select id="su-role" bind:value={form.role} options={roleOptions} />
            </div>
            {#if errors.role}
                <div class="field-help error">{errors.role}</div>
            {:else}
                <div class="field-help">
                    What they may do. It can be changed later from the list, and the store always
                    keeps at least one owner.
                </div>
            {/if}
        {/if}
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (editorOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="superuser-form"
            class="btn"
            class:loading={saving}
            disabled={saving}
        >
            <span class="txt">{editing ? "Save changes" : "Create superuser"}</span>
        </button>
    {/snippet}
</Drawer>

<Drawer
    open={inviteOpen}
    title={issued ? (issued.emailed ? "Invitation sent" : "Invitation created") : "Invite member"}
    size="sm"
    onclose={() => closeInvite()}
>
    {#if issued}
        {#if issued.emailed}
            <div class="alert success m-b-base invite-result" role="status">
                <i class="ri-mail-check-line" aria-hidden="true"></i>
                <p>Invitation emailed to <strong>{issued.email}</strong>.</p>
            </div>
        {:else}
            <div class="alert warning m-b-base invite-result" role="status">
                <i class="ri-mail-close-line" aria-hidden="true"></i>
                <p>{issued.email_error || `Nothing was emailed to ${issued.email}.`}</p>
            </div>
        {/if}

        <div class="field">
            <label for="invite-link">Link for {issued.email}</label>
            <input id="invite-link" type="text" readonly value={issued.accept_url} />
        </div>
        <div class="field-help">
            It works until {formatDate(issued.expires_at)}. They choose their own password when they
            open it, or use the one they have if they already work in another store.
        </div>
    {:else}
        <form id="invite-form" onsubmit={submitInvite}>
            <div class="field">
                <label for="invite-method">Invite method</label>
                <Select id="invite-method" bind:value={invite.method} options={INVITE_METHODS} />
            </div>
            <div class="field-help">
                {invite.method === "link"
                    ? "They get an email with a link and set their own password."
                    : "You choose their first password and tell it to them."}
            </div>

            <div class="field required m-t-sm" class:error={!!errors.invite}>
                <label for="invite-email">Email</label>
                <!-- svelte-ignore a11y_autofocus -->
                <input
                    id="invite-email"
                    type="email"
                    autocomplete="off"
                    autofocus
                    placeholder="name@example.com"
                    bind:value={invite.email}
                    oninput={() => (errors = {})}
                />
            </div>
            {#if errors.invite}<div class="field-help error">{errors.invite}</div>{/if}

            <div class="field required m-t-sm">
                <label for="invite-role">Role</label>
                <Select id="invite-role" bind:value={invite.role} options={roleOptions} />
            </div>
            <div class="field-help">What they may do. It can be changed later from the list.</div>

            {#if invite.method === "password"}
                <div class="field required m-t-sm" class:error={!!errors.password}>
                    <label for="invite-password">Password</label>
                    <input
                        id="invite-password"
                        type="password"
                        autocomplete="new-password"
                        minlength="8"
                        bind:value={invite.password}
                        oninput={() => (errors = {})}
                    />
                </div>
                <div class="field-help" class:error={!!errors.password}>
                    {errors.password || "At least 8 characters. Tell them, and ask them to change it."}
                </div>
            {/if}
        </form>
    {/if}

    {#snippet footer()}
        {#if issued}
            <button
                type="button"
                class="btn transparent m-r-auto"
                onclick={() => closeInvite({ deliberate: true })}
            >
                <span class="txt">Done</span>
            </button>
            <button type="button" class="btn" class:copied onclick={copyLink}>
                <i class={copied ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                <span class="txt">{copied ? "Copied" : "Copy link"}</span>
            </button>
        {:else}
            <button
                type="button"
                class="btn transparent m-r-auto"
                onclick={() => closeInvite({ deliberate: true })}
            >
                <span class="txt">Cancel</span>
            </button>
            <button
                type="submit"
                form="invite-form"
                class="btn"
                class:loading={inviting}
                disabled={inviting}
            >
                <span class="txt">{invite.method === "link" ? "Send invitation" : "Add member"}</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title="Remove this superuser?"
    message={pendingDelete
        ? `${pendingDelete.email} will lose access to this panel immediately, and every session they hold ends with them.`
        : ""}
    confirmLabel="Remove"
    danger
    onconfirm={doDelete}
/>

<!-- A second Confirm rather than a shared one with a mode flag: this message
     names a count and the other names a person, and one component asked to say
     both ends up saying neither. -->
<Confirm
    bind:open={bulkRemoveOpen}
    title="Remove {actionable.length} {pluralize(actionable.length, 'superuser')}?"
    message="They lose access to this panel immediately, and every session they hold ends with them. The store always keeps at least one owner, so anything that would leave it without one is refused by name."
    confirmLabel="Remove"
    danger
    onconfirm={bulkRemove}
/>
{/if}

<style>
    /*
     * Feedback for the invitation drawer and rows: the result notice settles
     * in, a row being worked on dims, and the copy button answers its press.
     * 150-250ms on transform and opacity; with reduced motion the movement
     * goes and the change of state stays.
     */
    .invite-result {
        display: flex;
        gap: 8px;
        align-items: flex-start;
        animation: invite-in 220ms ease-out;
    }
    .invite-row {
        transition: opacity 180ms ease;
    }
    .invite-row.busy {
        opacity: 0.6;
    }
    .btn.copied i {
        animation: invite-pop 200ms ease-out;
    }
    @keyframes invite-in {
        from {
            opacity: 0;
            transform: translateY(-4px);
        }
        to {
            opacity: 1;
            transform: none;
        }
    }
    @keyframes invite-pop {
        from {
            transform: scale(0.6);
        }
        to {
            transform: scale(1);
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .invite-result {
            animation: invite-fade 150ms ease-out;
        }
        .btn.copied i {
            animation: none;
        }
        @keyframes invite-fade {
            from {
                opacity: 0;
            }
            to {
                opacity: 1;
            }
        }
    }

    /*
     * The tick in an invitation row. PocketBase's `.field` is `width: 100%`,
     * right in a form column and wrong in a table cell; only the width is ours.
     */
    .invite-select {
        width: auto;
        flex: 0 0 auto;
    }
</style>
