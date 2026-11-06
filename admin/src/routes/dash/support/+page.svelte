<script>
    /**
     * Where a store's people reach the install's support. The message starts with
     * the store's name, so nobody has to be asked which store it is, and they can
     * change it in WhatsApp before sending.
     */
    import { titled } from "$lib/brand.svelte.js";
    import { myStores } from "$lib/stores.svelte.js";

    // ponytail: one number for the whole panel. Move it to the brand's settings
    // when a brand needs its own.
    const WHATSAPP = "918249028220";
    const SHOWN = "+91 82490 28220";

    const store = $derived(myStores.current?.name);
    const message = $derived(store ? `Hi, I need help with my store ${store}.` : "Hi, I need help with my store.");
    const link = $derived(`https://wa.me/${WHATSAPP}?text=${encodeURIComponent(message)}`);
</script>

<svelte:head><title>{titled("Support")}</title></svelte:head>

<div class="page page-support shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Support</div>
            </nav>
        </header>

        <section class="card support-card">
            <h2 class="tw:text-sm tw:font-semibold">Message us on WhatsApp</h2>
            <p class="txt-hint">Tell us what you need help with, and which screen you were on.</p>
            <a class="btn support-whatsapp" href={link} target="_blank" rel="noopener noreferrer">
                <i class="ri-whatsapp-line" aria-hidden="true"></i>
                <span class="txt">Chat on WhatsApp</span>
            </a>
            <p class="txt-hint support-number">{SHOWN}</p>
        </section>
    </div>
</div>

<style>
    .support-card {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 12px;
        max-width: 480px;
    }
    .support-card p {
        margin: 0;
    }
    .support-whatsapp {
        min-height: 44px;
        transition: transform 150ms ease-out;
    }
    .support-whatsapp:active {
        transform: scale(0.98);
    }
    @media (prefers-reduced-motion: reduce) {
        .support-whatsapp {
            transition: none;
        }
        .support-whatsapp:active {
            transform: none;
        }
    }
</style>
