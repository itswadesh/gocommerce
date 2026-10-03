<script>
    /**
     * One store, as the platform sees it: its name, its addresses, whether it
     * is open, and the three things only the platform can do to it — give
     * somebody an owner's login, replace its admin token, and delete it.
     *
     * Everything inside the store — its products, its orders, its team — is
     * the store's own panel's, on the store's own host. This screen links
     * there and reaches no further, for D70's reason: a platform token is not
     * a store credential.
     *
     * Deleting is two decisions, as the engine has it: suspend first, then
     * delete with the slug typed out. The form for the second does not exist
     * until the first has been made.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { goto } from "$app/navigation";
    import { untrack } from "svelte";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate } from "$lib/format.js";
    import { platformApi, hostURL, statusClass, statusLabel } from "$lib/platform.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import PlatformSecret from "$lib/components/PlatformSecret.svelte";

    const slug = $derived(page.params.slug);

    let tenant = $state(null);
    let running = $state(true);
    let baseDomain = $state("");
    let loading = $state(true);
    let missing = $state(false);

    async function load() {
        loading = true;
        try {
            const [t, h] = await Promise.all([
                platformApi.get(`/api/platform/tenants/${encodeURIComponent(slug)}`),
                platformApi.get("/api/platform/health"),
            ]);
            tenant = t;
            running = !(h?.unbooted ?? []).includes(t.slug);
            baseDomain = h?.base_domain ?? "";
            missing = false;
            if (!renaming) nameDraft = t.name;
        } catch (err) {
            if (err?.status === 404) missing = true;
            else toast.error(err);
        } finally {
            loading = false;
        }
    }

    $effect(() => {
        slug;
        untrack(() => {
            tenant = null;
            ownerSecret = null;
            tokenSecret = null;
            deleteDraft = "";
            load();
        });
    });

    const suspended = $derived(tenant?.status === "suspended");
    /* The address every store has under the platform's base domain, which no
       request can detach. Empty on a platform run with custom domains only. */
    const platformHost = $derived(tenant && baseDomain ? `${tenant.slug}.${baseDomain}` : "");

    // --------------------------------------------------------------- name

    let nameDraft = $state("");
    let renaming = $state(false);

    async function rename(event) {
        event.preventDefault();
        const name = nameDraft.trim();
        if (!name || name === tenant.name || renaming) return;
        renaming = true;
        try {
            tenant = await platformApi.patch(`/api/platform/tenants/${tenant.slug}`, { name });
            nameDraft = tenant.name;
            toast.success("Renamed");
        } catch (err) {
            toast.error(err);
        } finally {
            renaming = false;
        }
    }

    // -------------------------------------------------------------- status

    let confirmSuspend = $state(false);
    let switching = $state(false);

    async function setStatus(status) {
        switching = true;
        try {
            tenant = await platformApi.patch(`/api/platform/tenants/${tenant.slug}`, { status });
            running = status === "active";
            deleteDraft = "";
            toast.success(status === "active" ? `${tenant.name} is open again` : `${tenant.name} is suspended`);
        } catch (err) {
            toast.error(err);
            // A resume that failed to boot is still recorded as open; read
            // back what the platform now says rather than guessing.
            load();
        } finally {
            switching = false;
        }
    }

    // ------------------------------------------------------------- domains

    let domainDraft = $state("");
    let addingDomain = $state(false);
    let removing = $state(null);
    let confirmRemove = $state(false);

    async function addDomain(event) {
        event.preventDefault();
        const domain = domainDraft.trim().toLowerCase();
        if (!domain || addingDomain) return;
        addingDomain = true;
        try {
            tenant = await platformApi.post(`/api/platform/tenants/${tenant.slug}/domains`, { domain });
            domainDraft = "";
            toast.success(`${domain} reaches ${tenant.name}`);
        } catch (err) {
            toast.error(err);
        } finally {
            addingDomain = false;
        }
    }

    async function removeDomain() {
        const domain = removing;
        try {
            tenant = await platformApi.delete(
                `/api/platform/tenants/${tenant.slug}/domains/${encodeURIComponent(domain)}`,
            );
            toast.success(`${domain} no longer reaches ${tenant.name}`);
        } catch (err) {
            toast.error(err);
        }
    }

    // -------------------------------------------------------------- owners

    let ownerEmail = $state("");
    let ownerPassword = $state("");
    let addingOwner = $state(false);
    let ownerSecret = $state(null);

    async function addOwner(event) {
        event.preventDefault();
        const email = ownerEmail.trim();
        if (!email.includes("@") || addingOwner) return;
        addingOwner = true;
        try {
            const out = await platformApi.post(`/api/platform/tenants/${tenant.slug}/owners`, {
                email,
                password: ownerPassword,
            });
            ownerSecret = out;
            ownerEmail = "";
            ownerPassword = "";
            toast.success(`${out.email} is an owner of ${tenant.name}`);
        } catch (err) {
            toast.error(err);
        } finally {
            addingOwner = false;
        }
    }

    // --------------------------------------------------------------- token

    let confirmRotate = $state(false);
    let tokenSecret = $state(null);

    async function rotate() {
        try {
            const out = await platformApi.post(`/api/platform/tenants/${tenant.slug}/token`);
            tokenSecret = out.admin_token;
            toast.success("The store has a new admin token");
        } catch (err) {
            toast.error(err);
        }
    }

    // -------------------------------------------------------------- delete

    let deleteDraft = $state("");
    let deleting = $state(false);

    async function destroy(event) {
        event.preventDefault();
        if (deleteDraft !== tenant.slug || deleting) return;
        deleting = true;
        try {
            await platformApi.delete(
                `/api/platform/tenants/${tenant.slug}?confirm=${encodeURIComponent(tenant.slug)}`,
            );
            toast.success(`Deleted ${tenant.name}`);
            goto(`${base}/platform`);
        } catch (err) {
            toast.error(err);
            deleting = false;
        }
    }
