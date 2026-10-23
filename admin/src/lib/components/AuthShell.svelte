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

    let { children } = $props();

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

<div class="auth-shell">
    <header class="auth-bar">
        <span class="app-brand auth-brand">
            <!-- The brand of the address this is opened on (D114): its logo
                 where it has one, else the shell's mark, drawn the same way —
                 from parts, in currentColor, so it takes the theme. -->
            {#if brand.logo_url}
                <img class="app-brand-logo" src={brand.logo_url} alt="" height="20" />
            {:else}
                <svg class="app-brand-mark" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false">
                    <rect x="4" y="4" width="3.6" height="16" rx="1" />
                    <path d="M9.6 12 15.4 4h4.4l-5.8 8z" />
                    <path d="M9.6 12h4.4l5.8 8h-4.4z" />
                </svg>
            {/if}
            <span class="app-brand-name">{brand.name}</span>
        </span>
    </header>
    <div class="auth-main">
        <div class="auth-card" class:auth-card-enter={enter}>
            {@render children?.()}
        </div>
    </div>
</div>
