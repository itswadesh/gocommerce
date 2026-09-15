<script>
    /**
     * Reports, at its own address.
     *
     * The screen itself lives in ReportsPanel and is also embedded on the
     * dashboard, which is where the sidebar now sends an owner asking "how
     * much did we sell". This route stays so a bookmark, a shared link with
     * a window in its query string, or a Playwright pass still finds it.
     */
    import ReportsPanel from "$lib/components/ReportsPanel.svelte";
</script>

<ReportsPanel />
