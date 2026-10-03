<script>
    /**
     * Team: who buys for the company, in which role, and who has been
     * invited. Only an account admin runs it; the engine refuses everybody
     * else, and so does this page before asking.
     *
     * Joining is by invitation to an address, never by adding an account
     * directly: accepting from that mailbox is what proves the address, and an
     * unproven address must never be given the company's prices (D66).
     */
    import { onMount } from "svelte";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { trade, tradeApi, explain, loadMe, roleWord, ROLE_WORDS } from "$lib/trade.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Select from "$lib/components/Select.svelte";
    import PortalPage from "../PortalPage.svelte";

    const ROLE_OPTIONS = Object.entries(ROLE_WORDS).map(([value, w]) => ({
        value,
        label: `${w.label} — ${w.hint}`,
        short: w.label,
    }));

    let members = $state([]);
    let invitations = $state([]);
    let loading = $state(true);
    let failure = $state("");
    let changing = $state(0);
    let removing = $state(null);
    let removeOpen = $state(false);
    let cancelling = $state(null);
    let cancelOpen = $state(false);

    let inviteEmail = $state("");
    let inviteRole = $state("buyer");
    let inviting = $state(false);
    let inviteError = $state("");
    let invited = $state("");

    const isAdmin = $derived(trade.me.role === "admin");
    const pending = $derived(invitations.filter((i) => !i.accepted_at));

    async function load() {
        if (!isAdmin) {
            loading = false;
            return;
        }
        loading = true;
        failure = "";
        try {
            const [m, i] = await Promise.all([tradeApi.get("/x/b2b/members"), tradeApi.get("/x/b2b/invitations")]);
            members = m ?? [];
            invitations = i ?? [];
        } catch (err) {
            if (!err.handled) failure = explain(err);
        } finally {
            loading = false;
        }
    }

    onMount(load);

    async function setRole(member, role) {
        if (role === member.role) return;
        changing = member.customer_id;
        try {
            const updated = await tradeApi.patch(`/x/b2b/members/${member.customer_id}`, { role });
            // Only the role is taken from the answer: the rest of the row is what
            // the list said, and the answer does not always carry the name.
            members = members.map((m) => (m.customer_id === member.customer_id ? { ...m, role: updated?.role ?? role } : m));
            toast.success(`${member.name || member.email}'s role is now ${roleWord(role)}.`);
            if (member.customer_id === trade.account?.id) await loadMe();
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
            members = [...members];
        } finally {
            changing = 0;
        }
    }

    async function remove() {
        const m = removing;
        try {
            await tradeApi.delete(`/x/b2b/members/${m.customer_id}`);
            members = members.filter((x) => x.customer_id !== m.customer_id);
            toast.info(`${m.name || m.email} no longer buys for ${trade.me.company.name}.`);
            if (m.customer_id === trade.account?.id) await loadMe();
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
        }
    }

    async function cancelInvitation() {
        const inv = cancelling;
        try {
            await tradeApi.delete(`/x/b2b/invitations/${inv.id}`);
            invitations = invitations.filter((x) => x.id !== inv.id);
            if (invited === inv.email) invited = "";
            toast.info(`The invitation to ${inv.email} is cancelled.`);
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
        }
    }

    async function invite(event) {
        event.preventDefault();
        if (inviting) return;
        inviteError = "";
        invited = "";
        const email = inviteEmail.trim();
        if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
            inviteError = "That doesn't look like an email address.";
            return;
        }
        inviting = true;
        try {
            const inv = await tradeApi.post("/x/b2b/invitations", { email, role: inviteRole });
            invitations = [inv, ...invitations.filter((i) => i.id !== inv.id)];
            invited = email;
            inviteEmail = "";
        } catch (err) {
            if (!err.handled) inviteError = explain(err);
        } finally {
            inviting = false;
        }
    }
</script>

