<script>
    /**
     * Where a store's people reach the install's support, by email: the owner took
     * the phone number and WhatsApp off it (14 Nov 2026). The message starts with
     * the store's name, so nobody has to be asked which store it is, and they can
     * change it before sending.
     */
    import { base } from "$app/paths";
    import { titled } from "$lib/brand.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { myStores } from "$lib/stores.svelte.js";

    // ponytail: one address for the whole panel. Move it to the brand's settings
    // when a brand needs its own.
    const EMAIL = "hi@shopnx.in";

    const store = $derived(myStores.current?.name);
    const message = $derived(store ? `Hi, I need help with my store ${store}.` : "Hi, I need help with my store.");
    const mailTo = (text) => `mailto:${EMAIL}?subject=${encodeURIComponent("Support request")}&body=${encodeURIComponent(text)}`;
    const link = $derived(mailTo(message));
    const setupLink = $derived(mailTo(store ? `Hi, I need help setting up my store ${store}.` : "Hi, I need help setting up my store."));

    // Sent as emails like the rest of this page, so they reach the same
    // person, and the store's name comes along. The first words say which kind it
    // is. An empty box has no link, so nothing blank can be sent.
    const fromStore = $derived(store ? ` from my store ${store}` : "");
    let issue = $state("");
    const reported = $derived(issue.trim());
    const issueLink = $derived(reported && mailTo(`Issue report${fromStore}: ${reported}`));
    let feature = $state("");
    const asked = $derived(feature.trim());
    const featureLink = $derived(asked && mailTo(`Feature request${fromStore}: ${asked}`));

    // Each opens as its own tab, so the panel stays where the person left it.
    const READING = [
        { href: "https://shopnx.in/help", icon: "ri-book-open-line", label: "Help guides", hint: "Step-by-step guides for running your store." },
        { href: `${base}/docs`, icon: "ri-code-s-slash-line", label: "API reference", hint: "For connecting your own apps and tools." },
        { href: "https://shopnx.in/updates", icon: "ri-megaphone-line", label: "What's new", hint: "New features and fixes, as they ship." },
    ];
    // The ShopNx tutorials on YouTube, in the order a new store needs them. One
    // with `from` is scheduled and private until then, so it is left out until it
    // publishes rather than shown as a dead link.
    const PLAYLIST = "https://www.youtube.com/playlist?list=PLL5RUDvO6zNE";
    const VIDEOS = [
        { id: "xJ0lxasdQkw", label: "Add your store details and GSTIN", from: "2026-11-10T13:00:00Z" },
        { id: "uqpDvr_nQjk", label: "Add a product" },
        { id: "kz8LaZvm-MI", label: "Add sizes and variants" },
        { id: "QE9hgdVIZJI", label: "Create a collection" },
        { id: "oilaWywnu9E", label: "Import products from a CSV file" },
        { id: "fAOzhuSh-Tk", label: "Set up cash on delivery and payments" },
        { id: "tWt7c84QaOw", label: "Set up shipping zones and rates", from: "2026-11-08T13:00:00Z" },
        { id: "8N3YKqye_bU", label: "Add policy pages", from: "2026-11-12T13:00:00Z" },
        { id: "CJVX67nku_k", label: "Connect your own domain", from: "2026-11-14T13:00:00Z" },
        { id: "kVDYeyc9IjY", label: "Create a discount code" },
        { id: "LBmSceBUsrA", label: "Add an FAQ page", from: "2026-11-17T13:00:00Z" },
        { id: "S9DitSPU1D8", label: "Issue gift cards", from: "2026-11-19T13:00:00Z" },
        { id: "w5rEdgJ6O7Y", label: "Price jewellery by the gold rate", from: "2026-11-21T13:00:00Z" },
        { id: "bGvaJ1Yfe6k", label: "Track stock and what's running low", from: "2026-11-24T13:00:00Z" },
        { id: "ROlg-O6Z-N0", label: "Ship an order and mark COD paid" },
        { id: "aJJ-UQAurF8", label: "Invite a team member" },
        { id: "d3VOICVlO80", label: "Get help and request a feature", from: "2026-11-26T13:00:00Z" },
    ];
    const videos = VIDEOS.filter((v) => !v.from || Date.now() >= Date.parse(v.from));
    // Where stores most often need to go. Rights match the sidebar's (nav.js), so
    // nobody is sent to a screen that would refuse them.
    const SETUP = [
        { href: "/dash/settings", icon: "ri-store-2-line", label: "Store details", hint: "Name, address, currency and timezone." },
        { href: "/dash/settings/payments", icon: "ri-bank-card-line", label: "Payment methods", hint: "Choose how shoppers pay.", right: "plugins.read" },
        { href: "/dash/shipping-settings", icon: "ri-truck-line", label: "Shipping and delivery", hint: "Zones, rates and carriers.", right: "shipping.read" },
        { href: "/dash/settings/domains", icon: "ri-global-line", label: "Domains", hint: "Use your own web address." },
        { href: "/dash/data/import", icon: "ri-upload-2-line", label: "Import products", hint: "Bring in a CSV, including one from Shopify.", right: "data.import" },
        { href: "/dash/settings/teams", icon: "ri-group-line", label: "Team", hint: "Invite people and set what they can do.", right: "team.read" },
    ];
    const setup = $derived(SETUP.filter((l) => !l.right || can(l.right)));
