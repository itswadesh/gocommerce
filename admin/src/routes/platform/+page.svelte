<script>
    /**
     * Every store on the platform, and the way to open a new one.
     *
     * The platform lists its stores whole — there is no paging on
     * GET /api/platform/tenants — so the search and the status filter narrow
     * what has already arrived rather than asking again. A platform with
     * thousands of stores will want the engine to page this; until then a
     * round trip per keystroke would buy nothing.
     *
     * Creating a store is all or nothing on the engine's side: its schema, its
     * migrations, its first owner and its domains, or none of them. What comes
     * back — the owner's generated password and the store's own admin token —
     * is shown once, in the drawer that asked for it, because the platform
     * keeps no copy it could show again.
     */
    import { base } from "$app/paths";
    import { onMount } from "svelte";
    import { goto } from "$app/navigation";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate } from "$lib/format.js";
    import { platformApi, hostURL, STATUSES, statusClass, statusLabel } from "$lib/platform.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";
    import PlatformSecret from "$lib/components/PlatformSecret.svelte";

    const list = listState({ q: "", status: "" });

    let tenants = $state([]);
    let health = $state(null);
    let loading = $state(true);
    let draft = $state(list.params.q);

    async function load() {
        loading = true;
        try {
            // Health beside the list: a store that failed to boot is "active"
            // in the list and silent everywhere else, and this is the one
            // screen a platform operator is sure to look at.
            const [rows, h] = await Promise.all([
                platformApi.get("/api/platform/tenants"),
                platformApi.get("/api/platform/health"),
            ]);
            tenants = rows ?? [];
            health = h;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    onMount(load);

    const unbooted = $derived(new Set(health?.unbooted ?? []));

    const rows = $derived.by(() => {
        const q = list.params.q.toLowerCase();
        return tenants.filter((t) => {
            if (list.params.status && t.status !== list.params.status) return false;
            if (!q) return true;
            return (
                t.name.toLowerCase().includes(q) ||
                t.slug.includes(q) ||
                (t.domains ?? []).some((d) => d.includes(q))
            );
        });
    });

    const counts = $derived({
        all: tenants.length,
        suspended: tenants.filter((t) => t.status === "suspended").length,
    });

    const open = (slug) => goto(`${base}/platform/${slug}`);

    // ------------------------------------------------------------ new store

    let creating = $state(false);
    let saving = $state(false);
    let errors = $state({});
    let form = $state(blank());
    /* Once somebody types a slug of their own, the name stops writing it. */
    let slugTouched = $state(false);
    /* What provisioning handed back: shown once, then gone with the drawer. */
    let created = $state(null);

    function blank() {
        return {
            name: "",
            slug: "",
            owner_email: "",
            owner_password: "",
            currency: "",
            languages: "",
            domains: "",
        };
    }

    function openNew() {
        form = blank();
        errors = {};
        slugTouched = false;
        created = null;
        creating = true;
    }

    function closeNew() {
        creating = false;
        // The credentials leave the page with the drawer; a refresh would lose
        // them anyway, and leaving them in memory invites a second look later
        // that the platform cannot honour.
        if (created) {
            created = null;
            load();
        }
    }

    function slugFrom(name) {
        return name
            .toLowerCase()
            .normalize("NFKD")
            .replace(/[̀-ͯ]/g, "")
            .replace(/[^a-z0-9]+/g, "-")
            .replace(/^-+|-+$/g, "")
            .slice(0, 40)
            .replace(/-+$/, "");
    }

    $effect(() => {
        if (!slugTouched) form.slug = slugFrom(form.name);
    });

    const SLUG = /^[a-z0-9]+(-[a-z0-9]+)*$/;
    const RESERVED = ["platform", "api", "www", "admin"];

    const splitList = (text) =>
        text
            .split(/[\s,]+/)
            .map((s) => s.trim())
            .filter(Boolean);

    async function create(event) {
        event?.preventDefault();
        if (saving) return;
        errors = {};
        const slug = form.slug.trim().toLowerCase();
        if (!form.name.trim()) errors.name = "A store needs a name.";
        if (!SLUG.test(slug) || slug.length > 40)
            errors.slug = "Lower-case letters and digits, joined by single hyphens, at most 40 characters.";
        else if (RESERVED.includes(slug)) errors.slug = `“${slug}” is kept for the platform's own hosts.`;
        if (!form.owner_email.includes("@")) errors.owner_email = "The store's first owner signs in with this address.";
        const currency = form.currency.trim().toUpperCase();
        if (currency && !/^[A-Z]{3}$/.test(currency)) errors.currency = "Three letters, like USD or INR.";
        if (Object.keys(errors).length) return;

        const body = {
            slug,
            name: form.name.trim(),
            owner_email: form.owner_email.trim(),
        };
        if (form.owner_password) body.owner_password = form.owner_password;
        if (currency) body.currency = currency;
        const languages = splitList(form.languages);
        if (languages.length) body.languages = languages;
        const domains = splitList(form.domains.toLowerCase());
        if (domains.length) body.domains = domains;

        saving = true;
        try {
            created = await platformApi.post("/api/platform/tenants", body);
            toast.success(`Opened ${created.tenant.name}`);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function search(event) {
        event.preventDefault();
        list.set({ q: draft.trim() });
    }
</script>

<svelte:head><title>Stores · Platform · GoCommerce</title></svelte:head>

<div class="page page-platform platform-page shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Platform</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Stores</div>
            </nav>
            <div class="flex-fill"></div>
            <div class="page-header-primary-btns">
                <button
                    type="button"
                    class="btn secondary"
                    aria-label="Refresh"
                    class:loading
                    disabled={loading}
                    onclick={load}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                    <span class="txt">Refresh</span>
                </button>
                <button type="button" class="btn" aria-label="New store" onclick={openNew}>
                    <i class="ri-add-line" aria-hidden="true"></i>
                    <span class="txt">New store</span>
                </button>
            </div>
        </header>

        <p class="field-help m-b-base platform-notice">
            Every store this platform serves. Each is a whole store of its own — its own operators,
            panel, admin token and data — reached at its own address. Nothing here signs you in to
            one: a store's owners do that on the store's own host.
        </p>

        {#if health}
            <div class="platform-stats m-b-base" aria-live="polite">
                <div class="platform-stat">
                    <span class="platform-stat-value">{health.stores}</span>
                    <span class="platform-stat-label">{health.stores === 1 ? "store" : "stores"}</span>
                </div>
                <div class="platform-stat">
                    <span class="platform-stat-value">{health.running}</span>
                    <span class="platform-stat-label">running</span>
                </div>
                <div class="platform-stat">
                    <span class="platform-stat-value">{counts.suspended}</span>
                    <span class="platform-stat-label">suspended</span>
                </div>
                <div class="platform-stat" class:platform-stat-bad={unbooted.size > 0}>
                    <span class="platform-stat-value">{unbooted.size}</span>
                    <span class="platform-stat-label">failed to start</span>
                </div>
            </div>
            {#if unbooted.size}
                <div class="alert danger m-b-base" role="alert">
                    <p>
                        {[...unbooted].join(", ")}
                        {unbooted.size === 1 ? "is" : "are"} open but not running: the engine failed
                        to boot, so every host answers 503. The platform's log says why; suspend and
                        resume to try again once it is fixed.
                    </p>
                </div>
            {/if}
        {/if}

        <div class="platform-filters m-b-base">
            <form class="fields platform-search" onsubmit={search} role="search">
                <div class="field">
                    <label for="store-search">Search</label>
                    <input
                        id="store-search"
                        type="search"
                        placeholder="Name, slug or domain"
                        autocomplete="off"
                        bind:value={draft}
                        oninput={(e) => list.set({ q: e.currentTarget.value.trim() })}
                    />
                </div>
            </form>
            <div class="field platform-status-filter">
                <label for="store-status">Status</label>
                <Select
                    id="store-status"
                    value={list.params.status}
                    options={[
                        { value: "", label: `Every store (${counts.all})`, short: "Every store" },
                        ...STATUSES.map((s) => ({ value: s.value, label: s.label })),
                    ]}
                    onchange={(v) => list.set({ status: v })}
                />
            </div>
        </div>

        <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && tenants.length > 0}>
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        <th class="col-field-name-id">Store</th>
                        <th>Address</th>
                        <th class="min-width">Status</th>
                        <th class="min-width">Currency</th>
                        <th class="min-width">Languages</th>
                        <th class="min-width">Opened</th>
                    </tr>
                </thead>
                <tbody>
                    {#if loading && !tenants.length}
                        {#each Array(4) as _, i (i)}
                            <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {/if}
                    {#each rows as t (t.slug)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={() => open(t.slug)}
                            onkeydown={(e) => rowKey(e, () => open(t.slug))}
                        >
                            <td class="col-field-name-id" data-name="Store">
                                <div class="row-name row-name-stacked">
                                    <a
                                        href="{base}/platform/{t.slug}"
                                        class="txt-bold"
                                        onclick={(e) => e.stopPropagation()}>{t.name}</a
                                    >
                                    <span class="txt-hint txt-sm txt-code">{t.slug}</span>
                                </div>
                            </td>
                            <td data-name="Address">
                                {#if t.host}
                                    <a
                                        href={hostURL(t.host)}
                                        target="_blank"
                                        rel="noreferrer"
                                        class="platform-host"
                                        onclick={(e) => e.stopPropagation()}
                                    >
                                        <span class="txt-ellipsis">{t.host}</span>
                                        <i class="ri-external-link-line" aria-hidden="true"></i>
                                    </a>
                                    {#if (t.domains?.length ?? 0) > 1}
                                        <span class="txt-hint txt-sm">+{t.domains.length - 1} more</span>
                                    {/if}
                                {:else}
                                    <span class="txt-hint">No address</span>
                                {/if}
                            </td>
                            <td class="min-width" data-name="Status">
                                <div class="inline-flex gap-5">
                                    <span class="label {statusClass(t.status)}">{statusLabel(t.status)}</span>
                                    {#if unbooted.has(t.slug)}
                                        <span class="label label-danger">Not running</span>
                                    {/if}
                                </div>
                            </td>
                            <td class="min-width txt-code" data-name="Currency">
                                {t.currency || "—"}
                            </td>
                            <td class="min-width" data-name="Languages">
                                {#if t.languages?.length}
                                    <span class="txt-code">{t.languages.join(", ")}</span>
                                {:else}
                                    <span class="txt-hint">Default</span>
                                {/if}
                            </td>
                            <td class="min-width txt-hint txt-sm" data-name="Opened">
                                {formatDate(t.created_at, { withTime: false })}
                            </td>
                        </tr>
                    {/each}
                    {#if !loading && !rows.length}
                        <tr>
                            <td colspan="6" class="txt-hint txt-center p-base">
                                {#if list.params.q}
                                    No store matches “{list.params.q}”.
                                {:else if list.params.status}
                                    No store is {statusLabel(list.params.status).toLowerCase()}.
                                {:else}
                                    The platform has no stores yet. Open the first with New store.
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>
    </div>
</div>

<Drawer open={creating} size="sm" title={created ? `${created.tenant.name} is open` : "New store"} onclose={closeNew}>
    {#if created}
        <div class="platform-created">
            <div class="alert success m-b-base" role="status">
                <p>
                    The store is running. These are shown once — the platform keeps no copy it could
                    show again — so keep them somewhere safe before closing this.
                </p>
            </div>
            <dl class="platform-facts m-b-base">
                <div>
                    <dt>Address</dt>
                    <dd>
                        <a href={hostURL(created.tenant.host)} target="_blank" rel="noreferrer">{created.tenant.host}</a>
                    </dd>
                </div>
                <div>
                    <dt>Owner</dt>
                    <dd>{created.owner_email}</dd>
                </div>
            </dl>
            {#if created.owner_password}
                <PlatformSecret
                    id="created-password"
                    label="Owner's password"
                    value={created.owner_password}
                    hint="Generated. The owner can change it once signed in."
                />
            {:else}
                <p class="field-help m-b-base">The owner signs in with the password you gave.</p>
            {/if}
            <PlatformSecret
                id="created-token"
                label="Store admin token"
                value={created.admin_token}
                hint="Opens this store's admin API, and no other store's. Rotate it from the store's page."
            />
        </div>
    {:else}
        <form id="store-form" class="platform-form" onsubmit={create} novalidate>
            <div class="field required" class:error={!!errors.name}>
                <label for="store-name">Name</label>
                <input id="store-name" type="text" autocomplete="off" bind:value={form.name} />
            </div>
            {#if errors.name}<div class="field-help txt-danger">{errors.name}</div>{/if}

            <div class="field required m-t-sm" class:error={!!errors.slug}>
                <label for="store-slug">Slug</label>
                <input
                    id="store-slug"
                    type="text"
                    class="txt-code"
                    autocomplete="off"
                    maxlength="40"
                    bind:value={form.slug}
                    oninput={() => (slugTouched = true)}
                />
            </div>
            <div class="field-help">
                {#if errors.slug}
                    <span class="txt-danger">{errors.slug}</span>
                {:else}
                    The store's name on the platform, its database schema and its first address
                    {#if health?.base_domain}— <span class="txt-code">{form.slug || "slug"}.{health.base_domain}</span>{/if}.
                    It cannot be changed later.
                {/if}
            </div>

            <h6 class="section-title">
                <i class="ri-user-star-line" aria-hidden="true"></i>
                First owner
            </h6>
            <div class="field required" class:error={!!errors.owner_email}>
                <label for="store-owner">Email</label>
                <input id="store-owner" type="email" autocomplete="off" bind:value={form.owner_email} />
            </div>
            {#if errors.owner_email}<div class="field-help txt-danger">{errors.owner_email}</div>{/if}
            <div class="field m-t-sm">
                <label for="store-password">Password</label>
                <input
                    id="store-password"
                    type="password"
                    autocomplete="new-password"
                    placeholder="Generated if left empty"
                    bind:value={form.owner_password}
                />
            </div>
            <div class="field-help">
                A generated one is shown once, when the store opens. Either way the owner can change
                it from their account.
            </div>

            <h6 class="section-title">
                <i class="ri-global-line" aria-hidden="true"></i>
                Market
            </h6>
            <div class="field" class:error={!!errors.currency}>
                <label for="store-currency">Currency</label>
                <input
                    id="store-currency"
                    type="text"
                    class="txt-code"
                    autocomplete="off"
                    maxlength="3"
                    placeholder="The platform's default"
                    bind:value={form.currency}
                />
            </div>
            <div class="field-help">
                {#if errors.currency}
                    <span class="txt-danger">{errors.currency}</span>
                {:else}
                    Fixed once the store takes an order, as every store's is — choose it now.
                {/if}
            </div>
            <div class="field m-t-sm">
                <label for="store-languages">Languages</label>
                <input
                    id="store-languages"
                    type="text"
                    class="txt-code"
                    autocomplete="off"
                    placeholder="The platform's default"
                    bind:value={form.languages}
                />
            </div>
            <div class="field-help">Codes separated by commas, the default first — <span class="txt-code">en, hi</span>.</div>
            <div class="field m-t-sm">
                <label for="store-domains">Custom domains</label>
                <input
                    id="store-domains"
                    type="text"
                    autocomplete="off"
                    placeholder="shop.example.com"
                    bind:value={form.domains}
                />
            </div>
            <div class="field-help">
                Optional, separated by commas. The first is the address the store's emails and panel
                name. Point each one's DNS at the platform; more can be attached later.
            </div>
        </form>
    {/if}

    {#snippet footer()}
        {#if created}
            <a href={hostURL(created.tenant.host)} target="_blank" rel="noreferrer" class="btn transparent m-r-auto">
                <i class="ri-external-link-line" aria-hidden="true"></i>
                <span class="txt">Open the store</span>
            </a>
            <button
                type="button"
                class="btn"
                onclick={() => {
                    const slug = created.tenant.slug;
                    closeNew();
                    open(slug);
                }}
            >
                <span class="txt">I have kept them</span>
            </button>
        {:else}
            <button type="button" class="btn transparent m-r-auto" onclick={closeNew}>
                <span class="txt">Cancel</span>
            </button>
            <button type="submit" form="store-form" class="btn" class:loading={saving} disabled={saving}>
                <span class="txt">Open the store</span>
            </button>
        {/if}
    {/snippet}
</Drawer>
