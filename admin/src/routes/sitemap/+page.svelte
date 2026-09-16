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
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PluginFields from "$lib/components/PluginFields.svelte";

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

    /** One row per sitemap: what it is, where it is, how many addresses. */
    let files = $state([]);
    let scanning = $state(false);
    let done = $state(0);
    let total = $state(0);
    let scannedAt = $state(null);
    let tab = $state("google");

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

<svelte:head><title>Sitemap · GoCommerce</title></svelte:head>

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
                            disabled={working}
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
            <section class="card feed-card">
                <div class="feed-card-head">
                    <div>
                        <h2 class="feed-card-title">Your sitemaps</h2>
                        <p class="txt-hint txt-sm feed-card-sub">
                            Read by fetching the files themselves, the way a crawler would. Each is
                            generated at the moment it is asked for, so what is counted here is
                            exactly what a crawler would have got a second ago.
                        </p>
                    </div>
                    <button
                        type="button"
                        class="btn sm transparent secondary"
                        class:loading={scanning}
                        disabled={scanning || !plugin.enabled || !plugin.configured}
                        onclick={scan}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                        <span class="txt">Read them again</span>
                    </button>
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

                {#if !plugin.enabled}
                    <p class="txt-hint txt-sm feed-foot">
                        The sitemap answers 404 while the plugin is off, so there is nothing to read.
                    </p>
                {:else if !plugin.configured}
                    <p class="txt-hint txt-sm feed-foot">
                        The sitemap answers 409 until the storefront URL is set below — without it
                        an address has no host to be under.
                    </p>
                {:else if files.length}
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
                    <p class="txt-hint txt-sm feed-foot">
                        {nf.format(addresses)}
                        {addresses === 1 ? "address" : "addresses"} across
                        {files.length - 1}
                        {files.length === 2 ? "sitemap" : "sitemaps"}, plus the index that points at
                        them.{#if scannedAt}{" "}Read {scannedAt.toLocaleTimeString()}.{/if}
                    </p>
                {/if}
            </section>

            <section class="card feed-card">
                <h2 class="feed-card-title">What goes in them</h2>
                <p class="txt-hint txt-sm feed-card-sub">
                    Every active product and collection, and every published page when the CMS
                    module is installed. These settings say where the storefront puts each one, so
                    the sitemap lists addresses that exist.
                </p>
                <div class="feed-form">
                    <PluginFields fields={plugin.fields} bind:form idPrefix="map" />
                </div>
                {#if writable}
                    <div class="feed-form-actions">
                        <button
                            type="button"
                            class="btn sm"
                            class:loading={saving}
                            disabled={saving}
                            onclick={save}
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
            </section>

            <section class="card feed-card">
                <h2 class="feed-card-title">Tell the search engines</h2>
                <p class="txt-hint txt-sm feed-card-sub">
                    Submit the index, not the individual files — it names them, and a crawler
                    follows it. Submitting once is enough; the file is rebuilt on every fetch.
                </p>

                <div class="feed-links">
                    <div class="feed-link">
                        <div class="feed-link-name">
                            <span class="txt-bold">Sitemap index</span>
                            <span class="txt-hint txt-sm">This is the one to submit</span>
                        </div>
                        <a
                            class="feed-link-url txt-code"
                            href={origin + INDEX}
                            target="_blank"
                            rel="noreferrer">{origin}{INDEX}</a
                        >
                        <div class="feed-link-actions">
                            <button
                                type="button"
                                class="btn sm secondary"
                                onclick={() => copy(origin + INDEX)}
                            >
                                <i class="ri-file-copy-line" aria-hidden="true"></i>
                                <span class="txt">Copy</span>
                            </button>
                            <a
                                class="btn sm transparent secondary"
                                href={origin + INDEX}
                                target="_blank"
                                rel="noreferrer"
                            >
                                <i class="ri-external-link-line" aria-hidden="true"></i>
                                <span class="txt">Open</span>
                            </a>
                        </div>
                    </div>
                </div>

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
                        <a
                            class="btn sm transparent secondary"
                            href={engine.docs}
                            target="_blank"
                            rel="noreferrer"
                        >
                            <span class="txt">Their docs</span>
                        </a>
                    </div>
                    <h4 class="feed-guide-heading">Steps</h4>
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
                        — each file carries a stylesheet a browser applies. A crawler ignores it and
                        reads the XML underneath.
                    </li>
                    <li>
                        <span class="txt-bold">A sitemap is a hint, not an instruction</span>
                        — it tells a crawler what exists and when it changed. It does not make anything
                        get indexed.
                    </li>
                    <li>
                        <span class="txt-bold">Drafts and archived products are not in it</span>
                        — only what is active, which is the same rule the storefront serves under.
                    </li>
                </ul>
            </section>
        {/if}
    </div>
</div>
