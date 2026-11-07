<script>
    /**
     * Every screen somebody sees before they are inside a store: signing in,
     * signing up, a forgotten password, a reset link, an invitation, and the
     * store picker that follows.
     *
     * Built for the phone first, because that is where these screens are met
     * most — from an email, on the way somewhere. So the form sits at the top
     * rather than in the vertical middle (a centred card jumps when the
     * keyboard opens and covers half the screen), it keeps a gutter on both
     * sides, and its fields and buttons are the full PocketBase size rather
     * than the compact one the store's screens use. On a wider screen the
     * same column becomes a card in the middle of the page.
     *
     * Moving between these screens animates the card alone, as a view
     * transition, so the brand bar holds still while the form changes.
     */
    import { onNavigate } from "$app/navigation";
    import { base } from "$app/paths";
    import { brand } from "$lib/brand.svelte.js";

    // hero sets the sign-in and sign-up cards beside a headline on a wide screen.
    let { children, hero = false } = $props();

    // The screens this transition is for. Leaving them (into the panel) is the
    // shell's own business.
    const SIGNED_OUT = ["/admin/auth", "/select-store"];
    const signedOut = (path) => SIGNED_OUT.some((p) => path.replace(base, "").startsWith(p));

    /*
     * The card rises in once, when the page first opens. A card that arrives
     * inside a view transition has that transition as its entrance, and an
     * animation of its own as well would play again the moment the
     * transition's class came off the root — a second rise, after the first.
     */
    const enter = typeof document === "undefined" || !document.documentElement.classList.contains("auth-vt");

    onNavigate((navigation) => {
        if (typeof document.startViewTransition !== "function" || navigation.willUnload) return;
        const from = navigation.from?.url.pathname;
        const to = navigation.to?.url.pathname;
        if (!from || !to || from === to || !signedOut(from) || !signedOut(to)) return;
        return new Promise((resolve) => {
            const root = document.documentElement;
            root.classList.add("auth-vt");
            const transition = document.startViewTransition(async () => {
                resolve();
                await navigation.complete;
            });
            transition.finished.finally(() => root.classList.remove("auth-vt"));
        });
    });
</script>

<div class="auth-shell" class:auth-with-hero={hero}>
    <header class="auth-bar">
        <span class="app-brand auth-brand">
            <!-- The brand of the address this is opened on (D114): its logo
                 where it has one, else the shell's mark, drawn the same way —
                 from parts, in currentColor, so it takes the theme. -->
            {#if brand.logo_url}
                <img class="app-brand-logo" src={brand.logo_url} alt="" height="20" />
            {:else}
                <svg class="app-brand-mark" viewBox="0 0 30 34" width="18" height="20" aria-hidden="true" focusable="false"><path d="M5 11h20l3 20H2L5 11Z" /><path d="M10 15V8a5 5 0 0 1 10 0v7" /></svg>
            {/if}
            <span class="app-brand-name">{brand.name}</span>
        </span>
    </header>
    <div class="auth-main">
        {#if hero}
            <aside class="auth-hero">
                <p class="auth-hero-title">Your store. <span>Ready to sell.</span></p>
                <p class="auth-hero-lede">Add your products, take payments and ship orders, all from one panel.</p>
                <ul class="auth-hero-points">
                    <li><i class="ri-store-2-line" aria-hidden="true"></i>Your own store, on your own web address</li>
                    <li><i class="ri-secure-payment-line" aria-hidden="true"></i>Online payments and cash on delivery</li>
                    <li><i class="ri-smartphone-line" aria-hidden="true"></i>Run it from your phone or your computer</li>
                </ul>
            </aside>
        {/if}
        <div class="auth-card" class:auth-card-enter={enter}>
            {@render children?.()}
        </div>
    </div>
</div>
