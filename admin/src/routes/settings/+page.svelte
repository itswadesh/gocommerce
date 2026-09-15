<script>
    /**
     * PocketBase's settings layout: a `.page-sidebar` of nav groups beside a
     * `.wrapper` of fields.
     *
     * Every field here is read-only, and deliberately so — a store's currency,
     * language and installed payment providers are decisions the binary was
     * started with. Rendering them as disabled fields rather than as prose
     * says that plainly: this is where the setting lives, and it is not
     * something to change from a browser while orders are in flight.
     *
     * They all arrive on one authenticated route, `GET /api/admin/settings`,
     * which the shell reads once at sign-in. This screen used to scrape the
     * readiness probe for the currency and the public checkout route for the
     * payment methods — two probes answering a question neither was written
     * for, and neither of which knew the TTLs, the flat shipping or whether
     * catalog prices include tax.
     */
    import { settings, loadSettings } from "$lib/settings.svelte.js";
    import { formatMoney, pluralize } from "$lib/format.js";

    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const store = $derived(settings.all);

    /**
     * The TTLs cross as seconds, which is the one unit that is never ambiguous
     * on the wire and the one nobody reads: 2592000 is thirty days, and no
     * operator should have to work that out.
     *
     * The largest unit the value divides into exactly, so a ninety-minute
     * window says ninety minutes rather than rounding itself to two hours.
     */
    function ttl(seconds) {
        if (!seconds) return "—";
        for (const [unit, size] of [
            ["day", 86400],
            ["hour", 3600],
            ["minute", 60],
        ]) {
            if (seconds >= size && seconds % size === 0) {
                const count = seconds / size;
                return `${count} ${pluralize(count, unit)}`;
            }
        }
        return `${seconds} ${pluralize(seconds, "second")}`;
    }

    /* The default is marked in the list rather than given a box of its own: it
       is one of these tags, not a fourth setting beside them. */
    const languageList = $derived(
        settings.languages
            .map((lang) => (lang === settings.defaultLanguage ? `${lang} (default)` : lang))
            .join(", "),
    );

    /*
     * The one line on this screen that can be bad news.
     *
     * With no notifier module installed the engine's built-in logger is
     * registered for every channel: it writes "notification (no delivery
     * backend installed)" to the process log and returns success. So the send
     * succeeds, the outbox marks the event delivered, nothing anywhere errors —
     * and the customer's order confirmation reached nobody. A store can run
     * that way for months, which is why this is drawn in red rather than left
     * as another grey chip.
     *
     * Read off `settings.all` rather than through a named getter: the store
     * exposes those for the fields several screens share, and this one has a
     * single reader.
     */
    const channels = $derived(
        Array.isArray(store?.notifier_channels) ? store.notifier_channels : [],
    );
    const silent = $derived(channels.filter((c) => !c.delivers));

    /* Key presence, not length. An empty list is a real answer — a store built
       from the engine and nothing else — and it has to be told apart from a
       binary that predates the field, which answers the same way by saying
       nothing. */
    const modulesKnown = $derived(Array.isArray(store?.modules));

    const CHANNEL_LABEL = { email: "Email", sms: "SMS" };
    const channelName = (code) => CHANNEL_LABEL[code] ?? code;

    /** What is actually behind a channel, for the chip's tooltip. */
    function backendsOf(channel) {
        const names = (channel.backends ?? []).map((b) =>
            b.delivers ? `${b.name} (${b.module})` : `${b.name} — writes to the log only`,
        );
        return names.length ? names.join(", ") : "nothing registered";
    }
</script>

<svelte:head><title>Store settings · GoCommerce</title></svelte:head>