<PortalPage title="Team">
    {#if !isAdmin}
        <div class="portal-empty-state tw:rounded-xl tw:border tw:bg-card">
            <i class="ri-team-line" aria-hidden="true"></i>
            <h2>Only an account admin manages the team</h2>
            <p class="txt-hint">Ask one of {trade.me.company.name}'s admins to add someone or change a role.</p>
        </div>
    {:else}
        <p class="field-help m-b-base portal-intro">
            Everyone who buys for {trade.me.company.name}. An approver orders without approval and decides buyers'
            requests; an account admin does that and runs this page.
        </p>

        {#if failure}
            <div class="alert danger m-b-base portal-notice" role="alert">
                <p>{failure}</p>
                <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
            </div>
        {/if}

        <div class="page-table-wrapper tw:rounded-xl tw:border m-b-base" class:faded={loading && members.length > 0}>
            <table class="table responsive-table">
                <thead>
                    <tr>
                        <th class="col-field-name-id">Person</th>
                        <th class="min-width">Role</th>
                        <th class="min-width">Since</th>
                        <th class="min-width" aria-label="Remove"></th>
                    </tr>
                </thead>
                <tbody>
                    {#if loading && !members.length}
                        {#each Array(3) as _, i (i)}
                            <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {/if}
                    {#each members as m (m.customer_id)}
                        {@const me = m.customer_id === trade.account?.id}
                        <tr>
                            <td class="col-field-name-id" data-name="Person">
                                <div class="row-name row-name-stacked">
                                    <span class="txt-bold">{m.name || m.email || "—"}{me ? " (you)" : ""}</span>
                                    <span class="txt-hint txt-sm">
                                        {m.email || "Moved to an address not yet confirmed"}
                                        {#if !m.confirmed}· <span class="txt-warning">not confirmed</span>{/if}
                                    </span>
                                </div>
                            </td>
                            <td class="min-width" data-name="Role">
                                <div class="field b2b-role b2b-select-fit portal-role-pick" class:faded={changing === m.customer_id}>
                                    <Select
                                        ariaLabel="Role of {m.name || m.email}"
                                        value={m.role}
                                        options={ROLE_OPTIONS}
                                        disabled={changing === m.customer_id}
                                        onchange={(v) => setRole(m, v)}
                                    />
                                </div>
                            </td>
                            <td class="min-width txt-hint txt-sm" data-name="Since">{formatDate(m.created_at, { withTime: false })}</td>
                            <td class="min-width b2b-actions">
                                <button
                                    type="button"
                                    class="btn circle sm transparent danger portal-remove"
                                    aria-label="Remove {m.name || m.email}"
                                    title="Remove from the company"
                                    onclick={() => {
                                        removing = m;
                                        removeOpen = true;
                                    }}
                                >
                                    <i class="ri-user-unfollow-line" aria-hidden="true"></i>
                                </button>
                            </td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        </div>

        <h2 class="section-title">
            <i class="ri-mail-send-line" aria-hidden="true"></i>
            Invite someone
        </h2>
        <form class="b2b-inline-form m-b-base" onsubmit={invite} novalidate>
            <div class="field b2b-grow" class:error={!!inviteError}>
                <label for="invite-email">Their work email</label>
                <input id="invite-email" type="email" autocomplete="off" bind:value={inviteEmail} />
            </div>
            <div class="field b2b-wide">
                <label for="invite-role">Role</label>
                <Select id="invite-role" bind:value={inviteRole} options={ROLE_OPTIONS} />
            </div>
            <button type="submit" class="btn portal-press" class:loading={inviting} disabled={inviting || !inviteEmail.trim()}>
                <span class="txt">Send invitation</span>
            </button>
        </form>
        {#key inviteError}
            {#if inviteError}
                <div class="field-help txt-danger portal-refusal m-b-base" role="alert">{inviteError}</div>
            {/if}
        {/key}
        {#if invited}
            <div class="alert success m-b-base portal-notice" role="status">
                <p>We've emailed {invited} a link. They join {trade.me.company.name} when they accept it from that address.</p>
            </div>
        {/if}

        {#if pending.length}
            <h3 class="b2b-eyebrow m-t-base">Waiting to be accepted</h3>
            <ul class="portal-list tw:rounded-xl tw:border tw:bg-card">
                {#each pending as inv (inv.id)}
                    <li class="portal-list-row">
                        <span class="portal-list-main">
                            <span class="txt-bold">{inv.email}</span>
                            <span class="txt-hint txt-sm">
                                {roleWord(inv.role)} · invited by {inv.invited_by} · open until {formatDate(inv.expires_at, { withTime: false })}
                            </span>
                        </span>
                        <span class="portal-list-side">
                            <button type="button" class="btn sm transparent secondary portal-press" onclick={() => {
                                    cancelling = inv;
                                    cancelOpen = true;
                                }}>
                                <span class="txt">Cancel</span>
                            </button>
                        </span>
                    </li>
                {/each}
            </ul>
        {/if}
    {/if}
</PortalPage>

<Confirm
    bind:open={removeOpen}
    title={removing?.customer_id === trade.account?.id ? "Leave the company?" : `Remove ${removing?.name || removing?.email}?`}
    message={removing?.customer_id === trade.account?.id
        ? `You'll stop buying for ${trade.me.company.name} and lose its prices.`
        : `They stop buying for ${trade.me.company.name} and lose its prices. Their past orders stay on the account.`}
    confirmLabel={removing?.customer_id === trade.account?.id ? "Leave" : "Remove"}
    danger
    onconfirm={remove}
/>

<Confirm
    bind:open={cancelOpen}
    title="Cancel the invitation to {cancelling?.email}?"
    message="The link in their email stops working. You can invite them again later."
    confirmLabel="Cancel invitation"
    danger
    onconfirm={cancelInvitation}
/>