</script>

<svelte:head><title>{tenant?.name ?? slug} · Platform · GoCommerce</title></svelte:head>

<div class="page page-platform-store platform-page shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <a href="{base}/platform" class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Stores</a>
                <div class="breadcrumb-item platform-title tw:text-2xl tw:font-semibold tw:tracking-tight">
                    <span>{tenant?.name ?? slug}</span>
                    {#if tenant}
                        <!-- Beside the name, where the eye already is: the store's state
                             is the first thing anybody opening it needs. -->
                        <span class="platform-head-labels">
                            <span class="label {statusClass(tenant.status)}">{statusLabel(tenant.status)}</span>
                            {#if !suspended && !running}<span class="label label-danger">Not running</span>{/if}
                        </span>
                    {/if}
                </div>
            </nav>
            <div class="flex-fill"></div>
            {#if tenant}
                <div class="page-header-primary-btns">
                    {#if tenant.host}
                        <a
                            href={hostURL(tenant.host)}
                            target="_blank"
                            rel="noreferrer"
                            class="btn secondary"
                            aria-label="Open the store"
                        >
                            <i class="ri-external-link-line" aria-hidden="true"></i>
                            <span class="txt">Open the store</span>
                        </a>
                    {/if}
                    {#if suspended}
                        <button
                            type="button"
                            class="btn"
                            aria-label="Resume"
                            class:loading={switching}
                            disabled={switching}
                            onclick={() => setStatus("active")}
                        >
                            <i class="ri-play-circle-line" aria-hidden="true"></i>
                            <span class="txt">Resume</span>
                        </button>
                    {:else}
                        <button
                            type="button"
                            class="btn secondary"
                            aria-label="Suspend"
                            class:loading={switching}
                            disabled={switching}
                            onclick={() => (confirmSuspend = true)}
                        >
                            <i class="ri-pause-circle-line" aria-hidden="true"></i>
                            <span class="txt">Suspend</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if missing}
            <div class="platform-empty txt-center">
                <i class="ri-store-2-line txt-hint" aria-hidden="true"></i>
                <h5 class="m-t-sm m-b-xs">There is no store “{slug}”</h5>
                <p class="txt-hint">It may have been deleted, or the address was typed.</p>
                <a href="{base}/platform" class="btn secondary m-t-sm"><span class="txt">Back to stores</span></a>
            </div>
        {:else if !tenant}
            <div class="platform-grid">
                <section class="platform-card tw:rounded-xl tw:border tw:bg-card tw:p-5"><span class="skeleton-loader"></span></section>
                <section class="platform-card tw:rounded-xl tw:border tw:bg-card tw:p-5"><span class="skeleton-loader"></span></section>
            </div>
        {:else}
            {#if suspended}
                <div class="alert warning m-b-base platform-notice" role="status">
                    <p>
                        Suspended. Its data is kept, its background work has stopped and every one of its
                        addresses answers 503 until it is resumed.
                    </p>
                </div>
            {:else if !running}
                <div class="alert danger m-b-base platform-notice" role="alert">
                    <p>
                        Open, but its engine failed to start, so its addresses answer 503. The platform's
                        log names the reason; once it is fixed, suspend and resume to start it again.
                    </p>
                </div>
            {/if}

            <div class="platform-grid" class:faded={loading}>
                <section class="platform-card tw:rounded-xl tw:border tw:bg-card tw:p-5" aria-labelledby="store-details">
                    <h6 class="platform-card-title" id="store-details">Store</h6>
                    <form class="platform-inline" onsubmit={rename}>
                        <div class="field platform-grow">
                            <label for="store-rename">Name</label>
                            <input id="store-rename" type="text" autocomplete="off" bind:value={nameDraft} />
                        </div>
                        <button
                            type="submit"
                            class="btn secondary"
                            class:loading={renaming}
                            disabled={renaming || !nameDraft.trim() || nameDraft.trim() === tenant.name}
                        >
                            <span class="txt">Rename</span>
                        </button>
                    </form>
                    <dl class="platform-facts m-t-sm">
                        <div>
                            <dt>Slug</dt>
                            <dd class="txt-code">{tenant.slug}</dd>
                        </div>
                        <div>
                            <dt>Currency</dt>
                            <dd class="txt-code">{tenant.currency || "The platform's default"}</dd>
                        </div>
                        <div>
                            <dt>Languages</dt>
                            <dd class="txt-code">{tenant.languages?.length ? tenant.languages.join(", ") : "The platform's default"}</dd>
                        </div>
                        <div>
                            <dt>Opened</dt>
                            <dd>{formatDate(tenant.created_at)}</dd>
                        </div>
                        <div>
                            <dt>Last changed</dt>
                            <dd>{formatDate(tenant.updated_at)}</dd>
                        </div>
                    </dl>
                </section>

                <section class="platform-card tw:rounded-xl tw:border tw:bg-card tw:p-5" aria-labelledby="store-domains">
                    <h6 class="platform-card-title" id="store-domains">Addresses</h6>
                    <ul class="platform-domains">
                        {#each tenant.domains ?? [] as domain, i (domain)}
                            <li class="platform-domain">
                                <a href={hostURL(domain)} target="_blank" rel="noreferrer" class="platform-domain-name">{domain}</a>
                                {#if i === 0}<span class="label">Primary</span>{/if}
                                <button
                                    type="button"
                                    class="btn sm circle transparent secondary platform-remove"
                                    aria-label="Detach {domain}"
                                    title="Detach {domain}"
                                    onclick={() => {
                                        removing = domain;
                                        confirmRemove = true;
                                    }}
                                >
                                    <i class="ri-close-line" aria-hidden="true"></i>
                                </button>
                            </li>
                        {/each}
                        {#if platformHost}
                            <li class="platform-domain">
                                <a href={hostURL(platformHost)} target="_blank" rel="noreferrer" class="platform-domain-name">{platformHost}</a>
                                {#if !tenant.domains?.length}<span class="label">Primary</span>{/if}
                                <span class="label platform-builtin">Platform address</span>
                            </li>
                        {/if}
                        {#if !platformHost && !tenant.domains?.length}
                            <li class="platform-domain txt-hint">No address: attach a domain to reach this store.</li>
                        {/if}
                    </ul>
                    <p class="platform-card-text">
                        The first custom domain is the one the store's emails and panel name. The platform
                        address stays attached whatever else is.
                    </p>
                    <form class="platform-inline m-t-sm" onsubmit={addDomain}>
                        <div class="field platform-grow">
                            <label for="store-domain">Attach a domain</label>
                            <input
                                id="store-domain"
                                type="text"
                                autocomplete="off"
                                placeholder="shop.example.com"
                                bind:value={domainDraft}
                            />
                        </div>
                        <button
                            type="submit"
                            class="btn secondary"
                            class:loading={addingDomain}
                            disabled={addingDomain || !domainDraft.trim()}
                        >
                            <span class="txt">Attach</span>
                        </button>
                    </form>
                </section>

                <section class="platform-card tw:rounded-xl tw:border tw:bg-card tw:p-5" aria-labelledby="store-owners">
                    <h6 class="platform-card-title" id="store-owners">Add an owner</h6>
                    <p class="platform-card-text">
                        A login with every right in this store, for a second owner the store asked for, or the
                        way back in when its owners are locked out. The store's team screen lists who has one.
                    </p>
                    {#if suspended}
                        <p class="txt-hint">Resume the store to add an owner: its engine is stopped.</p>
                    {:else}
                        <form class="platform-form-grid" onsubmit={addOwner}>
                            <div class="field">
                                <label for="owner-email">Email</label>
                                <input id="owner-email" type="email" autocomplete="off" bind:value={ownerEmail} />
                            </div>
                            <div class="field">
                                <label for="owner-password">Password</label>
                                <input
                                    id="owner-password"
                                    type="password"
                                    autocomplete="new-password"
                                    placeholder="Generated if left empty"
                                    bind:value={ownerPassword}
                                />
                            </div>
                            <div class="platform-form-actions">
                                <button
                                    type="submit"
                                    class="btn secondary"
                                    class:loading={addingOwner}
                                    disabled={addingOwner || !ownerEmail.includes("@")}
                                >
                                    <i class="ri-user-add-line" aria-hidden="true"></i>
                                    <span class="txt">Add owner</span>
                                </button>
                            </div>
                        </form>
                        {#if ownerSecret?.password}
                            <div class="platform-reveal m-t-sm">
                                <PlatformSecret
                                    id="owner-secret"
                                    label="{ownerSecret.email}'s password"
                                    value={ownerSecret.password}
                                    hint="Shown once. They can change it once signed in."
                                />
                            </div>
                        {/if}
                    {/if}
                </section>

                <section class="platform-card tw:rounded-xl tw:border tw:bg-card tw:p-5" aria-labelledby="store-token">
                    <h6 class="platform-card-title" id="store-token">Admin token</h6>
                    <p class="platform-card-text">
                        The store's own static API token, for the platform's automation and support. It
                        opens this store's admin API and no other. It is never shown after it is made, so
                        the way to a working one is a new one.
                    </p>
                    <button type="button" class="btn secondary" onclick={() => (confirmRotate = true)}>
                        <i class="ri-key-2-line" aria-hidden="true"></i>
                        <span class="txt">Replace the token</span>
                    </button>
                    {#if tokenSecret}
                        <div class="platform-reveal m-t-sm">
                            <PlatformSecret
                                id="token-secret"
                                label="New admin token"
                                value={tokenSecret}
                                hint="Shown once. The old one has already stopped working."
                            />
                        </div>
                    {/if}
                </section>

                <section class="platform-card platform-danger tw:rounded-xl tw:border tw:bg-card tw:p-5" aria-labelledby="store-delete">
                    <h6 class="platform-card-title" id="store-delete">Delete the store</h6>
                    {#if !suspended}
                        <p class="platform-card-text">
                            Deleting drops the store's database schema, its addresses and its uploads, and
                            cannot be undone. Suspend it first; deleting is a second decision, made here
                            once it is suspended.
                        </p>
                    {:else}
                        <p class="platform-card-text">
                            Drops the store's database schema — every product, order and customer in it —
                            its addresses and its uploads. This cannot be undone. Type
                            <span class="txt-code">{tenant.slug}</span> to confirm.
                        </p>
                        <form class="platform-inline" onsubmit={destroy}>
                            <div class="field platform-grow">
                                <label for="delete-confirm">Slug</label>
                                <input
                                    id="delete-confirm"
                                    type="text"
                                    class="txt-code"
                                    autocomplete="off"
                                    spellcheck="false"
                                    bind:value={deleteDraft}
                                />
                            </div>
                            <button
                                type="submit"
                                class="btn danger"
                                class:loading={deleting}
                                disabled={deleting || deleteDraft !== tenant.slug}
                            >
                                <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                <span class="txt">Delete for good</span>
                            </button>
                        </form>
                    {/if}
                </section>
            </div>
        {/if}
    </div>
</div>

<Confirm
    bind:open={confirmSuspend}
    title="Suspend {tenant?.name}?"
    message="Its shoppers and its operators are turned away with a 503 and its background work stops. Nothing is deleted; resume it to open it again."
    confirmLabel="Suspend"
    danger
    onconfirm={() => setStatus("suspended")}
/>

<Confirm
    bind:open={confirmRemove}
    title="Detach {removing}?"
    message="Requests to it stop reaching this store at once. The domain can be attached again, to this store or another."
    confirmLabel="Detach"
    danger
    onconfirm={removeDomain}
/>

<Confirm
    bind:open={confirmRotate}
    title="Replace {tenant?.name}'s admin token?"
    message="The store restarts with a new token and the current one stops working at once — anything still using it is refused until it is given the new one."
    confirmLabel="Replace"
    danger
    onconfirm={rotate}
/>
