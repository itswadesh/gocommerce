<script>
    /*
     * The panel's home is /dash, as the KitCommerce admin's is. The root
     * stays a route only so typing the store's bare address still lands
     * somewhere: the layout sends a signed-out visitor to sign in first.
     */
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { onMount } from "svelte";

    onMount(() => goto(`${base}/dash`, { replaceState: true }));
</script>