</script>

<svelte:head><title>{titled("Support")}</title></svelte:head>

<div class="page page-support shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Support</div>
            </nav>
        </header>

        {#snippet sendByEmail(href, label)}
            {#if href}
                <a class="btn support-send" {href}>
                    <i class="ri-mail-send-line" aria-hidden="true"></i>
                    <span class="txt">{label}</span>
                </a>
            {:else}
                <button type="button" class="btn support-send" disabled>
                    <i class="ri-mail-send-line" aria-hidden="true"></i>
                    <span class="txt">{label}</span>
                </button>
            {/if}
        {/snippet}

        <div class="support-cards">
            <section class="card support-card">
                <h2 class="tw:text-sm tw:font-semibold">Email us</h2>
                <p class="txt-hint">Tell us what you need help with, and which screen you were on.</p>
                <a class="btn support-send" href={link}>
                    <i class="ri-mail-send-line" aria-hidden="true"></i>
                    <span class="txt">Email us</span>
                </a>
                <p class="txt-hint support-number">{EMAIL}</p>
                <p class="support-promise">
                    <i class="ri-shield-check-line" aria-hidden="true"></i>
                    <span>No unwanted, promotional or spam messages. Guaranteed.</span>
                </p>
            </section>
    
            <section class="card support-card support-form">
                <h2 class="tw:text-sm tw:font-semibold">Report an issue</h2>
                <p class="txt-hint">Tell us what went wrong and which screen you were on. You can attach a screenshot to the email.</p>
                <div class="field support-form-field">
                    <label for="support_issue">What went wrong?</label>
                    <textarea id="support_issue" rows="3" maxlength="1000" bind:value={issue}></textarea>
                </div>
                {@render sendByEmail(issueLink, "Send report by email")}
            </section>
    
            <section class="card support-card support-form">
                <h2 class="tw:text-sm tw:font-semibold">Request a new feature for free</h2>
                <p class="txt-hint">Found a feature somewhere and want it in your store? Send us the details, and we will respond within 24 hours. Asking costs nothing.</p>
                <div class="field support-form-field">
                    <label for="support_feature">What should we add?</label>
                    <textarea id="support_feature" rows="3" maxlength="1000" bind:value={feature}></textarea>
                </div>
                {@render sendByEmail(featureLink, "Send request by email")}
            </section>
        </div>

        <h2 class="support-heading tw:text-sm tw:font-semibold">Read and learn</h2>
        <ul class="support-links">
            {#each READING as l (l.href)}
                <li>
                    <a class="card support-link" href={l.href} target="_blank" rel="noopener noreferrer">
                        <i class={l.icon} aria-hidden="true"></i>
                        <span class="support-link-text">
                            <span class="support-link-label">{l.label}<span class="support-new-tab"> (opens in a new tab)</span></span>
                            <span class="txt-hint support-link-hint">{l.hint}</span>
                        </span>
                        <i class="ri-external-link-line" aria-hidden="true"></i>
                    </a>
                </li>
            {/each}
        </ul>

        <section class="card support-card support-setup-help">
            <h2 class="tw:text-sm tw:font-semibold">Need help setting up your store? Talk to us anytime.</h2>
            <a class="btn support-send" href={setupLink}>
                <i class="ri-mail-send-line" aria-hidden="true"></i>
                <span class="txt">Email us</span>
            </a>
        </section>

        <div class="support-video-head">
            <h2 class="support-heading tw:text-sm tw:font-semibold">Video tutorials</h2>
            <a class="support-playlist" href={PLAYLIST} target="_blank" rel="noopener noreferrer">
                <i class="ri-play-list-line" aria-hidden="true"></i>
                Watch them all<span class="support-new-tab"> (opens in a new tab)</span>
            </a>
        </div>
        <ul class="support-videos">
            {#each videos as v (v.id)}
                <li>
                    <a class="card support-video" href="https://youtu.be/{v.id}" target="_blank" rel="noopener noreferrer">
                        <!-- YouTube's own thumbnail, so it changes when the video's does. -->
                        <img src="https://i.ytimg.com/vi/{v.id}/mqdefault.jpg" alt="" width="320" height="180" loading="lazy" decoding="async" />
                        <span class="support-video-label">
                            <i class="ri-youtube-line" aria-hidden="true"></i>
                            <span>{v.label}<span class="support-new-tab"> (opens in a new tab)</span></span>
                        </span>
                    </a>
                </li>
            {/each}
        </ul>

        <h2 class="support-heading tw:text-sm tw:font-semibold">Set up your store</h2>
        <ul class="support-links">
            {#each setup as l (l.href)}
                <li>
                    <a class="card support-link" href="{base}{l.href}">
                        <i class={l.icon} aria-hidden="true"></i>
                        <span class="support-link-text">
                            <span class="support-link-label">{l.label}</span>
                            <span class="txt-hint support-link-hint">{l.hint}</span>
                        </span>
                        <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                    </a>
                </li>
            {/each}
        </ul>
    </div>
</div>

<style>
    .support-promise {
        display: flex;
        align-items: center;
        gap: 6px;
        font-weight: 500;
    }
    /* Past this, lines and tiles get too long to read on a wide monitor. */
    .support-cards,
    .support-links,
    .support-card.support-setup-help {
        max-width: 1200px;
    }
    /* One column on a phone, side by side once there is room for two. */
    .support-cards {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(min(100%, 300px), 1fr));
        gap: 16px;
    }
    .support-cards .support-card {
        margin: 0;
    }
    .support-form-field {
        width: 100%;
    }
    /* A heading and one button: a row when they fit, stacked when they don't. */
    .support-card.support-setup-help {
        flex-flow: row wrap;
        align-items: center;
        justify-content: space-between;
        margin-top: 24px;
    }
    .support-heading {
        margin: 24px 0 8px;
    }
    .support-links {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(min(100%, 280px), 1fr));
        gap: 12px;
        margin: 0;
        padding: 0;
        list-style: none;
    }
    .support-links li {
        display: flex;
    }
    .support-link {
        display: flex;
        align-items: center;
        gap: 12px;
        width: 100%;
        min-height: 44px;
        margin: 0;
        color: inherit;
        text-decoration: none;
        transition:
            box-shadow 200ms ease-out,
            transform 150ms ease-out;
    }
    .support-link:hover,
    .support-link:focus-visible {
        box-shadow: 0 2px 10px rgba(16, 24, 40, 0.12);
    }
    .support-link:active {
        transform: scale(0.98);
    }
    .support-link-text {
        flex: 1;
        min-width: 0;
    }
    .support-link-label,
    .support-link-hint {
        display: block;
    }
    .support-link-label {
        font-weight: 600;
    }
    .support-link-hint {
        font-size: var(--smFontSize);
    }
    /* For a screen reader: sighted people have the icon. */
    .support-new-tab {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip-path: inset(50%);
        white-space: nowrap;
    }
    .support-card {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 12px;
    }
    .support-card p {
        margin: 0;
    }
    .support-send {
        min-height: 44px;
        transition: transform 150ms ease-out;
    }
    .support-send:active {
        transform: scale(0.98);
    }
    .support-video-head {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 12px;
        max-width: 1200px;
    }
    .support-playlist {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        min-height: 44px;
        font-weight: 600;
    }
    .support-videos {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(min(100%, 220px), 1fr));
        gap: 12px;
        max-width: 1200px;
        margin: 0;
        padding: 0;
        list-style: none;
    }
    .support-videos li {
        display: flex;
    }
    .support-video {
        display: flex;
        flex-direction: column;
        width: 100%;
        margin: 0;
        padding: 0;
        overflow: hidden;
        color: inherit;
        text-decoration: none;
        transition:
            box-shadow 200ms ease-out,
            transform 150ms ease-out;
    }
    .support-video img {
        display: block;
        width: 100%;
        height: auto;
        aspect-ratio: 16 / 9;
        object-fit: cover;
        background: var(--surfaceAlt2Color);
    }
    .support-video-label {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        padding: 10px 12px 12px;
        font-weight: 600;
    }
    .support-video:hover,
    .support-video:focus-visible {
        box-shadow: 0 2px 10px rgba(16, 24, 40, 0.12);
    }
    .support-video:active {
        transform: scale(0.98);
    }
    @media (prefers-reduced-motion: reduce) {
        .support-send {
            transition: none;
        }
        .support-send:active,
        .support-video:active,
        .support-link:active {
            transform: none;
        }
    }
</style>
