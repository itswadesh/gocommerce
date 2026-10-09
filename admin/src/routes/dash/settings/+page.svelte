<script>
    /**
     * PocketBase's settings layout: a `.page-sidebar` of nav groups beside a
     * `.wrapper` of fields.
     *
     * Two halves, and the split is the point. The shop's own details — its
     * name, address, contact and tax registration — are facts about a business
     * that change when it moves premises, so they are a form. Everything below
     * them is a start-up decision and is read-only, deliberately: changing the
     * settlement currency mid-flight would change what every order in flight
     * means. Rendering those as disabled fields rather than as prose says so
     * plainly — this is where the setting lives, and it is not something to
     * change from a browser while orders are in flight.
     *
     * They all arrive on one authenticated route, `GET /api/admin/settings`,
     * which the shell reads once at sign-in. This screen used to scrape the
     * readiness probe for the currency and the public checkout route for the
     * payment methods — two probes answering a question neither was written
     * for, and neither of which knew the TTLs, the flat shipping or whether
     * catalog prices include tax.
     */
    import { base } from "$app/paths";
    import { settings, loadSettings } from "$lib/settings.svelte.js";
    import { storeProfile, can } from "$lib/api.js";
    import { formatMoney, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";

    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const store = $derived(settings.all);

    /*
     * The shop's own details, which are the one editable thing on this screen.
     * Everything else here is a start-up decision; a name and an address are
     * facts about a business and change without a redeploy.
     */
    const editable = $derived(can("store.write"));
    let profile = $state(null);
    let draft = $state(null);
    let saving = $state(false);

    const FIELDS = [
        { key: "name", label: "Store name", hint: "What a customer calls the shop." },
        { key: "legal_name", label: "Legal name", hint: "The registered entity, when it differs." },
        { key: "email", label: "Contact email" },
        { key: "phone", label: "Phone" },
        { key: "address_line1", label: "Address" },
        { key: "address_line2", label: "Address line 2" },
        { key: "city", label: "City" },
        { key: "state", label: "State or region" },
        { key: "postal_code", label: "Postal code" },
        { key: "country", label: "Country", hint: "Two-letter code, such as GB." },
        { key: "tax_id", label: "Tax registration", hint: "VAT, GST or equivalent." },
        { key: "support_url", label: "Support URL" },
        {
            key: "timezone",
            label: "Timezone",
            kind: "select",
            hint: "What \"today\" means on an invoice and in a report. Empty is UTC.",
        },
        {
            key: "language",
            label: "Language",
            kind: "select",
            hint: "How a customer is written to when their order recorded no preference of its own.",
        },
    ];

    /*
     * The zones this browser knows, which is the IANA list the server will
     * accept — both read the same database. A free-text box here means a
     * typo is only discovered by the save, and a dropdown of four hundred
     * names is still the shortest path to the right one.
     *
     * Older browsers without supportedValuesOf get a text box instead of an
     * empty menu, because a control with no options is worse than a control
     * that asks you to type.
     */
    const zones = (() => {
        try {
            return Intl.supportedValuesOf("timeZone");
        } catch {
            return [];
        }
    })();

    /* Only what this binary has content for: choosing anything else would
       write every customer a message in a language nobody translated, and the
       engine refuses it anyway. */
    const languages = $derived(settings.languages ?? []);

    function optionsFor(key) {
        if (key === "timezone") return zones;
        if (key === "language") return languages;
        return [];
    }

    $effect(() => {
        loadProfile();
    });

    async function loadProfile() {
        try {
            profile = await storeProfile.get();
            draft = { ...profile };
        } catch (err) {
            toast.error(err);
        }
    }

    const dirty = $derived(
        !!profile && !!draft && FIELDS.some((f) => (draft[f.key] ?? "") !== (profile[f.key] ?? "")),
    );

    async function saveProfile() {
        if (saving || !dirty) return;
        saving = true;
        try {
            profile = await storeProfile.save(draft);
            draft = { ...profile };
            toast.success("Store details saved");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

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
    /*
     * Two different silences, and telling them apart is the whole point.
     *
     * A channel with nothing but the engine's log backend needs a vendor
     * module compiled in — a restart, and somebody with the source. A channel
     * that HAS a vendor and still does not deliver needs an API key typed into
     * a form, which is a minute's work by whoever is already looking at the
     * screen. Reporting both as "no delivery backend" sent the second group to
     * do the first group's job, having already done their own.
     */
    const vendorsOf = (c) => (c.backends ?? []).filter((b) => b.module !== "core");
    const unwired = $derived(
        channels.filter((c) => !c.delivers && vendorsOf(c).length === 0),
    );
    const unconfigured = $derived(
        channels.filter((c) => !c.delivers && vendorsOf(c).length > 0),
    );
    /** The vendor names on a channel, for "SendGrid is installed but…". */
    const vendorNames = (c) => vendorsOf(c).map((b) => b.name).join(" and ");
    /** Where a channel's key is typed in. */
    const setupHref = (c) => (c.channel === "sms" ? "/notifications/sms" : "/notifications/email");

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
                <!-- The shop's own details first, because they are the only
                     thing on this screen anybody came here to change. What
                     follows is what the binary was started with. -->
                {#if draft}
                    <h2 class="tw:mb-3 tw:text-sm tw:font-semibold">Store details</h2>
                    <div class="grid m-b-base">
                        {#each FIELDS as f (f.key)}
                            <div class="col-md-4">
                                <div class="field" class:readonly={!editable}>
                                    <label for="sp-{f.key}">{f.label}</label>
                                    {#if f.kind === "select" && optionsFor(f.key).length}
                                        <select
                                            id="sp-{f.key}"
                                            disabled={!editable}
                                            bind:value={draft[f.key]}
                                        >
                                            <!-- Blank is a real choice, not a
                                                 prompt: it means "whatever the
                                                 binary was started with". -->
                                            <option value="">
                                                {f.key === "timezone"
                                                    ? "UTC (nothing set)"
                                                    : "The binary's default"}
                                            </option>
                                            {#each optionsFor(f.key) as option (option)}
                                                <option value={option}>{option}</option>
                                            {/each}
                                        </select>
                                    {:else}
                                        <input
                                            id="sp-{f.key}"
                                            type="text"
                                            maxlength="200"
                                            readonly={!editable}
                                            bind:value={draft[f.key]}
                                        />
                                    {/if}
                                    {#if f.hint}<div class="field-help">{f.hint}</div>{/if}
                                </div>
                            </div>
                        {/each}
                        <div class="col-12">
                            {#if editable}
                                <button
                                    type="button"
                                    class="btn sm"
                                    disabled={saving || !dirty}
                                    onclick={saveProfile}
                                >
                                    <span class="txt">{saving ? "Saving…" : "Save store details"}</span>
                                </button>
                            {:else}
                                <div class="field-help">
                                    These are the shop's own details, and changing them needs
                                    store.write.
                                </div>
                            {/if}
                        </div>
                    </div>
                {/if}

                <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">How this store was started</h2>
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
                        <!-- The payment methods and the carriers used to be
                             listed here as chips. They have had screens of
                             their own since Settings › Payment methods and
                             Settings › Shipping providers arrived, where each
                             one is switched on and given its keys — so this was
                             a second, read-only copy of a list the operator
                             goes somewhere else to act on. -->

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
                            {#if unwired.length}
                                <div class="alert danger m-b-10">
                                    <p>
                                        <i class="ri-error-warning-line" aria-hidden="true"></i>
                                        <strong>
                                            Nothing is installed to deliver {unwired
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
                            {#if unconfigured.length}
                                <!-- A different problem with a different fix:
                                     the module is already compiled in, and what
                                     is missing is a key somebody can type on
                                     the next screen. -->
                                <div class="alert warning m-b-10">
                                    {#each unconfigured as c (c.channel)}
                                        <p>
                                            <i class="ri-key-2-line" aria-hidden="true"></i>
                                            <strong>
                                                {vendorNames(c)}
                                                {vendorsOf(c).length === 1 ? "is" : "are"} installed
                                                for {channelName(c.channel)}, and not sending.
                                            </strong>
                                            {vendorsOf(c).length === 1 ? "It needs its" : "They need an"}
                                            API key and sender before anything leaves the building;
                                            until then these messages only reach the process log and
                                            are reported as sent.
                                            <a href="{base}{setupHref(c)}">
                                                Set up {channelName(c.channel)}
                                            </a>.
                                        </p>
                                    {/each}
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

                    <!-- The list of Go modules this binary was built with used
                         to sit here. Nothing on this screen could act on it —
                         the text said so itself, that changing it is a redeploy
                         — and Plugins names what is installed in words a
                         shopkeeper uses. Diagnostics identifies the build when
                         that is the actual question. -->

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

                        <!-- Only when uploads are off. Working is the ordinary
                             case and needs no sentence; the warning explains a
                             missing upload control, which is a real question. -->
                        {#if !settings.mediaUploadsEnabled}
                            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Media</h2>
                            <div class="field-help">
                                No media backend is configured, so the library records files by URL
                                and offers no upload control. That is a supported way to run —
                                point the store at a directory or a media store to change it.
                            </div>
                        {/if}
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
