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
    import { storeProfile, can, stores as storesAPI } from "$lib/api.js";
    import { forgetStores, loadStores, myStores } from "$lib/stores.svelte.js";
    import { formatMoney, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { forgetStore } from "$lib/store-scope.js";
    import { goto } from "$app/navigation";

    import ThemeToggle from "$lib/components/ThemeToggle.svelte";
    import Select from "$lib/components/Select.svelte";
    import { countryOptions } from "$lib/countries.js";
    import { currencyOptions } from "$lib/currencies.js";
    import { languageName, languageOptions } from "$lib/languages.js";
    import { rise, flipDuration } from "$lib/motion.js";
    import { flip } from "svelte/animate";
    import { brand, titled } from "$lib/brand.svelte.js";

    /*
     * Deleting the store. Offered to its owner only, never on a demo store,
     * and never for the install's last one — the engine refuses all three,
     * and a control that can only fail is worse than none. The slug typed
     * back is the whole of the confirmation: there is no undo to offer.
     */
    const here = $derived(myStores.current);
    const mayDelete = $derived(
        !!here && here.role === "owner" && !here.demo && myStores.list.length > 1,
    );
    let confirmSlug = $state("");
    let deleting = $state(false);
    async function deleteStore() {
        if (!here || deleting || confirmSlug.trim() !== here.slug) return;
        deleting = true;
        try {
            await storesAPI.remove(here.id, confirmSlug.trim());
            toast.success(`${here.name} was deleted`);
            forgetStore();
            forgetStores();
            await goto(`${base}/select-store`, { replaceState: true });
        } catch (err) {
            toast.error(err);
        } finally {
            deleting = false;
        }
    }

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
        { key: "country", label: "Country", kind: "select", hint: "Where the business is registered." },
        { key: "tax_id", label: "Tax registration", hint: "VAT, GST or equivalent." },
        { key: "support_url", label: "Support URL" },
        {
            key: "timezone",
            label: "Timezone",
            kind: "select",
            hint: "What \"today\" means on an invoice and in a report. Empty is UTC.",
        },
        // The store's own currency (D85): chosen until its first order, then
        // fixed, because changing it would change what every order means.
        { key: "currency", label: "Currency", kind: "select" },
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

    function optionsFor(key) {
        if (key === "timezone") return zones;
        if (key === "country") return countryOptions(draft?.country);
        if (key === "currency") return currencyOptions(draft?.currency);
        return [];
    }

    /*
     * A store-details select's choices. Blank is a real choice, not a prompt:
     * it means "whatever the binary was started with". A store always sells in
     * some currency, so that one has none.
     */
    function selectOptions(key) {
        const blank =
            key === "currency"
                ? []
                : [
                      {
                          value: "",
                          label:
                              key === "timezone"
                                  ? "UTC (nothing set)"
                                  : key === "country"
                                    ? "Not set"
                                    : "The binary's default",
                      },
                  ];
        return [...blank, ...optionsFor(key).map((o) => ({ value: o.value ?? o, label: o.label ?? o }))];
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

    const languagesChanged = $derived(
        !!profile && !!draft && (draft.languages ?? []).join() !== (profile.languages ?? []).join(),
    );
    const dirty = $derived(
        !!profile &&
            !!draft &&
            (languagesChanged || FIELDS.some((f) => (draft[f.key] ?? "") !== (profile[f.key] ?? ""))),
    );

    /*
     * The store's languages (D87), main first. One list rather than a list and
     * a default beside it, as the engine keeps them: the main language is what
     * the catalogue is written in and what a shopper who asks for nothing is
     * served, so it cannot be one the store does not sell in.
     */
    let adding = $state("");
    function addLanguage() {
        if (!adding) return;
        draft.languages = [...(draft.languages ?? []), adding];
        adding = "";
    }
    function makeMain(tag) {
        draft.languages = [tag, ...draft.languages.filter((l) => l !== tag)];
    }
    function dropLanguage(tag) {
        draft.languages = draft.languages.filter((l) => l !== tag);
    }

    async function saveProfile() {
        if (saving || !dirty) return;
        saving = true;
        try {
            const before = profile?.currency;
            const languagesBefore = (profile?.languages ?? []).join();
            profile = await storeProfile.save({ ...draft, language: draft.languages?.[0] ?? "" });
            draft = { ...profile };
            toast.success("Store details saved");
            // Every price on every screen is formatted in the store's currency,
            // and the languages decide what Translations and the message
            // editors offer; both are read once into the settings, so a change
            // has to reach them now.
            if (profile.currency !== before || profile.languages.join() !== languagesBefore) {
                loadSettings({ force: true }).catch(() => {});
            }
            // The name here is the store's name, so the account menu and the
            // store list change with it.
            loadStores().catch(() => {});
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
    // Notifications open by themselves while a channel is not really sending.
    let notifyOpen = $state(false);
    $effect(() => {
        if (unwired.length || unconfigured.length) notifyOpen = true;
    });
    /** The vendor names on a channel, for "SendGrid is installed but…". */
    const vendorNames = (c) => vendorsOf(c).map((b) => b.name).join(" and ");
    /** Where a channel's key is typed in. */
    const setupHref = (c) => (c.channel === "sms" ? "/dash/notifications/sms" : "/dash/notifications/email");

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

<svelte:head><title>{titled("Store settings")}</title></svelte:head>

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
                    <section class="card">
                        <h2 class="tw:mb-3 tw:text-sm tw:font-semibold">Store details</h2>
                        <!-- A form, so Enter in any field saves, like the button. -->
                        <form class="grid" onsubmit={(e) => (e.preventDefault(), editable && saveProfile())}>
                            {#each FIELDS as f (f.key)}
                                <div class="col-md-4">
                                    <div class="field" class:readonly={!editable}>
                                        <label for="sp-{f.key}">{f.label}</label>
                                        {#if f.kind === "select" && optionsFor(f.key).length}
                                            <Select
                                                id="sp-{f.key}"
                                                disabled={!editable || (f.key === "currency" && profile?.currency_locked)}
                                                bind:value={draft[f.key]}
                                                options={selectOptions(f.key)}
                                            />
                                        {:else}
                                            <input
                                                id="sp-{f.key}"
                                                type="text"
                                                maxlength="200"
                                                readonly={!editable}
                                                bind:value={draft[f.key]}
                                            />
                                        {/if}
                                        {#if f.key === "currency"}
                                            <div class="field-help">
                                                {profile?.currency_locked
                                                    ? "Fixed: this store has orders that were paid or not cancelled, and changing it would change what their amounts mean."
                                                    : "What the store sells in. Fixed once it takes its first order; changing it now relabels the prices already entered, without converting them."}
                                            </div>
                                        {:else if f.hint}<div class="field-help">{f.hint}</div>{/if}
                                    </div>
                                </div>
                            {/each}
                            <div class="col-12">
                                <div class="langs" role="group" aria-labelledby="sp-languages">
                                    <div class="langs-head">
                                        <span id="sp-languages" class="langs-title">Languages</span>
                                        <span class="field-help">
                                            The first is the main one: the catalogue is written in it, and a
                                            shopper who asks for nothing is served it. Customers are written to
                                            in the language they ordered in.
                                        </span>
                                    </div>
                                    <ul class="langs-list">
                                        {#each draft.languages ?? [] as tag, i (tag)}
                                            <li class="langs-item" class:main={i === 0} in:rise animate:flip={{ duration: flipDuration() }}>
                                                <span class="langs-name">
                                                    {languageName(tag)} <span class="txt-hint txt-sm">{tag}</span>
                                                </span>
                                                {#if i === 0}
                                                    <span class="label sm success">Main</span>
                                                {:else if editable}
                                                    <button
                                                        type="button"
                                                        class="btn sm transparent secondary"
                                                        aria-label="Make {languageName(tag)} the main language"
                                                        onclick={() => makeMain(tag)}
                                                    >
                                                        <span class="txt">Make main</span>
                                                    </button>
                                                    <button
                                                        type="button"
                                                        class="btn sm circle transparent secondary"
                                                        aria-label="Stop selling in {languageName(tag)}"
                                                        title="Remove"
                                                        onclick={() => dropLanguage(tag)}
                                                    >
                                                        <i class="ri-close-line" aria-hidden="true"></i>
                                                    </button>
                                                {/if}
                                            </li>
                                        {/each}
                                    </ul>
                                    {#if editable}
                                        <!-- A button rather than adding on pick, so a
                                             language is added only on purpose. -->
                                        <div class="langs-add">
                                            <div class="field">
                                                <label for="sp-add-language">Add a language</label>
                                                <Select
                                                    id="sp-add-language"
                                                    bind:value={adding}
                                                    options={languageOptions(draft.languages ?? [])}
                                                    placeholder="Choose one…"
                                                />
                                            </div>
                                            <button type="button" class="btn sm secondary" disabled={!adding} onclick={addLanguage}>
                                                <span class="txt">Add</span>
                                            </button>
                                        </div>
                                    {/if}
                                </div>
                            </div>
                            <div class="col-12">
                                {#if editable}
                                    <button type="submit" class="btn sm" disabled={saving || !dirty}>
                                        <span class="txt">{saving ? "Saving…" : "Save store details"}</span>
                                    </button>
                                {:else}
                                    <div class="field-help">
                                        These are the shop's own details, and changing them needs
                                        store.write.
                                    </div>
                                {/if}
                            </div>
                        </form>
                    </section>
                {/if}

                <!-- Everything below is read more than it is changed, so it is
                     folded, as on Sitemap and Feeds, and each fold says on its
                     heading what is inside. -->
                <details class="card map-fold">
                    <summary>
                        <span class="map-fold-title">How this store was started</span>
                        <span class="txt-hint txt-sm">Fixed when the engine starts; read only</span>
                        <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                        <span class="map-fold-values">
                            <span class="map-fold-value">
                                prices {store?.prices_include_tax ? "include tax" : "have tax added at checkout"}
                            </span>
                            <span class="map-fold-value">version {store?.version ?? "—"}</span>
                        </span>
                    </summary>
                    <div class="map-fold-body grid">
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
                                    value={store?.prices_include_tax ? "Include tax" : "Have tax added at checkout"}
                                />
                            </div>
                        </div>
                        <div class="col-md-4">
                            <div class="field readonly">
                                <label for="shipping">Flat shipping</label>
                                <input id="shipping" type="text" readonly value={formatMoney(store?.flat_shipping)} />
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
                                <input id="cart-ttl" type="text" readonly value={ttl(store?.cart_ttl_seconds)} />
                            </div>
                        </div>
                        <div class="col-md-4">
                            <div class="field readonly">
                                <label for="order-ttl">An unpaid order expires after</label>
                                <input id="order-ttl" type="text" readonly value={ttl(store?.order_ttl_seconds)} />
                            </div>
                        </div>
                        <div class="col-12">
                            <div class="field-help">
                                These come from the <code>Config</code> the binary was started with.
                                The store's currency is its own, under Store details above.
                            </div>
                        </div>
                    </div>
                </details>

                <!-- Only when the store reported its channels: the engine always
                     reports both, so an empty list is an older binary, and a
                     section with nothing in it reads as a bug. Open by itself
                     while a channel is not really sending — that is bad news the
                     owner has to see. -->
                {#if channels.length}
                    <details class="card map-fold" bind:open={notifyOpen}>
                        <summary>
                            <span class="map-fold-title">Notifications</span>
                            <span class="txt-hint txt-sm">Who delivers order emails and texts</span>
                            <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                            {#if !notifyOpen}
                                <span class="map-fold-values">
                                    {#each channels as channel (channel.channel)}
                                        <span class="map-fold-value" class:missing={!channel.delivers}>
                                            <span class="txt-hint">{channelName(channel.channel)}:</span>
                                            {channel.delivers ? "delivering" : "not sending"}
                                        </span>
                                    {/each}
                                </span>
                            {/if}
                        </summary>
                        <div class="map-fold-body">
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
                                <!-- A different problem with a different fix: the
                                     module is compiled in, and what is missing is
                                     a key somebody can type on the next screen. -->
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
                        </div>
                    </details>
                {/if}

                {#if settings.languages.length > 1}
                    <details class="card map-fold">
                        <summary>
                            <span class="map-fold-title">Read the catalog as a shopper</span>
                            <span class="txt-hint txt-sm">
                                The public catalog in each of {settings.languages.length} languages
                            </span>
                            <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                        </summary>
                        <div class="map-fold-body">
                            <div class="field-help m-b-sm">
                                The engine negotiates a language per request, renders the public
                                catalog through the translator the store registered, stamps the
                                language on every order and hands it to every notifier. These open
                                the public read for one language, which is the only way to see what
                                a shopper in it is actually served.
                            </div>
                            <div class="flex flex-wrap gap-5">
                                {#each settings.languages as lang (lang)}
                                    <a class="btn sm secondary" href="/api/products?lang={lang}" target="_blank" rel="noreferrer">
                                        <span class="txt">{lang}</span>
                                    </a>
                                {/each}
                            </div>
                        </div>
                    </details>
                {/if}

                <!-- The one personal preference on a page of store facts. -->
                <details class="card map-fold">
                    <summary>
                        <span class="map-fold-title">Appearance</span>
                        <span class="txt-hint txt-sm">
                            Light or dark, for this browser{#if !settings.mediaUploadsEnabled}; and why there is no upload button{/if}
                        </span>
                        <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                    </summary>
                    <div class="map-fold-body">
                        <div class="flex flex-wrap gap-10 m-b-10" style="align-items: center">
                            <ThemeToggle />
                            <span class="txt-hint txt-sm">
                                Until you choose, the panel follows the system setting.
                            </span>
                        </div>
                        <!-- Only when uploads are off: the warning explains a
                             missing upload control, which is a real question. -->
                        {#if !settings.mediaUploadsEnabled}
                            <div class="field-help">
                                No media backend is configured, so the library records files by URL
                                and offers no upload control. That is a supported way to run —
                                point the store at a directory or a media store to change it.
                            </div>
                        {/if}
                    </div>
                </details>

                <details class="card map-fold">
                    <summary>
                        <span class="map-fold-title">API</span>
                        <span class="txt-hint txt-sm">Everything here can be done with curl</span>
                        <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                    </summary>
                    <div class="map-fold-body">
                        <div class="field-help m-b-sm">
                            This panel is a client of the same API as anything else — it has no
                            private endpoints.
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
                </details>

                {#if mayDelete}
                    <details class="card map-fold store-delete-fold">
                        <summary>
                            <span class="map-fold-title">Delete this store</span>
                            <span class="txt-hint txt-sm">For good; nobody can undo it</span>
                            <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                        </summary>
                        <div class="map-fold-body store-delete">
                            <p class="field-help m-b-sm">
                                Deletes {here.name} and everything in it — products, orders, customers,
                                settings — for good. Nobody can undo it. Your other stores are not touched.
                            </p>
                            <div class="field">
                                <label for="delete-confirm">Type <code>{here.slug}</code> to confirm</label>
                                <input
                                    id="delete-confirm"
                                    type="text"
                                    autocomplete="off"
                                    spellcheck="false"
                                    bind:value={confirmSlug}
                                />
                            </div>
                            <button
                                type="button"
                                class="btn danger m-t-sm"
                                class:loading={deleting}
                                disabled={deleting || confirmSlug.trim() !== here.slug}
                                onclick={deleteStore}
                            >
                                <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                <span class="txt">Delete this store</span>
                            </button>
                        </div>
                    </details>
                {/if}
            {/if}
        </div>

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <span class="txt">{brand.name || "Version"} {store?.version ?? ""}</span>
        </footer>
    </div>
</div>

<style>
    .langs {
        display: grid;
        gap: 8px;
    }
    .langs-head {
        display: grid;
        gap: 2px;
    }
    .langs-title {
        font-weight: 600;
    }
    .langs-list {
        display: grid;
        gap: 4px;
        margin: 0;
        padding: 0;
        list-style: none;
    }
    .langs-item {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 4px 8px;
        min-height: 44px;
        padding: 2px 6px 2px 12px;
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
        transition: border-color 150ms ease-out;
    }
    .langs-item.main {
        border-color: color-mix(in srgb, var(--successColor), transparent 50%);
    }
    .langs-name {
        flex: 1 1 auto;
        min-width: 0;
        font-weight: 500;
        overflow-wrap: anywhere;
    }
    .langs-add {
        display: flex;
        align-items: center;
        gap: 8px;
        max-width: 440px;
    }
    .langs-add .field {
        flex: 1 1 auto;
        min-width: 0;
    }
    @media (prefers-reduced-motion: reduce) {
        .langs-item {
            transition: none;
        }
    }
</style>