<div class="page page-settings shopify-skin">

    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Store</div>
            </nav>
        </header>

        <div class="wrapper m-b-base">
            {#if !settings.loaded && (settings.loading || !settings.error)}
                <div class="block txt-center"><span class="loader lg"></span></div>
            {:else if !settings.loaded}
                <!-- The one screen where a failed settings read is the subject
                     rather than a background degradation, so the one screen
                     that offers a retry. -->
                <div class="alert danger">
                    <p>Could not read this store's settings. {settings.error?.message ?? ""}</p>
                    <button
                        type="button"
                        class="btn sm secondary"
                        onclick={() => loadSettings({ force: true })}
                    >
                        <span class="txt">Try again</span>
                    </button>
                </div>
            {:else}
                <div class="grid">
                    <div class="col-md-4">
                        <div class="field readonly">
                            <label for="currency">Settlement currency</label>
                            <input id="currency" type="text" readonly value={store?.currency ?? ""} />
                        </div>
                    </div>
                    <div class="col-md-4">
                        <div class="field readonly">
                            <label for="language">Languages</label>
                            <!-- The list, not the default alone. A store that
                                 serves three languages negotiates one per
                                 request, stamps it on every order and hands it
                                 to every notifier — and until this said so, the
                                 set the store was configured for appeared
                                 nowhere in the panel at all. The default is
                                 marked rather than shown on its own line,
                                 because it is one of these and not a fourth
                                 thing. -->
                            <input id="language" type="text" readonly value={languageList} />
                        </div>
                    </div>
                    <div class="col-md-4">
                        <div class="field readonly">
                            <label for="version">Engine version</label>
                            <input id="version" type="text" readonly value={store?.version ?? ""} />
                        </div>
                    </div>

                    <div class="col-md-4">
                        <div class="field readonly">
                            <label for="tax">Catalog prices</label>
                            <input
                                id="tax"
                                type="text"
                                readonly
                                value={store?.prices_include_tax
                                    ? "Include tax"
                                    : "Have tax added at checkout"}
                            />
                        </div>
                    </div>
                    <div class="col-md-4">
                        <div class="field readonly">
                            <label for="shipping">Flat shipping</label>
                            <input
                                id="shipping"
                                type="text"
                                readonly
                                value={formatMoney(store?.flat_shipping)}
                            />
                        </div>
                    </div>
                    <div class="col-md-4">
                        <div class="field readonly">
                            <label for="prefix">Order number prefix</label>
                            <input id="prefix" type="text" readonly value={store?.order_prefix ?? ""} />
                        </div>
                    </div>

                    <div class="col-md-4">
                        <div class="field readonly">
                            <label for="cart-ttl">A cart is kept for</label>
                            <input
                                id="cart-ttl"
                                type="text"
                                readonly
                                value={ttl(store?.cart_ttl_seconds)}
                            />
                        </div>
                    </div>
                    <div class="col-md-4">
                        <div class="field readonly">
                            <label for="order-ttl">An unpaid order expires after</label>
                            <input
                                id="order-ttl"
                                type="text"
                                readonly
                                value={ttl(store?.order_ttl_seconds)}
                            />
                        </div>
                    </div>

                    <div class="col-12">
                        <div class="field-help">
                            These come from the <code>Config</code> the binary was started with.
                            Money is stored in minor units alongside its code, which is what lets
                            any currency work without an engine change — and what makes changing
                            one mid-flight a migration rather than a setting.
                        </div>
                    </div>

                    <div class="col-12">
                        <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Installed capabilities</h2>
                        <div class="flex flex-wrap gap-5 m-b-10">
                            <!-- The name is the provider's own, and the nested
                                 label is the module that installed it — which is
                                 the question a store with four gateways has. -->
                            {#each store?.payment_methods ?? [] as method (method.code)}
                                <span class="label info" title={method.code}>
                                    {method.name}
                                    <span class="label">{method.module}</span>
                                </span>
                            {/each}
                        </div>
                        <div class="field-help m-b-base">
                            Each one is a Go module wired into <code>main()</code>. Cash on delivery
                            is built in because it needs no third party; adding Stripe is one import
                            and one argument, and changes no engine code.
                        </div>

                        <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Shipping</h2>
                        <div class="flex flex-wrap gap-5 m-b-10">
                            {#each store?.fulfillment_providers ?? [] as provider (provider.code)}
                                <span class="label info" title={provider.code}>
                                    {provider.name}
                                    <span class="label">{provider.module}</span>
                                </span>
                            {/each}
                        </div>
                        <div class="field-help m-b-base">
                            The same arrangement on the shipping side: manual fulfilment is built
                            in, and a carrier module joins this list by registering a provider.
                        </div>

                        <!-- Only when the store reported its channels. The
                             engine always reports both, so an empty list is a
                             binary that predates the field rather than a store
                             with no channels — and a section that renders
                             nothing but its own heading reads as a bug. -->
                        {#if channels.length}
                            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Notifications</h2>
                            <!-- The warning comes before the chips, because it is
                                 the thing to read: a channel with no delivery
                                 backend accepts every send and reports success. -->
                            {#if silent.length}
                                <div class="alert danger m-b-10">
                                    <p>
                                        <i class="ri-error-warning-line" aria-hidden="true"></i>
                                        <strong>
                                            No delivery backend for {silent
                                                .map((c) => channelName(c.channel))
                                                .join(" or ")}.
                                        </strong>
                                        Those messages are written to the process log and
                                        reported as sent — order confirmations, shipping
                                        notices and password-reset links included. Nothing
                                        errors, nothing retries, and nobody receives anything.
                                    </p>
                                    <p>
                                        Wiring a vendor module into <code>main()</code> — one
                                        import and one argument — puts a real backend on the
                                        channel. <code>gocommerce doctor</code> reports the
                                        same thing from the command line.
                                    </p>
                                </div>
                            {/if}
                            <div class="flex flex-wrap gap-5 m-b-10">
                                {#each channels as channel (channel.channel)}
                                    <span
                                        class="label {channel.delivers ? 'success' : 'danger'}"
                                        title={backendsOf(channel)}
                                    >
                                        {channelName(channel.channel)}
                                        <span class="label">
                                            {channel.delivers ? "delivering" : "log only"}
                                        </span>
                                    </span>
                                {/each}
                            </div>
                            <div class="field-help">
                                The engine decides when to notify and what the message is about;
                                delivering it is a vendor's job, and a vendor is a module. A channel
                                nobody sends on is a fine configuration — it should just be one
                                somebody chose.
                            </div>
                        {/if}
                    </div>

                    {#if modulesKnown}
                        <div class="col-12">
                            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Modules</h2>
                            <!-- What this binary was actually built with. Until
                                 the engine served this list the panel learned it
                                 by probing one admin route per module on every
                                 sign-in, and no screen said it out loud. -->
                            {#if settings.modules.length}
                                <div class="flex flex-wrap gap-5 m-b-10">
                                    {#each settings.modules as name (name)}
                                        <span class="label">{name}</span>
                                    {/each}
                                </div>
                            {:else}
                                <div class="field-help m-b-10">
                                    This store is the engine and nothing else. Every screen you
                                    can reach is core.
                                </div>
                            {/if}
                            <div class="field-help">
                                A store is its own Go program that composes the engine with the
                                modules it needs, in the order shown — which is the order their
                                migrations ran in. Changing the list is a redeploy, not a setting.
                            </div>
                        </div>
                    {/if}

                    {#if settings.languages.length > 1}
                        <div class="col-12">
                            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Read the catalog as a shopper</h2>
                            <div class="field-help m-b-sm">
                                The engine negotiates a language per request, renders the public
                                catalog through the translator the store registered, stamps the
                                language on every order and hands it to every notifier. These open
                                the public read for one language, which is the only way to see what
                                a shopper in it is actually served.
                            </div>
                            <div class="flex flex-wrap gap-5">
                                {#each settings.languages as lang (lang)}
                                    <a
                                        class="btn sm secondary"
                                        href="/api/products?lang={lang}"
                                        target="_blank"
                                        rel="noreferrer"
                                    >
                                        <span class="txt">{lang}</span>
                                    </a>
                                {/each}
                            </div>
                        </div>
                    {/if}

                    <div class="col-12">
                        <!-- The one personal preference on a page of store facts. It
                             used to sit in every screen's footer; a switch flipped
                             twice a year does not earn thirty copies of itself. -->
                        <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Appearance</h2>
                        <div class="flex flex-wrap gap-10 m-b-10" style="align-items: center">
                            <ThemeToggle />
                            <span class="txt-hint txt-sm">
                                Light or dark, for this browser. Until you choose, the panel
                                follows the system setting.
                            </span>
                        </div>

                        <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Media</h2>
                        <div class="field-help">
                            {#if settings.mediaUploadsEnabled}
                                This store has somewhere to put a file, so the library takes
                                uploads as well as URLs.
                            {:else}
                                No media backend is configured, so the library records files by URL
                                and offers no upload control. That is a supported way to run —
                                point the store at a directory or a media store to change it.
                            {/if}
                        </div>
                    </div>

                    <div class="col-12">
                        <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">API</h2>
                        <div class="field-help m-b-sm">
                            This panel is a client of the same API as anything else — it has no
                            private endpoints. Everything you can do here, you can do with curl.
                        </div>
                        <div class="flex gap-10">
                            <a class="btn secondary" href="/docs" target="_blank" rel="noreferrer">
                                <i class="ri-book-open-line" aria-hidden="true"></i>
                                <span class="txt">Browse the API</span>
                            </a>
                            <a class="btn transparent secondary" href="/doc" target="_blank" rel="noreferrer">
                                <i class="ri-file-code-line" aria-hidden="true"></i>
                                <span class="txt">OpenAPI document</span>
                            </a>
                        </div>
                    </div>
                </div>
            {/if}
        </div>

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <span class="txt">GoCommerce {store?.version ?? ""}</span>
        </footer>
    </div>
</div>
