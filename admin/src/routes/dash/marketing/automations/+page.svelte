<script>
    /**
     * Marketing › Automations: what the store sends to shoppers unasked.
     *
     * Two today, both ext/cart-recovery's: the reminder sequence for a basket
     * that reached checkout, and the one for a basket that did not. Each row
     * says whether it is on, when a basket counts as abandoned and what the
     * sequence sends, and leads to its editor. The two things that make every
     * step a no-op whatever the editors say — no email provider, no storefront
     * address for the link to land on — are said above the rows, with where to
     * fix each, because an automation that is "on" and sends nothing is the
     * failure this screen exists to make visible.
     */
    import { base } from "$app/paths";
    import { can, recovery } from "$lib/api.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { durationWords, sequenceSummary } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const readable = $derived(can("abandonment.read"));
    /* Trusted only where the store could have been asked: the probe is made for
       an operator holding this right or orders.read (modules.svelte.js), and
       for anybody else the rights answer below is the true one. */
    const missing = $derived(
        modulesKnown() && !hasModule("cart-recovery") && (readable || can("orders.read")),
    );

    let view = $state(null);
    let loading = $state(true);

    $effect(() => {
        if (readable && hasModule("cart-recovery")) load();
    });

    async function load() {
        loading = true;
        try {
            view = await recovery.settings();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    const rows = $derived(
        view
            ? [
                  {
                      key: "checkout",
                      href: "/dash/marketing/automations/abandoned-checkout",
                      title: "Abandoned checkout recovery",
                      what: "Baskets that reached checkout — the shopper typed an email address — and were left there.",
                      icon: "ri-shopping-bag-3-line",
                      a: view.settings.checkout,
                  },
                  {
                      key: "cart",
                      href: "/dash/marketing/automations/abandoned-cart",
                      title: "Abandoned cart recovery",
                      what: "Baskets left before checkout. Reachable only when the shopper was signed in.",
                      icon: "ri-shopping-cart-2-line",
                      a: view.settings.cart,
                  },
              ]
            : [],
    );

    const storefront = $derived(view ? view.settings.storefront_url || view.storefront_fallback || "" : "");
</script>

<svelte:head><title>Automations · GoCommerce</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="the marketing automations" />
{:else}
    <div class="page page-automations shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Marketing</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Automations</div>
                </nav>
                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        disabled={loading}
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
                {#if !missing}
                    <div class="page-header-primary-btns">
                        <a class="btn sm secondary" href="{base}/dash/marketing/recovery">
                            <i class="ri-bar-chart-2-line" aria-hidden="true"></i>
                            <span class="txt">Recovery analytics</span>
                        </a>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="cart-recovery"
                    what="The recovery automations are ext/cart-recovery's, and this binary does not have it."
                />
            {:else}
                <div class="recovery-page">
                    {#if view && !view.email_delivers}
                        <div class="alert warning recovery-banner" role="status">
                            <i class="ri-mail-close-line" aria-hidden="true"></i>
                            <div>
                                <p><strong>No email provider is set up, so no reminder can go out.</strong></p>
                                <p>
                                    The sequences below run on schedule and stop at the send.
                                    <a class="recovery-arrow-link" href="{base}/dash/notifications/email">
                                        Set one up in Notifications › Setup Email <span aria-hidden="true">→</span>
                                    </a>
                                </p>
                            </div>
                        </div>
                    {/if}
                    {#if view && !storefront}
                        <div class="alert warning recovery-banner" role="status">
                            <i class="ri-store-2-line" aria-hidden="true"></i>
                            <div>
                                <p><strong>No storefront address is set.</strong></p>
                                <p>
                                    A reminder carries a basket reference instead of a link, and Copy link is unavailable.
                                    <a class="recovery-arrow-link" href="{base}/dash/marketing/automations/abandoned-checkout#storefront">
                                        Set the address <span aria-hidden="true">→</span>
                                    </a>
                                </p>
                            </div>
                        </div>
                    {/if}

                    <div class="recovery-automations tw:rounded-xl tw:border tw:bg-card">
                        {#if loading && !view}
                            {#each Array(2) as _, i (i)}
                                <div class="recovery-automation">
                                    <span class="skeleton-loader lg"></span>
                                    <span class="skeleton-loader"></span>
                                </div>
                            {/each}
                        {/if}
                        {#each rows as row (row.key)}
                            <a class="recovery-automation" href="{base}{row.href}">
                                <span class="recovery-automation-icon" aria-hidden="true"><i class={row.icon}></i></span>
                                <span class="recovery-automation-text">
                                    <span class="recovery-automation-title">
                                        {row.title}
                                        <span class="recovery-state" class:is-on={row.a.enabled}>
                                            <span class="recovery-dot {row.a.enabled ? 'success' : 'neutral'}" aria-hidden="true"></span>
                                            {row.a.enabled ? "On" : "Off"}
                                        </span>
                                    </span>
                                    <span class="txt-hint txt-sm">{row.what}</span>
                                    <span class="recovery-automation-facts txt-sm">
                                        <span>Counts as abandoned after {durationWords(row.a.abandon_after_minutes)} without activity</span>
                                        <span>{sequenceSummary(row.a.steps)}</span>
                                    </span>
                                </span>
                                <span class="recovery-automation-go txt-hint txt-sm">
                                    Edit <i class="ri-arrow-right-line" aria-hidden="true"></i>
                                </span>
                            </a>
                        {/each}
                    </div>

                    {#if view}
                        <p class="txt-hint txt-sm m-t-sm">
                            {#if view.customized && view.updated_at}
                                Last changed <span title={formatDate(view.updated_at)}>{relativeTime(view.updated_at)}</span>{view.updated_by
                                    ? ` by ${view.updated_by === "token" ? "an admin token" : view.updated_by}`
                                    : ""}.
                            {:else}
                                Running the defaults this release ships — nobody has saved these yet.
                            {/if}
                            Recovery was set up {formatDate(view.installed_at, { withTime: false })}; baskets left before then
                            are listed and never written to.
                            {#if storefront && !view.tracked}
                                Clicks are not counted: the store has no public address of its own set.
                            {/if}
                        </p>
                    {/if}
                </div>
            {/if}
        </div>
    </div>
{/if}
