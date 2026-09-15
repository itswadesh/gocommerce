<script>
    // Feeds: the Google Merchant and Meta catalogue feeds, and where to find
    // them.
    import PluginPage from "$lib/components/PluginPage.svelte";
</script>

<PluginPage
    pluginKey="product-feeds"
    module="feeds"
    title="Feeds"
    blurb="The catalogue as Google Merchant Center and Meta Commerce read it, rebuilt on every request from what is active right now. Paste the addresses into their dashboards; tell the plugin where the storefront lives so each item links to its page."
    links={[
        { label: "Google Merchant", path: "/x/feeds/google.xml", hint: "RSS 2.0 with the g: namespace" },
        { label: "Meta catalogue", path: "/x/feeds/meta.csv", hint: "CSV, for Commerce Manager" },
    ]}
/>
