<script>
    /**
     * The store's own domains (D83).
     *
     * A domain is added, proven by a TXT record at _gocommerce.<domain> that
     * the engine looks up itself when Verify is pressed, and only then routes
     * shoppers to this store. Verified and primary are the engine's to say;
     * this screen only asks.
     */
    import { can, request } from "$lib/api.js";
    import { copyText } from "$lib/clipboard.js";
    import { formatDate } from "$lib/format.js";
    import { loadStores, myStores } from "$lib/stores.svelte.js";
    import { toast } from "$lib/toast.svelte.js";

    const writable = $derived(can("store.write"));
    // <slug>.<base domain>: the engine's to say, and empty on an install
    // without one.
    const subdomain = $derived(myStores.current?.subdomain ?? "");

    let domains = $state([]);
    let loading = $state(true);
    let host = $state("");
    let adding = $state(false);
    // Which row is busy, and with what, so only that button spins.
    let busy = $state({ host: "", what: "" });
    // A failed verification is answered on its own row, where the record is.
    let notFound = $state({});
    let confirmRemove = $state("");

    $effect(() => {
        load();
    });

    async function load() {
        loading = true;
        try {
            domains = (await request("GET", "/api/admin/domains")) ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function add(event) {
        event.preventDefault();
        if (adding || !host.trim()) return;
        adding = true;
        try {
            const d = await request("POST", "/api/admin/domains", { body: { host: host.trim() } });
            host = "";
            toast.success(`${d.host} added — now create its TXT record`);
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            adding = false;
        }
    }

    async function act(d, what) {
        busy = { host: d.host, what };
        notFound = { ...notFound, [d.host]: "" };
        try {
            if (what === "verify") {
                await request("POST", `/api/admin/domains/${encodeURIComponent(d.host)}/verify`);
                toast.success(`${d.host} is verified and now reaches this store`);
            } else if (what === "primary") {
                await request("POST", `/api/admin/domains/${encodeURIComponent(d.host)}/primary`);
                toast.success(`${d.host} is now the store's primary domain`);
            } else if (what === "remove") {
                await request("DELETE", `/api/admin/domains/${encodeURIComponent(d.host)}`);
                confirmRemove = "";
                toast.success(`${d.host} removed`);
            }
            await load();
            // The account menu's "Visit store" follows the primary domain.
            loadStores().catch(() => {});
        } catch (err) {
            if (what === "verify" && err?.code === "not_verified") {
                notFound = { ...notFound, [d.host]: err.message };
            } else {
                toast.error(err);
            }
        } finally {
            busy = { host: "", what: "" };
        }
    }

    async function copy(text) {
        if (await copyText(text)) toast.success("Copied");
        else toast.error("Could not copy; select it and copy by hand.");
    }
</script>

<svelte:head><title>Domains · GoCommerce</title></svelte:head>

<div class="page page-domains shopify-skin">
    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item txt-sm txt-hint">Settings</div>
                <div class="breadcrumb-item feed-title">Domains</div>
            </nav>
        </header>

        {#if writable}
            <section class="card feed-card">
                <h2 class="feed-card-title">Add a domain</h2>
                <p class="txt-hint txt-sm feed-card-sub">
                    A domain you own, such as shop.example.com. Point it at this server as you would
                    any site, then prove it is yours with one DNS record — until then it reaches nothing.
                </p>
                <form class="domain-add" onsubmit={add}>
                    <input
                        type="text"
                        placeholder="shop.example.com"
                        aria-label="Domain"
                        autocomplete="off"
                        spellcheck="false"
                        bind:value={host}
                    />
                    <button class="btn sm" class:loading={adding} disabled={adding || !host.trim()}>
                        <span class="txt">Add domain</span>
                    </button>
                </form>
            </section>
        {/if}

        <section class="card feed-card">
            <h2 class="feed-card-title">This store's domains</h2>
            {#if subdomain}
                <div class="domain-list">
                    <div class="domain-row verified">
                        <div class="domain-head">
                            <span class="domain-host txt-ellipsis">{subdomain}</span>
                            <span class="label">Included</span>
                            <div class="domain-actions">
                                <a class="btn sm transparent secondary" href="https://{subdomain}" target="_blank" rel="noreferrer">
                                    <i class="ri-external-link-line" aria-hidden="true"></i>
                                    <span class="txt">Visit</span>
                                </a>
                            </div>
                        </div>
                        <p class="txt-hint txt-sm m-t-5">Every store has this address. It needs no DNS record.</p>
                    </div>
                </div>
            {/if}
            {#if loading && !domains.length}
                <div class="block txt-center p-base"><span class="loader lg"></span></div>
            {:else if !domains.length && !subdomain}
                <p class="txt-hint m-t-sm">
                    None yet. The store is reached at the install's own address until a domain is
                    added and verified.
                </p>
            {:else if domains.length}
                <div class="domain-list">
                    {#each domains as d (d.host)}
                        <div class="domain-row" class:verified={d.verified}>
                            <div class="domain-head">
                                <span class="domain-host txt-ellipsis">{d.host}</span>
                                {#if d.primary}
                                    <span class="label success">Primary</span>
                                {/if}
                                {#if d.verified}
                                    <span class="label success" title="Verified {formatDate(d.verified_at)}">Verified</span>
                                {:else}
                                    <span class="label warning">Waiting for DNS</span>
                                {/if}
                                <div class="domain-actions">
                                    {#if writable && !d.verified}
                                        <button
                                            type="button"
                                            class="btn sm"
                                            class:loading={busy.host === d.host && busy.what === "verify"}
                                            disabled={!!busy.host}
                                            onclick={() => act(d, "verify")}
                                        >
                                            <span class="txt">Verify</span>
                                        </button>
                                    {/if}
                                    {#if writable && d.verified && !d.primary}
                                        <button
                                            type="button"
                                            class="btn sm secondary"
                                            class:loading={busy.host === d.host && busy.what === "primary"}
                                            disabled={!!busy.host}
                                            onclick={() => act(d, "primary")}
                                        >
                                            <span class="txt">Make primary</span>
                                        </button>
                                    {/if}
                                    {#if d.verified}
                                        <a class="btn sm transparent secondary" href="https://{d.host}" target="_blank" rel="noreferrer">
                                            <i class="ri-external-link-line" aria-hidden="true"></i>
                                            <span class="txt">Visit</span>
                                        </a>
                                    {/if}
                                    {#if writable}
                                        {#if confirmRemove === d.host}
                                            <button
                                                type="button"
                                                class="btn sm danger"
                                                class:loading={busy.host === d.host && busy.what === "remove"}
                                                disabled={!!busy.host}
                                                onclick={() => act(d, "remove")}
                                            >
                                                <span class="txt">Remove {d.host}</span>
                                            </button>
                                            <button type="button" class="btn sm transparent secondary" onclick={() => (confirmRemove = "")}>
                                                <span class="txt">Keep</span>
                                            </button>
                                        {:else}
                                            <button
                                                type="button"
                                                class="btn sm transparent secondary"
                                                aria-label="Remove {d.host}"
                                                onclick={() => (confirmRemove = d.host)}
                                            >
                                                <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                            </button>
                                        {/if}
                                    {/if}
                                </div>
                            </div>
                            {#if !d.verified}
                                <!-- The record to create, where the person creating it
                                     will look: on the domain's own row. -->
                                <div class="domain-record">
                                    <p class="txt-hint txt-sm">
                                        At your DNS provider, add {d.point_type ? "these records" : "this record"},
                                        then press Verify. It can take a few minutes to appear. Once verified, the
                                        domain gets its certificate on its own.
                                    </p>
                                    {#if d.point_type}
                                        <!-- Sends visitors here. Named by the engine from the
                                             install's own address, so it is never a guess. -->
                                        <div class="domain-record-grid">
                                            <span class="txt-hint">Type</span>
                                            <code>{d.point_type}</code>
                                            <span></span>
                                            <span class="txt-hint">Name</span>
                                            <code class="txt-ellipsis">{d.point_name}</code>
                                            <button type="button" class="btn xs transparent secondary" aria-label="Copy name" onclick={() => copy(d.point_name)}>
                                                <i class="ri-file-copy-line" aria-hidden="true"></i>
                                            </button>
                                            <span class="txt-hint">Value</span>
                                            <code class="txt-ellipsis">{d.point_value}</code>
                                            <button type="button" class="btn xs transparent secondary" aria-label="Copy value" onclick={() => copy(d.point_value)}>
                                                <i class="ri-file-copy-line" aria-hidden="true"></i>
                                            </button>
                                        </div>
                                    {/if}
                                    <div class="domain-record-grid">
                                        <span class="txt-hint">Type</span>
                                        <code>TXT</code>
                                        <span></span>
                                        <span class="txt-hint">Name</span>
                                        <code class="txt-ellipsis">{d.txt_name}</code>
                                        <button type="button" class="btn xs transparent secondary" aria-label="Copy name" onclick={() => copy(d.txt_name)}>
                                            <i class="ri-file-copy-line" aria-hidden="true"></i>
                                        </button>
                                        <span class="txt-hint">Value</span>
                                        <code class="txt-ellipsis">{d.txt_value}</code>
                                        <button type="button" class="btn xs transparent secondary" aria-label="Copy value" onclick={() => copy(d.txt_value)}>
                                            <i class="ri-file-copy-line" aria-hidden="true"></i>
                                        </button>
                                    </div>
                                    {#if notFound[d.host]}
                                        <p class="domain-not-found" role="alert">{notFound[d.host]}</p>
                                    {/if}
                                </div>
                            {/if}
                        </div>
                    {/each}
                </div>
            {/if}
        </section>
    </div>
</div>
