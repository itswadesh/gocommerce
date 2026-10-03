<script>
    /**
     * One portal screen: its heading, its actions, and the notices that
     * belong on every screen while they are true — an address waiting to be
     * confirmed, a company on hold or closed. Written once here so no screen
     * can forget to say why an order is about to be refused.
     *
     * The page carries `.b2b-page` and `.shopify-skin`, so the B2B screens'
     * classes and the design tokens reach it as they do the store's own
     * business screens, and `.portal-page`, which is what the route
     * transition moves.
     */
    import { base } from "$app/paths";
    import { toast } from "$lib/toast.svelte.js";
    import { trade, tradeApi, explain } from "$lib/trade.svelte.js";

    let { title, back = null, actions, children } = $props();

    const company = $derived(trade.me?.company);

    let resending = $state(false);
    async function resendConfirmation() {
        resending = true;
        try {
            await tradeApi.post("/x/identity/me/email-verification", {});
            toast.success(`We've sent a link to ${trade.account?.email}.`);
        } catch (err) {
            toast.error(explain(err));
        } finally {
            resending = false;
        }
    }
</script>

<svelte:head><title>{title} · {trade.store.name || "Trade account"}</title></svelte:head>

<div class="page portal-page b2b-page shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header portal-header">
            <nav class="breadcrumbs" aria-label="Where you are">
                {#if back}
                    <a href="{base}{back.href}" class="breadcrumb-item tw:text-sm tw:text-muted-foreground portal-back">
                        <i class="ri-arrow-left-line" aria-hidden="true"></i>
                        {back.label}
                    </a>
                {:else}
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">{company?.name}</div>
                {/if}
                <div class="breadcrumb-item">
                    <h1 class="portal-title tw:text-2xl tw:font-semibold tw:tracking-tight">{title}</h1>
                </div>
            </nav>
            <div class="flex-fill"></div>
            {#if actions}
                <div class="page-header-primary-btns portal-actions">{@render actions()}</div>
            {/if}
        </header>

        {#if trade.account && !trade.account.email_verified}
            <div class="alert warning m-b-base portal-notice" role="status">
                <p>
                    Confirm <strong>{trade.account.email}</strong> to order at {company?.name}'s prices. We sent a
                    link when the address changed.
                    <button
                        type="button"
                        class="btn sm secondary portal-press portal-inline-btn"
                        class:loading={resending}
                        disabled={resending}
                        onclick={resendConfirmation}
                    >
                        <span class="txt">Send it again</span>
                    </button>
                </p>
            </div>
        {/if}
        {#if company?.status === "on_hold"}
            <div class="alert warning m-b-base portal-notice" role="status">
                <p>
                    <strong>{company.name}'s account is on hold.</strong> Orders can't go on account until
                    {trade.store.name || "the store"} lifts the hold — you can still pay when you order.
                </p>
            </div>
        {:else if company?.status === "closed"}
            <div class="alert danger m-b-base portal-notice" role="status">
                <p>
                    <strong>{company.name}'s account is closed.</strong> You can read your orders and quotes,
                    but not place new ones.
                </p>
            </div>
        {/if}

        {@render children?.()}
    </div>
</div>
