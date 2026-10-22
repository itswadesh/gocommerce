<script>
    /**
     * Sitemap: what the storefront tells a crawler it has.
     *
     * Shaped after RankMath's sitemap screen, which gets one thing right that
     * a plugin card cannot: it lists the sitemaps themselves, with how many
     * addresses are in each, so "is this working" is answered on the page
     * rather than by opening a file in another tab and counting angle
     * brackets.
     *
     * The counts are read from the files, by fetching them. Not from a status
     * endpoint, and this is the deliberate part: a sitemap that is generated
     * on request has no stored count to report, and a number computed a second
     * way would be a second answer to "what is in the sitemap". Fetching the
     * file is the only answer that cannot be wrong, and it is what a crawler
     * does.
     *
     * It also makes the progress real. The request was a progress bar with a
     * percentage while a sitemap is generated, and there is no honest
     * percentage inside one generation — the server builds the whole document
     * and writes it, with no intermediate state to report. What there IS is
     * four files, fetched one after another: "3 of 4" is a true fraction of
     * real work, and that is what the bar shows. A bar animating through a
     * number the code invented would be worse than no bar.
     */
    import { can, request } from "$lib/api.js";
    import { activateNote, filled } from "$lib/plugin-ready.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PluginFields from "$lib/components/PluginFields.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const PLUGIN = "sitemap";
    const INDEX = "/x/sitemaps/sitemap.xml";

    const allowed = $derived(can("plugins.read"));
    const writable = $derived(can("plugins.write"));
    const missing = $derived(modulesKnown() && !hasModule("sitemaps"));

    let plugin = $state(null);
    let loading = $state(true);
    let working = $state(false);
    let saving = $state(false);
    let form = $state({});
    // What has to be filled in before it can go on — said, not discovered by
    // pressing Activate and being refused.
    const note = $derived(plugin && !plugin.enabled ? activateNote(plugin) : "");

    /** One row per sitemap: what it is, where it is, how many addresses. */
    let files = $state([]);
    let scanning = $state(false);
    let done = $state(0);
    let total = $state(0);
    let scannedAt = $state(null);
    let tab = $state("google");

    /*
     * Settings, folded, still show what they are set to: a closed section
     * that only describes itself makes the owner open it to find out whether
     * there is anything to do. Open by itself while a required one is empty.
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

    const origin = $derived(typeof window === "undefined" ? "" : window.location.origin);
    const percent = $derived(total === 0 ? 0 : Math.round((done / total) * 100));

    $effect(() => {
        if (allowed && hasModule("sitemaps")) load();
    });

    async function load() {
        loading = true;
        try {
            plugin = await request("GET", `/api/admin/plugins/${PLUGIN}`);
            resetForm();
            if (plugin.enabled && plugin.configured) await scan();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function resetForm() {
        const draft = {};
        for (const f of plugin?.fields ?? []) {
            const v = plugin.settings?.[f.key];
            draft[f.key] = f.kind === "bool" ? !!v : (v ?? f.default ?? "");
        }
        form = draft;
    }

    function nameOf(path) {
        const file = path.split("/").pop() ?? path;
        if (file === "sitemap.xml") return "Index";
        return file.replace(".xml", "").replace(/^./, (c) => c.toUpperCase());
    }

    /**
     * Fetch each sitemap and count what is in it.
     *
     * The index first, because it names the rest — so the list of files is the
     * store's own rather than one hard-coded here, and a build without the CMS
     * module shows three rows instead of four without this screen knowing why.
     */
    async function scan() {
        if (scanning) return;
        scanning = true;
        files = [];
        done = 0;
        // Zero means "not known yet". The index names the rest, so until it
        // has been read there is no total to put a fraction over — and
        // claiming 1 of 1 for the first of four is exactly the invented
        // number this bar exists to avoid.
        total = 0;
        try {
            const index = await fetchOne(INDEX);
            files = [index];
            done = 1;

            const children = index.links;
            total = 1 + children.length;
            for (const href of children) {
                // Sequential on purpose: the point of the bar is that each
                // step is a real fetch that finished, and four at once would
                // jump from 0 to 100.
                const path = toPath(href);
                const row = await fetchOne(path);
                files = [...files, row];
                done += 1;
            }
            scannedAt = new Date();
        } catch (err) {
            toast.error(err);
        } finally {
            scanning = false;
        }
    }

    /* The index gives absolute addresses built from the request's own host, so
       they come back pointing at this panel. Reduced to a path so the fetch
       stays same-origin whatever the host header said. */
    function toPath(href) {
        try {
            return new URL(href, origin).pathname;
        } catch {
            return href;
        }
    }

    async function fetchOne(path) {
        const started = performance.now();
        const res = await fetch(path, { headers: { Accept: "application/xml" } });
        const text = await res.text();
        const ms = Math.round(performance.now() - started);
        if (!res.ok) {
            return { path, name: nameOf(path), ok: false, status: res.status, count: 0, bytes: text.length, ms, links: [] };
        }
        const doc = new DOMParser().parseFromString(text, "application/xml");
        const urls = [...doc.getElementsByTagName("url")];
        const maps = [...doc.getElementsByTagName("sitemap")];
        const nodes = maps.length ? maps : urls;
        const links = maps.map((m) => m.getElementsByTagName("loc")[0]?.textContent?.trim()).filter(Boolean);
        // The newest lastmod in the file, which is when anything it lists last
        // changed — the only "last built" a per-request sitemap can honestly
        // report.
        let newest = null;
        for (const n of nodes) {
            const raw = n.getElementsByTagName("lastmod")[0]?.textContent?.trim();
            if (!raw) continue;
            const at = new Date(raw);
            if (!Number.isNaN(at.valueOf()) && (!newest || at > newest)) newest = at;
        }
        return {
            path,
            name: nameOf(path),
            ok: true,
            status: res.status,
            count: nodes.length,
            kind: maps.length ? "sitemaps" : "addresses",
            bytes: new Blob([text]).size,
            ms,
            newest,
            links,
        };
    }

    async function toggle() {
        working = true;
        try {
            plugin = await request("PATCH", `/api/admin/plugins/${PLUGIN}`, {
                body: { enabled: !plugin.enabled },
            });
            toast.success(plugin.enabled ? "Sitemap activated" : "Sitemap deactivated");
            if (plugin.enabled && plugin.configured) await scan();
            else files = [];
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
            toast.success("Sitemap settings saved");
            if (plugin.enabled && plugin.configured) await scan();
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

    const nf = new Intl.NumberFormat();
    const addresses = $derived(
        files.filter((f) => f.kind === "addresses").reduce((n, f) => n + f.count, 0),
    );

    function size(bytes) {
        if (bytes < 1024) return bytes + " B";
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
        return (bytes / (1024 * 1024)).toFixed(1) + " MB";
    }

    function when(d) {
        if (!d) return "—";
        return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
    }

    const ENGINES = [
        {
            key: "google",
            name: "Google Search Console",
            blurb: "Where Google reports what it crawled, what it indexed, and what it refused.",
            steps: [
                "Open Search Console and select the property for this store's domain, adding it if there is not one yet.",
                "Go to Indexing, then Sitemaps.",
                "Paste the sitemap index address above and submit it.",
                "Come back a day later: Google reports the sitemap as read, with a count it found and any it could not fetch.",
            ],
            docs: "https://support.google.com/webmasters/answer/7451001",
        },
        {
            key: "bing",
            name: "Bing Webmaster Tools",
            blurb: "Also feeds DuckDuckGo and Yahoo, which read Bing's index.",
            steps: [
                "Open Bing Webmaster Tools and add this store's domain, or import the property from Search Console.",
                "Go to Sitemaps and choose Submit sitemap.",
                "Paste the sitemap index address above.",
                "Check the Sitemaps page afterwards for the count Bing read and any URLs it rejected.",
            ],
            docs: "https://www.bing.com/webmasters/help/sitemaps-3b5cf6ed",
        },
        {
            key: "robots",
            name: "robots.txt",
            blurb: "The line every crawler reads without being told, including ones you will never sign up for.",
            steps: [
                "Open the storefront's robots.txt — it is served by the storefront, not by this engine.",
                "Add one line: Sitemap: followed by the index address above.",
                "It can sit anywhere in the file and does not need a User-agent block of its own.",
                "Keep submitting to Search Console as well. This line is how a crawler finds the sitemap; submitting is how you find out what it did with it.",
            ],
            docs: "https://developers.google.com/search/docs/crawling-indexing/robots/create-robots-txt",
        },
    ];
    const engine = $derived(ENGINES.find((e) => e.key === tab) ?? ENGINES[0]);
</script>

<svelte:head><title>{titled("Sitemap")}</title></svelte:head>

<div class="page page-sitemap shopify-skin">
    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item txt-sm txt-hint">Content</div>
                <div class="breadcrumb-item feed-title">Sitemap</div>
            </nav>
            {#if plugin}
                <div class="feed-head-chips">
                    <span class="label {state.cls}">{state.label}</span>
                    <span class="label">sitemaps.org XML</span>
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
            <NoAccess right="plugins.read" what="the sitemap" />
        {:else if missing}
            <ModuleMissing
                module="sitemaps"
                what="The sitemap is served by ext/sitemaps, and this binary does not have it."
            />
        {:else if loading && !plugin}
            <div class="block txt-center p-base"><span class="loader lg"></span></div>
        {:else if plugin}
            <!-- The answer first: is it live, how much is in it, and the one
                 address to hand a search engine. Everything else — the files,
                 the settings, the how-to — is folded underneath, a click away,
                 because most visits want only this card. -->
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
                        <span class="map-summary-icon off" aria-hidden="true"><i class="ri-map-2-line"></i></span>
                        <div class="map-summary-text">
                            <h2 class="feed-card-title">The sitemap is off</h2>
                            <p class="txt-hint txt-sm">
                                Turn it on and search engines can find every product, collection and page.
                            </p>
                        </div>
                        <!-- The card says what to do, so it carries the button
                             that does it, not only the page header. -->
                        {#if writable}
                            <button
                                type="button"
                                class="btn sm map-summary-action"
                                class:loading={working}
                                disabled={working}
                                onclick={toggle}
                            >
                                <i class="ri-toggle-line" aria-hidden="true"></i>
                                <span class="txt">Turn it on</span>
                            </button>
                        {/if}
                    </div>
                {:else if !plugin.configured}
                    <div class="map-summary-state">
                        <span class="map-summary-icon warn" aria-hidden="true"><i class="ri-error-warning-line"></i></span>
                        <div>
                            <h2 class="feed-card-title">One setting left</h2>
                            <p class="txt-hint txt-sm">
                                Add the storefront's address under Settings below. Until then the sitemap has
                                no host to list addresses under.
                            </p>
                        </div>
                    </div>
                {:else}
                    <div class="map-summary-state">
                        <span class="map-summary-icon on" aria-hidden="true"><i class="ri-map-2-line"></i></span>
                        <div class="map-summary-text">
                            <h2 class="feed-card-title">
                                {#if files.length}
                                    {nf.format(addresses)} {addresses === 1 ? "address" : "addresses"} listed
                                {:else if scanning}
                                    Reading your sitemap…
                                {:else}
                                    Your sitemap is live
                                {/if}
                            </h2>
                            <p class="txt-hint txt-sm">
                                Submit this one address to a search engine; it points at the rest and is
                                rebuilt on every fetch.
                            </p>
                        </div>
                        <button
                            type="button"
                            class="btn sm transparent secondary map-summary-refresh"
                            class:loading={scanning}
                            disabled={scanning}
                            onclick={scan}
                            aria-label="Read the sitemap again"
                            title="Read it again"
                        >
                            <i class="ri-refresh-line" aria-hidden="true"></i>
                        </button>
                    </div>
                    <div class="map-index">
                        <a class="txt-code map-index-url" href={origin + INDEX} target="_blank" rel="noreferrer"
                            >{origin}{INDEX}</a
                        >
                        <button type="button" class="btn sm secondary" onclick={() => copy(origin + INDEX)}>
                            <i class="ri-file-copy-line" aria-hidden="true"></i>
                            <span class="txt">Copy</span>
                        </button>
                        <a class="btn sm transparent secondary" href={origin + INDEX} target="_blank" rel="noreferrer">
                            <i class="ri-external-link-line" aria-hidden="true"></i>
                            <span class="txt">Open</span>
                        </a>
                    </div>
                    <!-- A true fraction: files fetched, out of files the index
                         names. There is no honest percentage inside one
                         generation, so the bar counts the thing that does have
                         steps. -->
                    {#if scanning || (total > 0 && done < total)}
                        <div class="map-progress" role="status" aria-live="polite">
                            <div class="map-progress-head">
                                <span>
                                    {#if total === 0}
                                        Reading the sitemap index, which names the rest
                                    {:else}
                                        Reading sitemap {Math.min(done + 1, total)} of {total}
                                    {/if}
                                </span>
                                <span class="map-progress-pct">{percent}%</span>
                            </div>
                            <div
                                class="map-progress-track"
                                role="progressbar"
                                aria-valuenow={percent}
                                aria-valuemin="0"
                                aria-valuemax="100"
                            >
                                <div class="map-progress-bar" style="width: {percent}%"></div>
                            </div>
                        </div>
                    {/if}
                {/if}
            </section>

            {#if plugin.enabled && plugin.configured && files.length}
                <details class="card feed-card map-fold">
                    <summary>
                        <span class="map-fold-title">The files</span>
                        <span class="txt-hint txt-sm">
                            {files.length - 1} {files.length === 2 ? "sitemap" : "sitemaps"} and the index{#if scannedAt}
                                · read {scannedAt.toLocaleTimeString()}{/if}
                        </span>
                        <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                    </summary>
                    <div class="map-fold-body">
                        <p class="txt-hint txt-sm">
                            Read by fetching each file the way a crawler would, so what is counted here is
                            exactly what a crawler would have got a second ago.
                        </p>
                        <div class="map-table">
                            <div class="map-row map-head">
                                <div>Sitemap</div>
                                <div>Address</div>
                                <div class="txt-right">Holds</div>
                                <div class="txt-right">Size</div>
                                <div class="txt-right">Newest</div>
                            </div>
                            {#each files as f (f.path)}
                                <div class="map-row" class:is-bad={!f.ok}>
                                    <div class="txt-bold">{f.name}</div>
                                    <div class="map-url">
                                        <a href={origin + f.path} target="_blank" rel="noreferrer" class="txt-code"
                                            >{f.path}</a
                                        >
                                        <button
                                            type="button"
                                            class="btn sm transparent secondary map-copy"
                                            onclick={() => copy(origin + f.path)}
                                            aria-label="Copy {f.name} address"
                                        >
                                            <i class="ri-file-copy-line" aria-hidden="true"></i>
                                        </button>
                                    </div>
                                    <div class="txt-right">
                                        {#if f.ok}
                                            {nf.format(f.count)}
                                            <span class="txt-hint txt-sm">{f.kind}</span>
                                        {:else}
                                            <span class="txt-danger">HTTP {f.status}</span>
                                        {/if}
                                    </div>
                                    <div class="txt-right txt-hint">{f.ok ? size(f.bytes) : "—"}</div>
                                    <div class="txt-right txt-hint">{when(f.newest)}</div>
                                </div>
                            {/each}
                        </div>
                    </div>
                </details>
            {/if}

            <!-- Open by itself while a setting is missing: that is the one
                 thing the summary above just asked for. -->
            <details class="card feed-card map-fold" bind:open={settingsOpen}>
                <summary>
                    <span class="map-fold-title">Settings</span>
                    <span class="txt-hint txt-sm">Where the storefront puts each product, collection and page</span>
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
                        Every active product and collection is listed, and every published page when the CMS
                        module is installed. These say where each lives, so the sitemap lists addresses that
                        exist.
                    </p>
                    <form id="map-settings" class="feed-form" onsubmit={submit}>
                        <PluginFields fields={plugin.fields} unopened={plugin.unopened} bind:form idPrefix="map" />
                    </form>
                    {#if writable}
                        <div class="feed-form-actions">
                            <button
                                type="submit"
                                form="map-settings"
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
                    <span class="map-fold-title">How to submit it</span>
                    <span class="txt-hint txt-sm">Google, Bing and the others, step by step</span>
                    <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                </summary>
                <div class="map-fold-body">
                    <div class="feed-tabs" role="tablist" aria-label="Where to submit">
                        {#each ENGINES as e (e.key)}
                            <button
                                type="button"
                                role="tab"
                                class="feed-tab"
                                class:on={tab === e.key}
                                aria-selected={tab === e.key}
                                onclick={() => (tab = e.key)}>{e.name.split(" ")[0]}</button
                            >
                        {/each}
                    </div>

                    <div class="feed-guide">
                        <div class="feed-guide-head">
                            <div>
                                <h3 class="feed-guide-title">{engine.name}</h3>
                                <p class="txt-hint txt-sm">{engine.blurb}</p>
                            </div>
                            <a class="btn sm transparent secondary" href={engine.docs} target="_blank" rel="noreferrer">
                                <span class="txt">Their docs</span>
                            </a>
                        </div>
                        <ol class="feed-steps">
                            {#each engine.steps as step, i (i)}
                                <li>
                                    <span class="feed-step-n">{i + 1}</span>
                                    <span>{step}</span>
                                </li>
                            {/each}
                        </ol>
                    </div>

                    <h4 class="feed-guide-heading">Worth knowing</h4>
                    <ul class="feed-before">
                        <li>
                            <span class="txt-bold">Opening the address shows a table, not XML</span>
                            — each file carries a stylesheet a browser applies. A crawler ignores it and reads
                            the XML underneath.
                        </li>
                        <li>
                            <span class="txt-bold">A sitemap is a hint, not an instruction</span>
                            — it tells a crawler what exists and when it changed. It does not make anything get
                            indexed.
                        </li>
                        <li>
                            <span class="txt-bold">Drafts and archived products are not in it</span>
                            — only what is active, which is the same rule the storefront serves under.
                        </li>
                    </ul>
                </div>
            </details>
        {/if}
    </div>
</div>
