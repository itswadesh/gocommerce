<script>
    /**
     * Feeds: the catalogue as Google, Meta, Pinterest and TikTok read it.
     *
     * This was a PluginPage — a switch, a settings drawer and two addresses —
     * and it answered none of the questions somebody actually arrives with.
     * Is anything in the file? Why did Google hold half my items? What do I
     * paste, and where? A feed is unusual among plugins in that switching it
     * on is the easy tenth of the job; the other nine tenths happen in
     * somebody else's dashboard, days later, with an error list that names
     * fields rather than products.
     *
     * So the screen is built around the file's contents rather than the
     * plugin's state. The counts come from /api/admin/x/feeds/status, which
     * walks the catalogue exactly the way the feed does, so the number here and
     * the number of items in the file cannot disagree. The three "missing"
     * counts are the three things platforms reject items over, and they read as
     * this store's own list of work rather than as general advice.
     *
     * The platform steps are here and not in a docs site for the same reason
     * the addresses are: the person doing this has the panel open and the
     * platform open, and nothing else.
     */
    import { can, request } from "$lib/api.js";
    import { activateNote, filled } from "$lib/plugin-ready.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PluginFields from "$lib/components/PluginFields.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const PLUGIN = "product-feeds";

    const allowed = $derived(can("plugins.read"));
    const writable = $derived(can("plugins.write"));
    const missing = $derived(modulesKnown() && !hasModule("feeds"));

    let plugin = $state(null);
    let status = $state(null);
    let loading = $state(true);
    let working = $state(false);
    let saving = $state(false);
    let checking = $state(false);
    let form = $state({});
    let tab = $state("google");

    const origin = $derived(typeof window === "undefined" ? "" : window.location.origin);

    $effect(() => {
        if (allowed && hasModule("feeds")) load();
    });

    async function load() {
        loading = true;
        try {
            plugin = await request("GET", `/api/admin/plugins/${PLUGIN}`);
            resetForm();
            await check();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* A copy, so Save is a decision rather than a side effect of typing. */
    function resetForm() {
        const draft = {};
        for (const f of plugin?.fields ?? []) {
            const v = plugin.settings?.[f.key];
            draft[f.key] = f.kind === "bool" ? !!v : (v ?? f.default ?? "");
        }
        form = draft;
    }

    async function check() {
        checking = true;
        try {
            status = await request("GET", "/api/admin/x/feeds/status");
        } catch (err) {
            toast.error(err);
        } finally {
            checking = false;
        }
    }

    const note = $derived(plugin && !plugin.enabled ? activateNote(plugin) : "");

    /*
     * As on Sitemap: folded settings still show what they are set to, and
     * open by themselves while a required one is empty.
     */
    let settingsOpen = $state(false);
    $effect(() => {
        if (note || (plugin?.enabled && !plugin.configured)) settingsOpen = true;
    });
    const settingsPreview = $derived(
        (plugin?.fields ?? []).map((f) => {
            const v = plugin.settings?.[f.key];
            const set = filled(v);
            return { label: f.label, value: set ? String(v) : f.required ? "not set" : "default", missing: !set && f.required };
        }),
    );

    async function toggle() {
        working = true;
        try {
            plugin = await request("PATCH", `/api/admin/plugins/${PLUGIN}`, {
                body: { enabled: !plugin.enabled },
            });
            toast.success(plugin.enabled ? "Feeds activated" : "Feeds deactivated");
            await check();
        } catch (err) {
            toast.error(err);
        } finally {
            working = false;
        }
    }

    // Enter in a field saves, like the Save button.
    function submit(e) {
        e.preventDefault();
        if (writable && !saving) save();
    }

    async function save() {
        saving = true;
        try {
            const settings = {};
            for (const f of plugin?.fields ?? []) {
                let v = form[f.key];
                if (f.kind === "number") v = v === "" || v === null ? "" : Number(v);
                settings[f.key] = v;
            }
            plugin = await request("PATCH", `/api/admin/plugins/${PLUGIN}`, { body: { settings } });
            resetForm();
            toast.success("Feed settings saved");
            await check();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function copy(url) {
        try {
            await navigator.clipboard.writeText(url);
            toast.success("Copied");
        } catch {
            toast.error("Could not copy; select it and copy by hand.");
        }
    }

    const state = $derived.by(() => {
        if (!plugin) return { label: "", cls: "" };
        if (!plugin.enabled) return { label: "Inactive", cls: "" };
        if (!plugin.configured) return { label: "Needs a storefront URL", cls: "label-warning" };
        return { label: "Active", cls: "label-success" };
    });

    const serving = $derived(!!status?.enabled && !!status?.configured);
    const nf = new Intl.NumberFormat();

    const LINKS = [
        {
            key: "google",
            label: "Google Merchant",
            path: "/x/feeds/google.xml",
            hint: "RSS 2.0 with the g: namespace",
        },
        {
            key: "meta",
            label: "Meta catalogue",
            path: "/x/feeds/meta.csv",
            hint: "CSV, for Commerce Manager",
        },
    ];

    /* What an operator has to do on the other side, which is where the work
       actually is. Pinterest and TikTok read Google's RSS unchanged, so they
       get the same address and a shorter list. */
    const PLATFORMS = [
        {
            key: "google",
            name: "Google Merchant Center",
            reads: "/x/feeds/google.xml",
            blurb: "Powers Shopping ads and the free product listings in Google Search.",
            before: [
                "A Merchant Center account with this store's domain claimed and verified.",
                "Shipping and return policies filled in for every country you sell to.",
            ],
            steps: [
                "Open Merchant Center and go to Products, then Data sources.",
                "Choose Add product source, then Add products from a file.",
                "Pick Scheduled fetch, so Google collects the file itself rather than you uploading one.",
                "Paste the Google Merchant address above, then set the country and language your products sell in.",
                "Pick any fetch time. This feed is rebuilt on every request, so there is no build window to wait for.",
                "Save, run Fetch now, and read Diagnostics — that is where per-item rejections appear.",
            ],
            docs: "https://support.google.com/merchants/answer/7439058",
        },
        {
            key: "meta",
            name: "Meta Commerce Manager",
            reads: "/x/feeds/meta.csv",
            blurb: "Feeds the catalogue behind Facebook and Instagram shops and ads.",
            before: [
                "A Business Manager account with a catalogue created.",
                "This store's domain verified under Business settings.",
            ],
            steps: [
                "Open Commerce Manager and select the catalogue.",
                "Go to Catalogue, then Data sources, then Add items.",
                "Choose Scheduled feed and paste the Meta catalogue address above.",
                "Set how often Meta fetches — hourly, daily or weekly all work here.",
                "Name the feed and finish, then open Data quality after the first fetch lands.",
            ],
            docs: "https://www.facebook.com/business/help/125074381480892",
        },
        {
            key: "pinterest",
            name: "Pinterest",
            reads: "/x/feeds/google.xml",
            blurb: "Reads Google's RSS unchanged, so it takes the same address.",
            before: [
                "A Pinterest business account with this store's website claimed.",
            ],
            steps: [
                "Open Pinterest Business and go to Catalogues.",
                "Choose Add a data source, then the option to paste a link.",
                "Paste the Google Merchant address above.",
                "Set the country and currency to match this store, then save.",
            ],
            docs: "https://help.pinterest.com/en/business/article/data-source-ingestion",
        },
        {
            key: "tiktok",
            name: "TikTok",
            reads: "/x/feeds/google.xml",
            blurb: "Also reads Google's RSS, for catalogue ads and Shop listings.",
            before: [
                "A TikTok Business or Shop account with this store's domain verified.",
            ],
            steps: [
                "Open TikTok Ads Manager and go to Assets, then Catalogue.",
                "Create a catalogue, then choose Add products and Scheduled feed.",
                "Paste the Google Merchant address above and set the fetch frequency.",
                "Save, then check the catalogue's diagnostics for held items.",
            ],
            docs: "https://ads.tiktok.com/help/article/catalog-manager",
        },
    ];

    const current = $derived(PLATFORMS.find((p) => p.key === tab) ?? PLATFORMS[0]);

    /* The rejection list, with this store's own numbers in it. A count of zero
       still shows: "no items are missing a picture" is the useful half of the
       answer when a platform has held something and you are looking for why. */
    const health = $derived.by(() => {
        if (!status) return [];
        return [
            {
                key: "image",
                count: status.missing_image ?? 0,
                title: "No picture",
                fix: "Platforms want at least 500×500 pixels, with no watermark or promotional text. Add an image to the product.",
            },
            {
                key: "brand",
                count: status.missing_brand ?? 0,
                title: "No brand",
                fix: "Set the product's vendor, or fill in the fallback brand below so every item carries one.",
            },
            {
                key: "identifier",
                count: status.missing_identifier ?? 0,
                title: "No identifier",
                fix: "Google takes a barcode, or a brand and an MPN together. Set the variant's barcode, or give the product a vendor — the feed already sends the SKU as the MPN.",
            },
        ];
    });
    const trouble = $derived(health.filter((h) => h.count > 0));

    // The contents fold opens by itself when items will be held: that list is
    // the work to do.
    let contentsOpen = $state(false);
    $effect(() => {
        if (trouble.length) contentsOpen = true;
    });
</script>

<svelte:head><title>{titled("Feeds")}</title></svelte:head>

<div class="page page-feeds shopify-skin">
    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item txt-sm txt-hint">Content</div>
                <div class="breadcrumb-item feed-title">Feeds</div>
            </nav>
            {#if plugin}
                <div class="feed-head-chips">
                    <span class="label {state.cls}">{state.label}</span>
                    <span class="label">Google Merchant</span>
                    <span class="label">Meta catalogue</span>
                </div>
                {#if writable}
                    <div class="page-header-primary-btns">
                        <button
                            type="button"
                            class="btn sm {plugin.enabled ? 'secondary' : ''}"
                            class:loading={working}
                            disabled={working || !!note}
                            title={note || undefined}
                            onclick={toggle}
                        >
                            <span class="txt">{plugin.enabled ? "Deactivate" : "Activate"}</span>
                        </button>
                    </div>
                {/if}
            {/if}
        </header>

        {#if !allowed}
            <NoAccess right="plugins.read" what="feeds" />
        {:else if missing}
            <ModuleMissing
                module="feeds"
                what="Feeds are served by ext/feeds, and this binary does not have it."
            />
        {:else if loading && !plugin}
            <div class="block txt-center p-base"><span class="loader lg"></span></div>
        {:else if plugin}
            <!-- The answer first, as on Sitemap: are the feeds live, how much
                 is in them, the two addresses to paste, and whether anything
                 will be held. The figures, the settings and the platform
                 how-to are folded underneath. -->
            <section class="card feed-card map-summary">
                {#if !plugin.enabled && note}
                    <div class="map-summary-state">
                        <span class="map-summary-icon warn" aria-hidden="true"><i class="ri-error-warning-line"></i></span>
                        <div>
                            <h2 class="feed-card-title">Step 1: tell it where your shop is</h2>
                            <p class="txt-hint txt-sm">
                                {note} It is under Settings, just below — then Activate turns on.
                            </p>
                        </div>
                    </div>
                {:else if !plugin.enabled}
                    <div class="map-summary-state">
                        <span class="map-summary-icon off" aria-hidden="true"><i class="ri-rss-line"></i></span>
                        <div class="map-summary-text">
                            <h2 class="feed-card-title">The feeds are off</h2>
                            <p class="txt-hint txt-sm">
                                Turn them on and Google, Meta, Pinterest and TikTok can read your catalogue.
                            </p>
                        </div>
                        {#if writable}
                            <button
                                type="button"
                                class="btn sm map-summary-action"
                                class:loading={working}
                                disabled={working}
                                onclick={toggle}
                            >
                                <i class="ri-toggle-line" aria-hidden="true"></i>
                                <span class="txt">Turn them on</span>
                            </button>
                        {/if}
                    </div>
                {:else if !plugin.configured}
                    <div class="map-summary-state">
                        <span class="map-summary-icon warn" aria-hidden="true"><i class="ri-error-warning-line"></i></span>
                        <div>
                            <h2 class="feed-card-title">One setting left</h2>
                            <p class="txt-hint txt-sm">
                                Add the storefront's address under Settings below. Until then an item has no
                                page to link to, so the feeds answer with an error.
                            </p>
                        </div>
                    </div>
                {:else}
                    <div class="map-summary-state">
                        <span class="map-summary-icon on" aria-hidden="true"><i class="ri-rss-line"></i></span>
                        <div class="map-summary-text">
                            <h2 class="feed-card-title">
                                {#if serving}
                                    {nf.format(status.items)} {status.items === 1 ? "item" : "items"} in your feeds
                                {:else}
                                    Reading your feeds…
                                {/if}
                            </h2>
                            <p class="txt-hint txt-sm">
                                Paste an address into the platform once. Each fetch is built from the catalogue
                                as it is, so prices and stock are always current.
                            </p>
                        </div>
                        <button
                            type="button"
                            class="btn sm transparent secondary map-summary-refresh"
                            class:loading={checking}
                            disabled={checking}
                            onclick={check}
                            aria-label="Count the feeds again"
                            title="Count again"
                        >
                            <i class="ri-refresh-line" aria-hidden="true"></i>
                        </button>
                    </div>
                    <div class="feed-links">
                        {#each LINKS as link (link.path)}
                            <div class="feed-link">
                                <div class="feed-link-name">
                                    <span class="txt-bold">{link.label}</span>
                                    <span class="txt-hint txt-sm">{link.hint}</span>
                                </div>
                                <a class="feed-link-url txt-code" href={origin + link.path} target="_blank" rel="noreferrer"
                                    >{origin}{link.path}</a
                                >
                                <div class="feed-link-actions">
                                    <button type="button" class="btn sm secondary" onclick={() => copy(origin + link.path)}>
                                        <i class="ri-file-copy-line" aria-hidden="true"></i>
                                        <span class="txt">Copy</span>
                                    </button>
                                    <a class="btn sm transparent secondary" href={origin + link.path} target="_blank" rel="noreferrer">
                                        <i class="ri-external-link-line" aria-hidden="true"></i>
                                        <span class="txt">Open</span>
                                    </a>
                                </div>
                            </div>
                        {/each}
                    </div>
                    {#if serving}
                        {#if status.items === 0}
                            <div class="feed-health-clear warn">
                                <i class="ri-information-line" aria-hidden="true"></i>
                                <span>
                                    Nothing to list yet. An active product with a priced variant appears here,
                                    and in both files, as soon as it is saved.
                                </span>
                            </div>
                        {:else if trouble.length}
                            <div class="feed-health-clear warn">
                                <i class="ri-error-warning-line" aria-hidden="true"></i>
                                <span>
                                    Platforms may hold items with
                                    {trouble.map((h) => `${h.title.toLowerCase()} (${nf.format(h.count)})`).join(", ")}.
                                    What to do is just below.
                                </span>
                            </div>
                        {:else}
                            <div class="feed-health-clear">
                                <i class="ri-check-line" aria-hidden="true"></i>
                                <span>Every item has a picture, a brand and an identifier.</span>
                            </div>
                        {/if}
                    {/if}
                {/if}
            </section>

            {#if serving && status.items > 0}
                <details class="card feed-card map-fold" bind:open={contentsOpen}>
                    <summary>
                        <span class="map-fold-title">What's in the feeds</span>
                        <span class="txt-hint txt-sm">
                            {nf.format(status.products)} {status.products === 1 ? "product" : "products"} ·
                            {nf.format(status.in_stock)} in stock · prices in {status.currency || "—"}
                        </span>
                        <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                    </summary>
                    <div class="map-fold-body">
                        <div class="feed-stats">
                            <div class="feed-stat">
                                <div class="feed-stat-label">Items</div>
                                <div class="feed-stat-value">{nf.format(status.items)}</div>
                                <div class="feed-stat-note">
                                    one per variant with a price, across {nf.format(status.products)}
                                    {status.products === 1 ? "product" : "products"}
                                </div>
                            </div>
                            <div class="feed-stat">
                                <div class="feed-stat-label">Availability</div>
                                <div class="feed-stat-value">{nf.format(status.in_stock)} in stock</div>
                                {#if status.out_of_stock > 0}
                                    <div class="feed-stat-note">
                                        {nf.format(status.out_of_stock)} out of stock, still listed
                                    </div>
                                {/if}
                            </div>
                            <div class="feed-stat">
                                <div class="feed-stat-label">Prices in</div>
                                <div class="feed-stat-value">{status.currency || "—"}</div>
                                {#if status.storefront}
                                    <div class="feed-stat-note">linking to {status.storefront}</div>
                                {/if}
                            </div>
                        </div>
                        <!-- The three fields platforms hold an item over, in this
                             store's numbers. -->
                        {#if trouble.length}
                            <div class="feed-health">
                                {#each trouble as h (h.key)}
                                    <div class="feed-health-row">
                                        <div class="feed-health-count">{nf.format(h.count)}</div>
                                        <div>
                                            <div class="txt-bold">{h.title}</div>
                                            <div class="txt-hint txt-sm">{h.fix}</div>
                                        </div>
                                    </div>
                                {/each}
                            </div>
                        {/if}
                    </div>
                </details>
            {/if}

            <!-- Open by itself while a setting is missing: that is the one
                 thing the summary above just asked for. -->
            <details class="card feed-card map-fold" bind:open={settingsOpen}>
                <summary>
                    <span class="map-fold-title">Settings</span>
                    <span class="txt-hint txt-sm">Where each item's page lives, and what it is called</span>
                    <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                    {#if !settingsOpen}
                        <span class="map-fold-values">
                            {#each settingsPreview as v (v.label)}
                                <span class="map-fold-value" class:missing={v.missing}>
                                    <span class="txt-hint">{v.label}:</span>
                                    <span class="txt-ellipsis">{v.value}</span>
                                </span>
                            {/each}
                        </span>
                    {/if}
                </summary>
                <div class="map-fold-body">
                    <p class="txt-hint txt-sm">
                        Every active variant with a price is included, one item each. These settings say where
                        its page lives and what it is called when the product does not say.
                    </p>
                    <form id="feed-settings" class="feed-form" onsubmit={submit}>
                        <PluginFields fields={plugin.fields} unopened={plugin.unopened} bind:form idPrefix="feed" />
                    </form>
                    {#if writable}
                        <div class="feed-form-actions">
                            <button
                                type="submit"
                                form="feed-settings"
                                class="btn sm"
                                class:loading={saving}
                                disabled={saving}
                            >
                                <span class="txt">Save</span>
                            </button>
                            <button
                                type="button"
                                class="btn sm transparent secondary"
                                disabled={saving}
                                onclick={resetForm}
                            >
                                <span class="txt">Discard</span>
                            </button>
                        </div>
                    {/if}
                </div>
            </details>

            <details class="card feed-card map-fold">
                <summary>
                    <span class="map-fold-title">How to connect them</span>
                    <span class="txt-hint txt-sm">Google, Meta, Pinterest and TikTok, step by step</span>
                    <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                </summary>
                <div class="map-fold-body">
                    <div class="feed-tabs" role="tablist" aria-label="Platforms">
                        {#each PLATFORMS as p (p.key)}
                            <button
                                type="button"
                                role="tab"
                                class="feed-tab"
                                class:on={tab === p.key}
                                aria-selected={tab === p.key}
                                onclick={() => (tab = p.key)}>{p.name.split(" ")[0]}</button
                            >
                        {/each}
                    </div>

                    <div class="feed-guide">
                        <div class="feed-guide-head">
                            <div>
                                <h3 class="feed-guide-title">{current.name}</h3>
                                <p class="txt-hint txt-sm">{current.blurb}</p>
                            </div>
                            <a class="btn sm transparent secondary" href={current.docs} target="_blank" rel="noreferrer">
                                <span class="txt">Their docs</span>
                            </a>
                        </div>

                        <div class="feed-guide-reads txt-sm">
                            Reads <span class="txt-code">{current.reads}</span>
                        </div>

                        <h4 class="feed-guide-heading">Before you start</h4>
                        <ul class="feed-before">
                            {#each current.before as line, i (i)}
                                <li>{line}</li>
                            {/each}
                        </ul>

                        <h4 class="feed-guide-heading">Steps</h4>
                        <ol class="feed-steps">
                            {#each current.steps as step, i (i)}
                                <li>
                                    <span class="feed-step-n">{i + 1}</span>
                                    <span>{step}</span>
                                </li>
                            {/each}
                        </ol>
                    </div>

                    <h4 class="feed-guide-heading">If items get rejected</h4>
                    <ul class="feed-before">
                        {#each health as h (h.key)}
                            <li>
                                <span class="txt-bold">{h.title}</span>
                                {#if serving}
                                    <span class="feed-reject-count" class:none={h.count === 0}>
                                        {h.count === 0 ? "none" : nf.format(h.count) + " here"}
                                    </span>
                                {/if}
                                — {h.fix}
                            </li>
                        {/each}
                        <li>
                            <span class="txt-bold">Price or availability mismatch</span>
                            — the platform crawled the product page and read something different from the feed.
                            Usually a storefront cache serving an older page.
                        </li>
                        <li>
                            <span class="txt-bold">Domain not claimed</span>
                            — every platform above has to own the domain before it accepts items from it.
                        </li>
                    </ul>
                </div>
            </details>
        {/if}
    </div>
</div>
