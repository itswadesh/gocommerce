<script>
    import { api } from "$lib/api.js";
    /**
     * PocketBase's settings sidebar: `<details class="nav-group">` groups of
     * `.nav-item` links inside a `.sidebar-content.scrollable`.
     *
     * The `<summary tabindex="-1">` is deliberate — layout.css hides the
     * disclosure triangle and drops the pointer cursor for exactly that case,
     * which turns the group into a plain static heading rather than something
     * that looks collapsible but never collapses.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { can } from "$lib/api.js";
    import { hasModule } from "$lib/modules.svelte.js";
    import { health } from "$lib/health.svelte.js";

    /*
     * `right` gates the link, exactly as the main nav does. Two of them have
     * none. Your own account: every operator reaches it, which is the whole
     * reason it is a separate screen from the team. And Store, which reads the
     * store's own currency, languages and providers from public endpoints —
     * there is nothing there to gate.
     *
     * Import / export takes either half, so requiring both to reach it would
     * hide it from the person who may do one of them. That puts a requirement
     * on the screen rather than on this link: it must gate each half on its own
     * right, because an account holding only data.export reaches it and must
     * not be offered an import it cannot perform.
     *
     * One of them also names a module. A screen served by a module this binary
     * was not built with is not a permission problem and not a 403 — the route
     * is simply not there — so the link is hidden the same way.
     */
    const groups = {
        System: [
            { href: "/settings", label: "Store", icon: "ri-home-gear-line", exact: true },
            { href: "/settings/superusers", label: "Team", icon: "ri-group-line",
              right: "team.read" },
            { href: "/settings/roles", label: "Roles", icon: "ri-shield-user-line",
              right: "roles.write" },
            { href: "/settings/account", label: "Your account", icon: "ri-user-settings-line" },
        ],
        // The store as a running system rather than as a shop, which is what
        // store.operate names. An agent audit trail is an operator's
        // diagnostic and not daily commerce, so it lives here rather than in
        // the main nav beside Orders.
        Platform: [
            // Gated, unlike Store directly above it, and the difference is the
            // reason the two are not in one group: Store reads the public
            // endpoints every screen needs to format money, while this one
            // reads the database and can act on it.
            { href: "/settings/diagnostics", label: "Diagnostics", icon: "ri-pulse-line",
              right: "store.operate", health: true },
            // What the store told the outside world, and what it could not.
            // The report above says a consumer is failing; this is where the
            // rows it is failing on are read and made to go again.
            { href: "/settings/events", label: "Events", icon: "ri-broadcast-line",
              right: "store.operate" },
            // Events is what happened; this is who outside was told. They sit
            // together because the question that reaches this corner of the
            // panel is usually one question: did it get out.
            { href: "/settings/webhooks", label: "Webhooks", icon: "ri-send-plane-line",
              right: "store.operate", module: "webhooks" },
            // Who did what in this store. Behind store.operate for the reason
            // audit_http.go states: filtering the whole store by person is
            // surveillance of the team, which is a heavier thing than reading
            // one record's own history — so it is a heavier right, and it sits
            // here rather than in the main nav beside Orders.
            { href: "/settings/audit", label: "Audit trail", icon: "ri-history-line",
              right: "store.operate" },
            { href: "/settings/agent", label: "Agent activity", icon: "ri-robot-2-line",
              right: "store.operate", module: "mcp" },
            // What this binary can do that is switched on from the panel:
            // storefront extras, widgets, search, marketing. Same right as
            // webhooks, for the same reason — pasting an analytics key is
            // operating the store, not selling.
            { href: "/settings/plugins", label: "Plugins", icon: "ri-puzzle-line",
              right: "store.operate" },
        ],
        // Storefronts sit under Settings rather than the main nav: a store
        // configures them once and then works in the screens they scope, the
        // same shape as the attribute dictionary below.
        Selling: [{ href: "/channels", label: "Channels", icon: "ri-store-2-line",
                    right: "store.operate" }],
        // The attribute dictionary sits under Settings rather than beside
        // Categories in the main nav: it is vocabulary configured once and then
        // consumed from the categories drawer, not a screen worked in daily.
        Catalog: [{ href: "/settings/attributes", label: "Attribute dictionary",
                    icon: "ri-list-settings-line", right: "catalog.read" }],
        Data: [{ href: "/data", label: "Import / export", icon: "ri-file-transfer-line",
                 anyOf: ["data.export", "data.import"] }],
    };

    /**
     * Screens a module contributed, fetched once and merged into the groups
     * below.
     *
     * The engine already filters the listing by the caller's rights, so an
     * entry that arrives here is one this operator may use — the `allowed`
     * test below still runs, because a right can be withdrawn while a panel is
     * open and the nav should lose the entry without a reload.
     */
    let moduleScreens = $state([]);
    $effect(() => {
        api.get("/api/admin/screens")
            .then((result) => (moduleScreens = result.data ?? []))
            // A panel that cannot list module screens is a panel with the
            // built-in ones, not a broken panel.
            .catch(() => (moduleScreens = []));
    });

    /** A group with nothing left in it should not render its heading either. */
    const allowed = (l) =>
        // The module clause first: hasModule() reads a `$state` object, and
        // reading it on every pass is what makes this list re-render when the
        // answer arrives.
        (!l.module || hasModule(l.module)) &&
        (!l.right || can(l.right)) &&
        (!l.anyOf || l.anyOf.some((r) => can(r)));

    const visible = $derived.by(() => {
        // A module screen joins the group it names, or one headed by the module
        // itself when it names none — so a module cannot quietly plant an entry
        // in the middle of the store's own settings without saying where.
        const merged = { ...groups };
        for (const s of moduleScreens) {
            const group = s.group || s.module;
            merged[group] = [
                ...(merged[group] ?? []),
                { href: `/x/${s.slug}`, label: s.title, icon: s.icon || "ri-puzzle-line", right: s.right },
            ];
        }
        return Object.entries(merged)
            .map(([name, links]) => [name, links.filter(allowed)])
            .filter(([, links]) => links.length);
    });

    function isActive(item) {
        const path = page.url.pathname.replace(base, "") || "/";
        return item.exact ? path === item.href : path.startsWith(item.href);
    }
</script>

<aside class="page-sidebar settings-sidebar">
    <nav class="sidebar-content scrollable">
        {#each visible as [name, links] (name)}
            <details class="nav-group" open>
                <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
                <summary tabindex="-1">{name}</summary>
                {#each links as link (link.href)}
                    <a
                        href="{base}{link.href}"
                        class="nav-item"
                        class:active={isActive(link)}
                    >
                        <i class={link.icon} aria-hidden="true"></i>
                        <span class="txt">{link.label}</span>
                        <!-- The flag is named for what drives the dot, not for
                             what the dot looks like. health.visible and not
                             health.failing, so it cannot appear for somebody
                             who does not hold the right. -->
                        {#if link.health && health.visible}
                            <span class="app-nav-dot" title="A health check is failing"></span>
                        {/if}
                    </a>
                {/each}
            </details>
        {/each}
    </nav>
</aside>
