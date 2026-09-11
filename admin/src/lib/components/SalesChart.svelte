<script>
    /**
     * The sales chart: one stacked bar per bucket, in CSS.
     *
     * There is no charting library here and there will not be one. The engine
     * has a single production dependency and the panel has no build-time budget
     * for a second — and what this draws is a row of stacked bars, which is a
     * flex container and two custom properties. An SVG viewBox was the other
     * option and it is worse on a phone: it scales its own axis text down with
     * the drawing, where these bars reflow with the container and the labels
     * stay 13px.
     *
     * The column height is the bucket's total against the largest bucket in the
     * window. The three segments inside it are that bucket's paid, outstanding
     * and refunded against its OWN total, so they always fill the column
     * exactly: payment_status is CHECK-constrained to four values and the three
     * slices partition the sale set, which the engine asserts in a test. That
     * stacking is the whole point for a cash-on-delivery store — how much of
     * this month has actually been collected, answered inside the bar rather
     * than on a second screen.
     *
     * The figure is aria-hidden and the table beneath it on the Reports screen
     * is its accessible equivalent, carrying every number in the same order.
     * That is also the relief for the colour contrast: no fact here is ever
     * colour-only.
     */
    import { formatMoney } from "$lib/format.js";

    let { buckets = [], timeZone = "UTC", grain = "day", loading = false } = $props();

    const max = $derived(
        buckets.reduce((top, b) => Math.max(top, b.total?.amount_minor ?? 0), 0),
    );

    /*
     * Labels are formatted from the instant with Intl, in the report's own
     * zone. Never from a bare "YYYY-MM-DD": `new Date("2026-09-08")` is parsed
     * as UTC and renders as the 7th anywhere west of Greenwich, which is how a
     * chart comes to disagree with the table under it.
     */
    function label(bucket) {
        const options =
            grain === "month"
                ? { year: "numeric", month: "short" }
                : { month: "short", day: "numeric" };
        try {
            return new Intl.DateTimeFormat(undefined, { ...options, timeZone }).format(
                new Date(bucket.start),
            );
        } catch {
            // An unknown zone cannot reach here through the API, but a stale
            // tab can outlive a deploy; the reader's own zone is better than a
            // thrown label.
            return new Intl.DateTimeFormat(undefined, options).format(new Date(bucket.start));
        }
    }

    function share(part, whole) {
        const total = whole?.amount_minor ?? 0;
        if (!total) return 0;
        return ((part?.amount_minor ?? 0) / total) * 100;
    }

    function height(bucket) {
        if (!max) return 0;
        return ((bucket.total?.amount_minor ?? 0) / max) * 100;
    }

    function tooltip(bucket) {
        const lines = [
            `${label(bucket)}${bucket.partial ? " (part of a " + grain + ")" : ""}`,
            `${bucket.orders} ${bucket.orders === 1 ? "order" : "orders"} · ${formatMoney(bucket.total)}`,
            `collected ${formatMoney(bucket.paid?.net_collected)}`,
            `owed ${formatMoney(bucket.outstanding?.total)}`,
        ];
        if ((bucket.refunded?.total?.amount_minor ?? 0) > 0) {
            lines.push(`refunded ${formatMoney(bucket.refunded.total)}`);
        }
        return lines.join("\n");
    }
</script>

<div class="report-legend">
    <span><i class="report-key paid" aria-hidden="true"></i> Collected</span>
    <span><i class="report-key outstanding" aria-hidden="true"></i> Awaiting payment</span>
    <span><i class="report-key refunded" aria-hidden="true"></i> Refunded</span>
    {#if buckets.some((b) => b.partial)}
        <span class="txt-hint">Faded bars are a part period.</span>
    {/if}
</div>

{#if loading && !buckets.length}
    <span class="skeleton-loader" style="height: 180px"></span>
{:else}
    <figure class="report-chart" aria-hidden="true">
        {#each buckets as bucket (bucket.start)}
            <div
                class="report-bar"
                class:partial={bucket.partial}
                style="--h: {height(bucket)}%"
                title={tooltip(bucket)}
            >
                <span class="report-seg refunded" style="--seg: {share(bucket.refunded?.total, bucket.total)}%"
                ></span>
                <span
                    class="report-seg outstanding"
                    style="--seg: {share(bucket.outstanding?.total, bucket.total)}%"
                ></span>
                <span class="report-seg paid" style="--seg: {share(bucket.paid?.total, bucket.total)}%"></span>
            </div>
        {/each}
    </figure>

    {#if buckets.length}
        <div class="report-axis">
            <span>{label(buckets[0])}</span>
            <span>{label(buckets[buckets.length - 1])}</span>
        </div>
    {/if}
{/if}
