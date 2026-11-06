<script>
    /** The time now in a zone, so a wrong choice shows itself. Empty is UTC, as the engine reads it. */
    import { zoneOffset } from "$lib/countries.js";

    let { zone = "" } = $props();

    let now = $state(new Date());
    $effect(() => {
        const tick = setInterval(() => (now = new Date()), 10_000);
        return () => clearInterval(tick);
    });

    const shown = $derived.by(() => {
        try {
            const timeZone = zone || "UTC";
            const time = new Intl.DateTimeFormat(undefined, {
                timeZone,
                weekday: "short",
                day: "numeric",
                month: "short",
                hour: "numeric",
                minute: "2-digit",
            }).format(now);
            return `${time} (${zoneOffset(timeZone, now)})`;
        } catch {
            return ""; // A zone typed by hand that this browser cannot read.
        }
    });
</script>

{#if shown}<span class="zone-clock">Current time: {shown}</span>{/if}
