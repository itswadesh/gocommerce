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
    import SettingsSidebar from "$lib/components/SettingsSidebar.svelte";

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
</script>

<div class="page page-settings">
    <SettingsSidebar />

    <div class="page-content full-height">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item">Settings</div>
                <div class="breadcrumb-item">Store</div>
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
                            <label for="language">Default language</label>
                            <input
                                id="language"
                                type="text"
                                readonly
                                value={store?.default_language ?? ""}
                            />
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
                        <h6 class="section-title">
                            <i class="ri-bank-card-line" aria-hidden="true"></i>
                            Installed capabilities
                        </h6>
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

                        <h6 class="section-title">
                            <i class="ri-truck-line" aria-hidden="true"></i>
                            Shipping
                        </h6>
                        <div class="flex flex-wrap gap-5 m-b-10">
                            {#each store?.fulfillment_providers ?? [] as provider (provider.code)}
                                <span class="label info" title={provider.code}>
                                    {provider.name}
                                    <span class="label">{provider.module}</span>
                                </span>
                            {/each}
                        </div>
                        <div class="field-help">
                            The same arrangement on the shipping side: manual fulfilment is built
                            in, and a carrier module joins this list by registering a provider.
                        </div>
                    </div>

                    <div class="col-12">
                        <h6 class="section-title">
                            <i class="ri-code-s-slash-line" aria-hidden="true"></i>
                            API
                        </h6>
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

        <footer class="page-footer">
            <span class="txt">GoCommerce {store?.version ?? ""}</span>
            <ThemeToggle />
        </footer>
    </div>
</div>
