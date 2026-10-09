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
    import { portal } from "$lib/portal.js";
    import { trapFocus } from "$lib/focus.js";
    import { dismissable } from "$lib/dismiss.js";

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
    let invite = $state({ email: "", role: "staff" });
    let inviting = $state(false);
    /** The invitation being re-issued, so its own row shows the spinner. */
    let resending = $state("");
    // The link, held only for as long as the drawer that shows it is open. The
    // engine cannot produce it a second time, so this is the one chance to
    // copy it — and it must not end up anywhere it would outlive that.
    let issued = $state(null);
    let copied = $state(false);
    /** Whether the link on screen replaced an earlier one, which the drawer has
     *  to say out loud: the earlier link stopped working when this was made. */
    let reissued = $state(false);

    let editorOpen = $state(false);
    let editing = $state(null); // null = creating
    let form = $state({ email: "", password: "", role: "staff" });

    /*
     * The store's roles, from the store (D81). A store names and makes its own
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

    /** Outstanding ones only: accepted invitations are history, and the person
     *  they let in is already in the table above. */
    const outstanding = $derived(invitations.filter((i) => i.status !== "accepted"));

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
     * Revoking is the only bulk action an invitation has, and Resend is
     * deliberately not beside it. One resend answers with a link that is shown
     * once and cannot be produced again, and it cancels the link it replaces —
     * so a run over eight rows would destroy eight live links and show none of
     * the eight replacements. It stays a per-row button, which is where the
     * drawer that shows the link can follow it.
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

    /* An issued link is not a dirty form — closeInvite already refuses to
       dismiss while one is on screen, and for a stronger reason. */
    const inviteDirty = $derived(
        inviteOpen && !issued && (invite.email.trim() !== "" || invite.role !== "staff"),
    );

    const anyDirty = $derived(editorDirty || inviteDirty);

    /*
     * Adding somebody, the KitCommerce admin's way: an email and a role first,
     * and only then — when the address has no account — a choice between
     * making one now and sending a link. On a single store an "account" is an
     * operator of this store; once accounts span stores (D80) the same check
     * asks the platform instead, and an address that already has one joins
     * without either.
     */
    let chooseOpen = $state(false);
    let how = $state("invite"); // "create" | "invite"
    let newPassword = $state("");
    let proceeding = $state(false);

    function continueAdd(event) {
        event?.preventDefault();
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
        how = "invite";
        newPassword = "";
        chooseOpen = true;
    }

    async function proceed() {
        if (proceeding) return;
        errors = {};
        if (how === "create" && newPassword.length < 8) {
            errors.choose = "A password of at least 8 characters is required.";
            return;
        }
        proceeding = true;
        try {
            if (how === "create") {
                await auth.create(invite.email.trim(), newPassword, invite.role);
                toast.success("User created and added to team");
                chooseOpen = false;
                closeInvite({ deliberate: true });
                await load();
            } else {
                await sendInvite();
                if (issued) {
                    toast.success("Invite sent");
                    chooseOpen = false;
                } else if (errors.invite) {
                    errors.choose = errors.invite;
                }
            }
        } catch (err) {
            errors.choose = err.message;
        } finally {
            proceeding = false;
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
        invite = { email: "", role: defaultRole };
        issued = null;
        copied = false;
        reissued = false;
        errors = {};
        inviteOpen = true;
    }

    async function sendInvite(event) {
        event?.preventDefault();
        if (inviting) return;
        errors = {};
        if (!invite.email.trim()) {
            errors.invite = "An email is required.";
            return;
        }
        inviting = true;
        try {
            const result = await auth.invite(invite.email.trim(), invite.role);
            issued = result.data ?? result;
            await load();
        } catch (err) {
            errors.invite = err.message;
        } finally {
            inviting = false;
        }
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

    /**
     * Re-issue an outstanding invitation.
     *
     * One POST, not revoke-then-invite: the engine deletes an outstanding
     * invitation for the same address before writing the new one
     * (core/invitations.go), so re-inviting IS the replacement. What that means
     * for the operator is worth being plain about — the old link stops working
     * the moment this one exists, which is the right behaviour for a lost link
     * and the wrong surprise if somebody still has the first one.
     *
     * Until this existed the only remedy for a link that never arrived was to
     * find the row, revoke it, and then invite the same address again from a
     * different button — three steps to repeat one request.
     */
    async function resendInvite(inv) {
        if (resending) return;
        resending = inv.id;
        errors = {};
        try {
            const result = await auth.invite(inv.email, inv.role);
            // Straight into the drawer that shows the link: the response is the
            // only place it exists, so anything that navigates away from it
            // first has already lost it.
            issued = result.data ?? result;
            copied = false;
            reissued = true;
            invite = { email: inv.email, role: inv.role };
            inviteOpen = true;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            resending = "";
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
     * The invite drawer only closes deliberately while the one-time link is on
     * screen.
     *
     * `onclose` is one callback for the backdrop, Escape and the header's X, so
     * it cannot tell them apart — and a stray click on the dimmed page while
     * that link is up destroys it. So every route out of the drawer is refused
     * except the footer's Done, which says what it is doing. The form state has
     * nothing to protect and dismisses normally.
     *
     * Resend makes this recoverable rather than fatal now, which is why this is
     * a refusal with an explanation and not a confirmation dialog over a
     * dialog.
     */
    function closeInvite({ deliberate = false } = {}) {
        if (issued && !deliberate) {
            toast.info("This link is shown once — press Done when you have copied it");
            return;
        }
        /*
         * A half-typed invitation is worth one question. Cancel passes
         * `deliberate` and is never asked — it says what it does — while
         * Escape, the backdrop and the header's X all arrive here saying
         * nothing, which is how the address somebody was mid-way through
         * typing used to disappear.
         *
         * `confirm` rather than a dialog over a dialog, the same reasoning
         * DirtyGuard sets out: this has to answer synchronously and a second
         * modal over an open drawer is worse than the browser's own question.
         */
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
                <!-- One entry point, as in the KitCommerce admin: the email
                     and role come first, and how the person arrives (an
                     account made now, or a link they open) is asked after. -->
                {#if writable}
                    <button type="button" class="btn" onclick={openInvite}>
                        <i class="ri-user-add-line" aria-hidden="true"></i>
                        <span class="txt">Add team member</span>
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

        {#if outstanding.length}
            <!-- Below the team rather than beside it: these are people who are
                 not here yet, and mixing them into the list would say they are. -->
            <div class="m-t-base">
                <h2 class="tw:mt-6 tw:mb-3 tw:flex tw:flex-wrap tw:items-center tw:gap-2 tw:text-sm tw:font-semibold">
                    Invited, not yet joined
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
                </h2>
                <div class="list">
                    {#each outstanding as inv (inv.id)}
                        <div class="list-item">
                            {#if writable}
                                <!-- The same tick as the table above, so a
                                     selection means one thing on this screen.
                                     `.field` is `width: 100%` in a form column
                                     and has to be told otherwise in a flex
                                     row — see the scoped rule at the foot. -->
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
                            <i class="ri-mail-line" aria-hidden="true"></i>
                            <span class="txt">{inv.email}</span>
                            <span class="label">{roleName(inv.role)}</span>
                            {#if inv.status === "expired"}
                                <span class="label warning">expired</span>
                            {/if}
                            <div class="flex-fill"></div>
                            <span class="txt-hint txt-sm">
                                {inv.invited_by ? `invited by ${inv.invited_by}` : "invited"}
                                · {inv.status === "expired" ? "expired" : "expires"}
                                {formatDate(inv.expires_at)}
                            </span>
                            <!-- Before Revoke, because it is the thing an
                                 operator standing at this row nearly always
                                 wants: the link did not arrive, or it expired.
                                 The title says what it costs — the previous
                                 link stops working — because the engine
                                 replaces the outstanding invitation rather
                                 than adding a second one. -->
                            {#if writable}
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    class:loading={resending === inv.id}
                                    disabled={!!resending}
                                    aria-label="Resend the invitation to {inv.email}"
                                    title="Resend — issues a fresh link and cancels the old one"
                                    onclick={() => resendInvite(inv)}
                                >
                                    <i class="ri-mail-send-line" aria-hidden="true"></i>
                                </button>
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="Revoke the invitation to {inv.email}"
                                    title="Revoke"
                                    onclick={() => revokeInvite(inv)}
                                >
                                    <i class="ri-close-line" aria-hidden="true"></i>
                                </button>
                            {/if}
                        </div>
                    {/each}
                </div>
            </div>
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
    title={issued ? "Send this link" : "Add team member"}
    size="sm"
    onclose={() => closeInvite()}
>
    {#if issued}
        <!-- The link exists in this response and nowhere else: the store kept
             only its hash. Saying so is what stops somebody closing the drawer
             expecting to find it again on the list. -->
        <div class="alert info m-b-base">
            <p>
                <i class="ri-information-line" aria-hidden="true"></i>
                This is the only time this link is shown. Close this and it cannot be recovered —
                you would have to resend the invitation to {issued.email}, which issues a
                different link again.
            </p>
            {#if reissued}
                <!-- Said here rather than only on the button that did it: the
                     operator may have pressed Resend to chase a link somebody
                     had already been sent, and both links are not live. -->
                <p>
                    Any link {issued.email} was sent before this one has stopped working.
                </p>
            {/if}
        </div>

        <div class="field">
            <label for="invite-link">Link for {issued.email}</label>
            <input id="invite-link" type="text" readonly value={issued.accept_url} />
        </div>
        <div class="field-help">
            They choose their own password when they open it. It works until {formatDate(
                issued.expires_at,
            )}.
        </div>
    {:else}
        <form id="invite-form" onsubmit={continueAdd}>
            <div class="field required" class:error={!!errors.invite}>
                <label for="invite-email">Email</label>
                <!-- svelte-ignore a11y_autofocus -->
                <input
                    id="invite-email"
                    type="email"
                    autocomplete="off"
                    autofocus
                    bind:value={invite.email}
                    oninput={() => (errors = {})}
                />
            </div>
            {#if errors.invite}<div class="field-help error">{errors.invite}</div>{/if}

            <div class="field m-t-sm">
                <label for="invite-role">Role</label>
                <Select id="invite-role" bind:value={invite.role} options={roleOptions} />
            </div>
            <div class="field-help">
                Next you choose how they arrive: an account you make now, or a link they open
                and set their own password with.
            </div>
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
            <button type="button" class="btn" onclick={copyLink}>
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
                <span class="txt">Continue</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<!-- The KitCommerce admin's "User Account Not Found" choice. Built on the
     panel's modal (the same markup Confirm uses), so it opens, closes, traps
     focus and honours reduced motion the way every other dialog here does. -->
<div
    class="modal popup sm"
    data-modal-state={chooseOpen ? "open" : "closed"}
    inert={!chooseOpen}
    role="dialog"
    aria-modal="true"
    aria-labelledby="choose-title"
    tabindex="-1"
    use:portal
    use:trapFocus={chooseOpen}
    use:dismissable={{ onclose: () => (chooseOpen = false), enabled: chooseOpen && !proceeding }}
>
    <div class="modal-content">
        <h5 id="choose-title" class="m-b-sm">User Account Not Found</h5>
        <p class="txt-hint m-b-base">
            The email {invite.email.trim()} does not have a registered account yet. Choose how you
            would like to proceed.
        </p>
        <div class="choice-cards" role="radiogroup" aria-label="How they join">
            <button
                type="button"
                class="choice-card"
                role="radio"
                aria-checked={how === "create"}
                onclick={() => (how = "create")}
            >
                <i class="ri-user-add-line" aria-hidden="true"></i>
                <span class="choice-title">Create Account</span>
                <span class="choice-sub">Enter a password</span>
            </button>
            <button
                type="button"
                class="choice-card"
                role="radio"
                aria-checked={how === "invite"}
                onclick={() => (how = "invite")}
            >
                <i class="ri-mail-send-line" aria-hidden="true"></i>
                <span class="choice-title">Send Invite</span>
                <span class="choice-sub">Send a registration link</span>
            </button>
        </div>
        {#if how === "create"}
            <div class="field required m-t-base" class:error={!!errors.choose}>
                <label for="choose-password">Password</label>
                <input
                    id="choose-password"
                    type="password"
                    autocomplete="new-password"
                    minlength="8"
                    bind:value={newPassword}
                />
            </div>
            <div class="field-help">At least 8 characters. Tell them, and ask them to change it.</div>
        {:else}
            <p class="txt-hint m-t-base">
                An invitation link will be made for {invite.email.trim()}. They open it, set their own
                password and join the team; nobody else ever knows it.
            </p>
        {/if}
        {#if errors.choose}<div class="field-help error m-t-sm">{errors.choose}</div>{/if}
    </div>
    <footer class="modal-footer">
        <button type="button" class="btn transparent m-r-auto" onclick={() => (chooseOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button type="button" class="btn" class:loading={proceeding} disabled={proceeding} onclick={proceed}>
            <span class="txt">{proceeding ? "Processing…" : "Confirm & Proceed"}</span>
        </button>
    </footer>
</div>

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
     * The two ways a person joins, as the KitCommerce admin draws them: a
     * pair of cards, one checked. Feedback on hover, press and focus, and the
     * checked state lands in 150ms on transform and colour; with reduced
     * motion the movement goes and the change of state stays.
     */
    .choice-cards {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 10px;
    }
    .choice-card {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 2px;
        padding: 12px;
        text-align: left;
        border: 1px solid var(--borderColor);
        border-radius: var(--borderRadius);
        background: var(--baseColor);
        color: var(--txtPrimaryColor);
        cursor: pointer;
        transition:
            transform 150ms ease,
            border-color 150ms ease,
            box-shadow 150ms ease;
    }
    .choice-card:hover {
        border-color: var(--inputBorderColor);
        transform: translateY(-1px);
    }
    .choice-card:active {
        transform: translateY(0) scale(0.99);
    }
    .choice-card:focus-visible {
        outline: 2px solid var(--primaryColor);
        outline-offset: 2px;
    }
    .choice-card[aria-checked="true"] {
        border-color: var(--primaryColor);
        box-shadow: 0 0 0 1px var(--primaryColor);
    }
    .choice-card i {
        font-size: 1.25rem;
        margin-bottom: 4px;
    }
    .choice-title {
        font-weight: 600;
    }
    .choice-sub {
        font-size: var(--smFontSize);
        color: var(--txtHintColor);
    }
    @media (max-width: 420px) {
        .choice-cards {
            grid-template-columns: 1fr;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .choice-card,
        .choice-card:hover,
        .choice-card:active {
            transform: none;
            transition: border-color 150ms ease, box-shadow 150ms ease;
        }
    }

    /*
     * A checkbox as one item in a `.list-item` row.
     *
     * PocketBase's `.field` is `width: 100%` (form.css), which is right for a
     * field in a form column and wrong for a tick in a flex row — it takes the
     * whole line and pushes the address, the role chip and both buttons off the
     * end of it. Only the width is ours; the box itself is PocketBase's.
     */
    .invite-select {
        width: auto;
        flex: 0 0 auto;
    }

    /*
     * The same row on a phone.
     *
     * `.list-item` is one flex line, and at 390px it squeezed the address to
     * 48px and spelled it down the screen a character per line — before a tick
     * was added, which took it to 33px. So the address gets the width it needs
     * and everything else wraps under it, which is the reading an operator
     * chasing an invitation from their phone actually needs.
     */
    @media (max-width: 550px) {
        .list-item {
            flex-wrap: wrap;
        }
        .list-item .txt {
            flex: 1 1 100%;
            min-width: 0;
        }
    }
</style>
