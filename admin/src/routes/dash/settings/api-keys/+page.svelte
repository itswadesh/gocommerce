<script>
    /**
     * API keys: credentials for other systems, each with a role — and, since
     * M71, keys for AI agents, each with a list of rights.
     *
     * The store already had one way for a machine to authenticate — the static
     * token in the process environment — and it is deliberately roleless, so
     * anything holding it can do anything. That is right for a deploy script
     * on the same machine and wrong for the thing a shop actually wants to
     * hand out: a key for a shipping partner that reads orders and cannot
     * refund them.
     *
     * An integration key can be copied at any time, not only on the load it
     * was made on. It was hashed and thrown away at first, which is what GitHub
     * and Stripe do and is the stricter thing — but it was also stricter than
     * anything else in this engine, where a live Stripe key and every other
     * plugin credential are stored and merely masked. It bought a property the
     * rest of the system does not have and cost an operator their key the
     * moment they navigated away.
     *
     * An agent key is the exception, and on purpose (ADR 0003): only its hash
     * is kept, so it is shown once, on the load it was made on, and never
     * again. It reads only, with the rights ticked here — a checklist the
     * engine draws from what the owner holds and an agent may have — expires,
     * is rationed per minute, and sees customers' contact details masked.
     *
     * Reading an integration key is still a deliberate act rather than a side
     * effect of opening the screen: the listing carries nothing usable, and
     * Copy fetches the secret through a route gated on apikeys.write and
     * logged. Knowing a key exists and being able to use it are different
     * things.
     */
    import { tick } from "svelte";
    import { can, request } from "$lib/api.js";
    import { formatDate } from "$lib/format.js";
    import { rise } from "$lib/motion.js";
    import { rightLabel, rightScope, rightsBySection } from "$lib/rights.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const readable = $derived(can("apikeys.read"));
    const writable = $derived(can("apikeys.write"));

    let keys = $state([]);
    let loading = $state(true);
    let creating = $state(false);
    let revoking = $state(null);
    let copying = $state(null);
    let confirmKey = $state(null);
    let confirmOpen = $state(false);

    let kind = $state("integration");
    let name = $state("");
    let role = $state("staff");

    /* An agent key's terms. The defaults are the engine's own (90 days, 60 a
       minute), repeated here only so the form shows what will happen. */
    let chosen = $state([]);
    let days = $state(90);
    let rate = $state(60);

    /* What this owner may give an agent: the engine's answer, not a copy of
       its rules. Fetched the first time somebody picks "An AI agent". */
    let grantable = $state([]);
    let grantableState = $state("idle"); // idle | loading | ready | failed

    /* The one key whose secret this browser has seen, held in memory only: a
       reload loses it, which is the truth about where it lives. */
    let minted = $state(null);
    let mintedCard = $state(null);

    const ROLES = [
        { value: "staff", label: "Staff — read the catalogue, work the orders" },
        { value: "manager", label: "Manager — the shop, its prices and its money" },
        { value: "owner", label: "Owner — everything, including making more keys" },
    ];

    $effect(() => {
        if (readable) load();
    });

    $effect(() => {
        if (writable && kind === "agent" && grantableState === "idle") loadGrantable();
    });

    async function load() {
        loading = true;
        try {
            keys = await request("GET", "/api/admin/api-keys");
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function loadGrantable() {
        grantableState = "loading";
        try {
            grantable = (await request("GET", "/api/admin/api-keys/agent-rights")) ?? [];
            grantableState = "ready";
        } catch (err) {
            grantableState = "failed";
            toast.error(err);
        }
    }

    /* A module's words travel with its rights; the engine's own are in
       rights.js, as on every other screen that names a right. */
    const words = $derived(new Map(grantable.map((r) => [r.right, r])));
    const sections = $derived(
        rightsBySection(grantable.map((r) => r.right)).map((block) => ({
            section: block.section,
            rights: block.rows.flatMap((row) => row.rights.map((item) => item.right)),
        })),
    );
    const labelOf = (right) => words.get(right)?.label || rightLabel(right);
    const scopeOf = (right) => words.get(right)?.scope || rightScope(right);

    function toggleRight(right) {
        chosen = chosen.includes(right) ? chosen.filter((r) => r !== right) : [...chosen, right];
    }

    const allChosen = $derived(grantable.length > 0 && chosen.length === grantable.length);
    function chooseAll() {
        chosen = allChosen ? [] : grantable.map((r) => r.right);
    }

    const daysOk = $derived(Number.isInteger(days) && days >= 1 && days <= 365);
    const rateOk = $derived(Number.isInteger(rate) && rate >= 1 && rate <= 600);

    /* Why Create cannot run yet, in words (D98): a greyed button with no
       reason is a screen that looks broken. Empty when it can. */
    const blocker = $derived.by(() => {
        if (!name.trim()) return "Give the key a name";
        if (kind !== "agent") return "";
        if (grantableState !== "ready") return "Loading what you can give an agent";
        if (!grantable.length) return "You hold no right an agent key may carry";
        if (!chosen.length) return "Tick at least one right for the agent";
        if (!daysOk) return "Choose an expiry between 1 and 365 days";
        if (!rateOk) return "Choose between 1 and 600 requests a minute";
        return "";
    });

    async function create() {
        if (creating || blocker) return;
        creating = true;
        const body =
            kind === "agent"
                ? {
                      name: name.trim(),
                      kind: "agent",
                      rights: chosen,
                      expires_in_days: days,
                      rate_per_minute: rate,
                  }
                : { name: name.trim(), role };
        try {
            minted = await request("POST", "/api/admin/api-keys", { body });
            name = "";
            chosen = [];
            // No toast: the card below the header is the message, scrolled
            // into view with Copy focused. A toast as well put two notices
            // over the form saying what the card already says.
            await reveal();
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            creating = false;
        }
    }

    /* The card appears at the top of the screen, and Create is at the bottom
       of a long checklist: without this, the one sight of an agent key would
       happen above the fold, behind a toast. Its Copy button takes the focus,
       so the keyboard is where the eye is sent. */
    async function reveal() {
        await tick();
        if (!mintedCard) return;
        const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
        mintedCard.scrollIntoView({ behavior: still ? "auto" : "smooth", block: "start" });
        mintedCard.querySelector(".key-secret .btn")?.focus({ preventScroll: true });
    }

    async function revoke(key) {
        revoking = key.id;
        try {
            await request("DELETE", `/api/admin/api-keys/${key.id}`);
            // A revoked key that is still on screen with its secret showing is
            // an invitation to paste something that no longer works.
            if (minted?.id === key.id) minted = null;
            toast.success(`${key.name} revoked`);
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            revoking = null;
        }
    }

    /* Fetched on the click rather than held in the page: a secret that is in
       the DOM from load is one that is in every screenshot of this screen. */
    async function copyKey(key) {
        copying = key.id;
        try {
            const out = await request("GET", `/api/admin/api-keys/${key.id}/secret`);
            await copy(out.secret);
        } catch (err) {
            toast.error(err);
        } finally {
            copying = null;
        }
    }

    async function copy(text) {
        try {
            await navigator.clipboard.writeText(text);
            toast.success("Copied");
        } catch {
            toast.error("Could not copy; select it and copy by hand.");
        }
    }

    const active = $derived(keys.filter((k) => !k.revoked_at));
    const revoked = $derived(keys.filter((k) => k.revoked_at));

    function lastSeen(k) {
        if (k.revoked_at) return "Revoked " + formatDate(k.revoked_at, { withTime: false });
        if (!k.last_used_at) return "Never used";
        return "Last used " + formatDate(k.last_used_at);
    }

    /* Whole days, rounded up: a key with three hours left still works today,
       and "expires in 0 days" reads as already gone. */
    function daysLeft(k) {
        if (!k.expires_at) return null;
        return Math.ceil((new Date(k.expires_at).getTime() - Date.now()) / 86400000);
    }

    function expiry(k) {
        const left = daysLeft(k);
        if (left === null) return "";
        if (left <= 0) return "Expired";
        return left === 1 ? "Expires in 1 day" : `Expires in ${left} days`;
    }

    function rightsCount(k) {
        const n = k.rights?.length ?? 0;
        return n === 1 ? "1 right" : `${n} rights`;
    }
</script>

<svelte:head><title>{titled("API keys")}</title></svelte:head>

<div class="page page-api-keys shopify-skin">
    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item txt-sm txt-hint">Settings</div>
                <div class="breadcrumb-item feed-title">API keys</div>
            </nav>
        </header>

        {#if !readable}
            <NoAccess right="apikeys.read" what="API keys" />
        {:else}
            <!-- The secret, on the one load it exists. -->
            {#if minted}
                <section class="card feed-card key-minted" transition:rise bind:this={mintedCard}>
                    {#if minted.kind === "agent"}
                        <h2 class="feed-card-title">
                            <i class="ri-robot-2-line" aria-hidden="true"></i>
                            {minted.name} is ready — copy it now
                        </h2>
                        <p class="txt-hint txt-sm feed-card-sub">
                            <strong>This is the only time this key is shown.</strong> Only its hash
                            is kept, so it cannot be shown again, here or anywhere. If it is lost,
                            revoke it and make another.
                        </p>
                    {:else}
                        <h2 class="feed-card-title">{minted.name} is ready</h2>
                        <p class="txt-hint txt-sm feed-card-sub">
                            Copy it into whatever is going to use it. You can come back for it later —
                            Copy on its row fetches it again — but anything holding a key it cannot use
                            is a call somebody makes at three in the morning.
                        </p>
                    {/if}
                    <div class="key-secret">
                        <code class="txt-code">{minted.secret}</code>
                        <button type="button" class="btn sm secondary" onclick={() => copy(minted.secret)}>
                            <i class="ri-file-copy-line" aria-hidden="true"></i>
                            <span class="txt">Copy</span>
                        </button>
                        <button
                            type="button"
                            class="btn sm transparent secondary"
                            onclick={() => (minted = null)}
                        >
                            <span class="txt">Done</span>
                        </button>
                    </div>
                    {#if minted.kind === "agent"}
                        <p class="txt-hint txt-sm m-t-sm">
                            Send it as <code>Authorization: Bearer {minted.prefix}…</code> on any admin
                            request. It reads with {rightsCount(minted)}, sees customers' email
                            addresses and phone numbers masked, is allowed {minted.rate_per_minute}
                            requests a minute, and stops working on
                            {formatDate(minted.expires_at, { withTime: false })}.
                        </p>
                    {:else}
                        <p class="txt-hint txt-sm m-t-sm">
                            Send it as <code>Authorization: Bearer {minted.prefix}…</code> on any admin
                            request. It acts as {minted.role} and is refused by name anywhere that role
                            does not reach.
                        </p>
                    {/if}
                </section>
            {/if}

            {#if writable}
                <section class="card feed-card">
                    <h2 class="feed-card-title">New key</h2>
                    <p class="txt-hint txt-sm feed-card-sub">
                        Give a key the least it needs. A key for an integration is held to its role
                        exactly as a person is; a key for an AI agent is held to the rights you tick.
                    </p>

                    <fieldset class="key-kind">
                        <legend class="key-kind-legend">Who will use it</legend>
                        <div class="key-kind-options">
                            <label class="key-kind-option" class:is-on={kind === "integration"}>
                                <input type="radio" name="key-kind" value="integration" bind:group={kind} />
                                <i class="ri-plug-line key-kind-icon" aria-hidden="true"></i>
                                <span class="key-kind-text">
                                    <span class="key-kind-title">An integration</span>
                                    <span class="txt-hint txt-sm">
                                        A script or another system. It acts in a role, and its key can be
                                        copied again later.
                                    </span>
                                </span>
                            </label>
                            <label class="key-kind-option" class:is-on={kind === "agent"}>
                                <input type="radio" name="key-kind" value="agent" bind:group={kind} />
                                <i class="ri-robot-2-line key-kind-icon" aria-hidden="true"></i>
                                <span class="key-kind-text">
                                    <span class="key-kind-title">An AI agent</span>
                                    <span class="txt-hint txt-sm">
                                        Reads only, with the rights you tick. It expires, is rate limited,
                                        and is shown once.
                                    </span>
                                </span>
                            </label>
                        </div>
                    </fieldset>

                    <div class="feed-form">
                        <div class="field">
                            <label for="key-name">What is it for</label>
                            <input
                                id="key-name"
                                type="text"
                                maxlength="80"
                                placeholder={kind === "agent" ? "Order desk" : "Warehouse robot"}
                                bind:value={name}
                            />
                            <div class="field-help">
                                It is how you will recognise the key in the list, and in the audit
                                trail beside everything it does.
                            </div>
                        </div>
                        {#if kind === "integration"}
                            <div class="field" in:rise>
                                <label for="key-role">Role</label>
                                <Select id="key-role" bind:value={role} options={ROLES} />
                                <div class="field-help">
                                    What this store gives the role, not a fixed list — see
                                    Settings › Roles.
                                </div>
                            </div>
                        {/if}
                    </div>

                    {#if kind === "agent"}
                        <div class="agent-terms" in:rise>
                            <div class="agent-note">
                                <i class="ri-shield-keyhole-line" aria-hidden="true"></i>
                                <p class="txt-sm">
                                    Agent keys can only read. An agent sees customers' email addresses
                                    and phone numbers masked, and never reaches keys, the team, roles,
                                    plugins, store settings, exports or webhooks.
                                </p>
                            </div>

                            <div class="agent-rights-head">
                                <h3 class="agent-rights-title">What it may read</h3>
                                <span class="txt-hint txt-sm">
                                    {chosen.length} of {grantable.length} chosen
                                </span>
                                <div class="flex-fill"></div>
                                <button
                                    type="button"
                                    class="btn sm transparent secondary"
                                    disabled={grantableState !== "ready" || !grantable.length}
                                    onclick={chooseAll}
                                >
                                    <span class="txt">{allChosen ? "Clear" : "Choose all"}</span>
                                </button>
                            </div>

                            {#if grantableState === "loading" || grantableState === "idle"}
                                <div class="block txt-center p-base"><span class="loader"></span></div>
                            {:else if grantableState === "failed"}
                                <div class="alert danger">
                                    <p>The rights could not be loaded.</p>
                                    <button type="button" class="btn sm secondary" onclick={loadGrantable}>
                                        <span class="txt">Try again</span>
                                    </button>
                                </div>
                            {:else if !grantable.length}
                                <p class="txt-hint txt-sm">
                                    You hold no right an agent key may carry, so you cannot make one.
                                </p>
                            {:else}
                                <div class="agent-rights">
                                    {#each sections as block (block.section)}
                                        <div class="agent-rights-section">
                                            <div class="agent-rights-section-title">{block.section}</div>
                                            {#each block.rights as right (right)}
                                                <label class="agent-right" class:is-on={chosen.includes(right)}>
                                                    <input
                                                        type="checkbox"
                                                        checked={chosen.includes(right)}
                                                        onchange={() => toggleRight(right)}
                                                    />
                                                    <span class="agent-right-text">
                                                        <span class="agent-right-label">{labelOf(right)}</span>
                                                        <span class="txt-hint txt-sm">{scopeOf(right)}</span>
                                                    </span>
                                                </label>
                                            {/each}
                                        </div>
                                    {/each}
                                </div>
                            {/if}

                            <div class="feed-form agent-limits">
                                <div class="field">
                                    <label for="key-days">Expires after (days)</label>
                                    <input
                                        id="key-days"
                                        type="number"
                                        min="1"
                                        max="365"
                                        step="1"
                                        bind:value={days}
                                        aria-invalid={!daysOk}
                                    />
                                    <div class="field-help" class:is-error={!daysOk}>
                                        1 to 365. It stops working then, whether anybody remembers it
                                        or not.
                                    </div>
                                </div>
                                <div class="field">
                                    <label for="key-rate">Requests a minute</label>
                                    <input
                                        id="key-rate"
                                        type="number"
                                        min="1"
                                        max="600"
                                        step="1"
                                        bind:value={rate}
                                        aria-invalid={!rateOk}
                                    />
                                    <div class="field-help" class:is-error={!rateOk}>
                                        1 to 600. Over it, the agent is told to wait until the minute is
                                        up.
                                    </div>
                                </div>
                            </div>
                        </div>
                    {/if}

                    <div class="feed-form-actions">
                        <button
                            type="button"
                            class="btn sm"
                            class:loading={creating}
                            class:disabled={!!blocker}
                            disabled={creating}
                            aria-disabled={blocker ? "true" : undefined}
                            aria-describedby={blocker ? "key-create-why" : undefined}
                            onclick={create}
                        >
                            <span class="txt">{kind === "agent" ? "Create agent key" : "Create key"}</span>
                        </button>
                    </div>
                    {#if blocker}
                        <p id="key-create-why" class="field-help m-t-sm" in:rise>{blocker}</p>
                    {/if}
                </section>
            {/if}

            <section class="card feed-card">
                <h2 class="feed-card-title">
                    {active.length}
                    {active.length === 1 ? "key" : "keys"} in use
                </h2>
                <p class="txt-hint txt-sm feed-card-sub">
                    Revoked keys stay listed. What had access and when it stopped is the question
                    asked after something goes wrong, and a deleted row answers neither.
                </p>

                {#if loading && !keys.length}
                    <div class="block txt-center p-base"><span class="loader lg"></span></div>
                {:else if !keys.length}
                    <EmptyState
                        icon="ri-key-2-line"
                        title="No API keys yet"
                        text="Everything machine-to-machine is going through the static admin token, which carries no role and can do anything. A key is held to a role, or for an AI agent to a list of rights."
                    >
                        {#if writable}
                            <!-- The form is the card above; this only takes the
                                 reader to its first field. -->
                            <button
                                type="button"
                                class="btn"
                                onclick={() => document.getElementById("key-name")?.focus()}
                            >
                                <i class="ri-add-line" aria-hidden="true"></i>
                                <span class="txt">New key</span>
                            </button>
                        {/if}
                    </EmptyState>
                {:else}
                    <div class="map-table">
                        <div class="map-row key-row map-head">
                            <div>Name</div>
                            <div>Key</div>
                            <div>Access</div>
                            <div>Last seen</div>
                            <div></div>
                        </div>
                        {#each [...active, ...revoked] as k (k.id)}
                            <div class="map-row key-row" class:is-revoked={k.revoked_at} in:rise>
                                <div class="txt-bold">{k.name}</div>
                                <div class="txt-code key-prefix">{k.prefix}…</div>
                                <div class="key-access">
                                    {#if k.kind === "agent"}
                                        <span class="label key-kind-chip">
                                            <i class="ri-robot-2-line" aria-hidden="true"></i>
                                            Agent
                                        </span>
                                        <span class="txt-hint txt-sm" title={(k.rights ?? []).join(", ")}>
                                            {rightsCount(k)} · read only
                                        </span>
                                    {:else}
                                        <span class="label key-kind-chip">
                                            <i class="ri-plug-line" aria-hidden="true"></i>
                                            Integration
                                        </span>
                                        <span class="txt-hint txt-sm">Acts as {k.role}</span>
                                    {/if}
                                </div>
                                <div class="key-seen txt-sm">
                                    <span class="txt-hint">{lastSeen(k)}</span>
                                    {#if k.kind === "agent" && !k.revoked_at}
                                        <span
                                            class="key-expiry"
                                            class:is-expired={daysLeft(k) <= 0}
                                            class:is-soon={daysLeft(k) > 0 && daysLeft(k) <= 7}
                                        >
                                            {expiry(k)}
                                        </span>
                                    {/if}
                                </div>
                                <div class="txt-right key-actions">
                                    <!-- An agent key has no Copy: only its hash
                                         is kept, so there is nothing to fetch. -->
                                    {#if writable && k.kind !== "agent"}
                                        <button
                                            type="button"
                                            class="btn sm transparent secondary"
                                            class:loading={copying === k.id}
                                            disabled={copying === k.id}
                                            title="Copy {k.name} to the clipboard"
                                            onclick={() => copyKey(k)}
                                        >
                                            <i class="ri-file-copy-line" aria-hidden="true"></i>
                                            <span class="txt">Copy</span>
                                        </button>
                                    {/if}
                                    {#if writable && !k.revoked_at}
                                        <button
                                            type="button"
                                            class="btn sm transparent secondary"
                                            class:loading={revoking === k.id}
                                            disabled={revoking === k.id}
                                            onclick={() => ((confirmKey = k), (confirmOpen = true))}
                                        >
                                            <span class="txt">Revoke</span>
                                        </button>
                                    {/if}
                                </div>
                            </div>
                        {/each}
                    </div>
                {/if}
            </section>
        {/if}
    </div>
</div>

<Confirm
    bind:open={confirmOpen}
    danger
    title="Revoke {confirmKey?.name ?? 'this key'}?"
    message="Anything using it stops working immediately, and it cannot be turned back on — you would make a new key and change whatever holds this one."
    confirmLabel="Revoke"
    onconfirm={() => confirmKey && revoke(confirmKey)}
/>
