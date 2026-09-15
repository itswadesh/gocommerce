<script>
    // Sitemap: the storefront's sitemap, and where to submit it.
    import PluginPage from "$lib/components/PluginPage.svelte";
</script>

<PluginPage
    pluginKey="sitemap"
    module="sitemaps"
    title="Sitemap"
    blurb="Every active product, collection and page as a sitemap search engines can crawl, rebuilt on every request. Tell the plugin where the storefront lives and how its pages are addressed, then submit the address in Search Console."
    links={[{ label: "Sitemap", path: "/x/sitemaps/sitemap.xml", hint: "sitemaps.org XML" }]}
/>
