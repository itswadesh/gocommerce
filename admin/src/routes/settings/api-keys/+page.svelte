<script>
    /**
     * API keys: credentials for other systems, each with a role.
     *
     * The store already had one way for a machine to authenticate — the static
     * token in the process environment — and it is deliberately roleless, so
     * anything holding it can do anything. That is right for a deploy script
     * on the same machine and wrong for the thing a shop actually wants to
     * hand out: a key for a shipping partner that reads orders and cannot
     * refund them.
     *
     * The screen is built around the one fact that shapes everything else: the
     * secret exists once. It is shown on the row it was made on, with a copy
     * button and a warning, and it is gone on the next load — because the
     * engine hashed it and never kept it. Pretending otherwise with a "reveal"
     * button would be a lie the first operator to press it discovers at the
     * worst moment.
     */
    import { can, request } from "$lib/api.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const readable = $derived(can("apikeys.read"));
    const writable = $derived(can("apikeys.write"));

    let keys = $state([]);
    let loading = $state(true);
    let creating = $state(false);
    let revoking = $state(null);
    let confirmKey = $state(null);
    let confirmOpen = $state(false);

    let name = $state("");
    let role = $state("staff");

    /* The one key whose secret this browser has seen, held in memory only: a
       reload loses it, which is the truth about where it lives. */
    let minted = $state(null);

    const ROLES = [
        { value: "staff", label: "Staff — read the catalogue, work the orders" },
        { value: "manager", label: "Manager — the shop, its prices and its money" },
        { value: "owner", label: "Owner — everything, including making more keys" },
    ];

    $effect(() => {
        if (readable) load();
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

    async function create() {
        if (creating || !name.trim()) return;
        creating = true;
        try {
            const made = await request("POST", "/api/admin/api-keys", {
                body: { name: name.trim(), role },
            });
            minted = made;
            name = "";
            toast.success("Key created — copy it now");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            creating = false;
        }
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
</script>

<svelte:head><title>API keys · GoCommerce</title></svelte:head>

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
                <section class="card feed-card key-minted">
                    <h2 class="feed-card-title">Copy this now</h2>
                    <p class="txt-hint txt-sm feed-card-sub">
                        This is the only time <strong>{minted.name}</strong> can be read. The engine
                        stored a hash of it and cannot show it again — if it is lost, revoke the key
                        and make another.
                    </p>
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
                    <p class="txt-hint txt-sm m-t-sm">
                        Send it as <code>Authorization: Bearer {minted.prefix}…</code> on any admin
                        request. It acts as {minted.role} and is refused by name anywhere that role
                        does not reach.
                    </p>
                </section>
            {/if}

            {#if writable}
                <section class="card feed-card">
                    <h2 class="feed-card-title">New key</h2>
                    <p class="txt-hint txt-sm feed-card-sub">
                        A key is held to its role exactly as a person is, so give it the smallest
                        one that does the job. Owner includes making more keys.
                    </p>
                    <div class="feed-form">
                        <div class="field">
                            <label for="key-name">What is it for</label>
                            <input
                                id="key-name"
                                type="text"
                                maxlength="80"
                                placeholder="Warehouse robot"
                                bind:value={name}
                            />
                            <div class="field-help">
                                It is how you will recognise the key later; the secret is not
                                recoverable and this is all the list can show.
                            </div>
                        </div>
                        <div class="field">
                            <label for="key-role">Role</label>
                            <Select id="key-role" bind:value={role} options={ROLES} />
                            <div class="field-help">
                                What this store gives the role, not a fixed list — see
                                Settings › Roles.
                            </div>
                        </div>
                    </div>
                    <div class="feed-form-actions">
                        <button
                            type="button"
                            class="btn sm"
                            class:loading={creating}
                            disabled={creating || !name.trim()}
                            onclick={create}
                        >
                            <span class="txt">Create key</span>
                        </button>
                    </div>
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
                    <p class="txt-hint m-t-sm">
                        No keys yet. Everything machine-to-machine is going through the static admin
                        token, which carries no role and can do anything.
                    </p>
                {:else}
                    <div class="map-table">
                        <div class="map-row key-row map-head">
                            <div>Name</div>
                            <div>Key</div>
                            <div>Role</div>
                            <div>Last seen</div>
                            <div></div>
                        </div>
                        {#each [...active, ...revoked] as k (k.id)}
                            <div class="map-row key-row" class:is-revoked={k.revoked_at}>
                                <div class="txt-bold">{k.name}</div>
                                <div class="txt-code key-prefix">{k.prefix}…</div>
                                <div><span class="label">{k.role}</span></div>
                                <div class="txt-hint txt-sm">{lastSeen(k)}</div>
                                <div class="txt-right">
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
