<script>
    /**
     * The SEO score on the product editor, in RankMath's shape: a focus
     * keyword, a number out of a hundred, and the checks behind it split into
     * what is wrong and what is already right.
     *
     * The split is the whole design. A flat list of thirteen rows is thirteen
     * things to read every time the card is opened, and the eleven that pass
     * are the ones nobody needs. Failures are open and named; passes collapse
     * to a count you can expand when you want to know what was measured.
     *
     * The score moves as you type, because it scores the form rather than the
     * saved product — a number that only updates after Save cannot teach you
     * that moving two words changed it.
     *
     * What this does NOT claim is a full audit. The engine serves an API, not
     * the storefront, so nothing here has seen the rendered page: no headings,
     * no internal links, no load time. The footnote says so, because a score
     * that implies a crawl it never did is worse than no score.
     */
    import { analyse } from "$lib/seo.js";

    /** @type {{ input: any, keyword: string, disabled?: boolean }} */
    let { input, keyword = $bindable(""), disabled = false } = $props();

    let showPassed = $state(false);

    const result = $derived(analyse({ ...input, keyword }));
    // The circumference of the r=26 ring below, so the dash offset is a
    // percentage rather than a magic number.
    const RING = 2 * Math.PI * 26;
    const dash = $derived(RING - (RING * result.score) / 100);
</script>

<div class="seo-score">
    <div class="seo-score-dial seo-band-{result.band}">
        <svg viewBox="0 0 60 60" aria-hidden="true">
            <circle class="seo-ring-track" cx="30" cy="30" r="26" />
            <circle
                class="seo-ring-value"
                cx="30"
                cy="30"
                r="26"
                stroke-dasharray={RING}
                stroke-dashoffset={dash}
            />
        </svg>
        <div class="seo-score-n">{result.score}</div>
    </div>
    <div class="seo-score-text">
        <div class="seo-score-band seo-band-{result.band}">{result.bandLabel}</div>
        <div class="txt-hint txt-sm">
            {#if result.failed.length}
                {result.failed.length}
                {result.failed.length === 1 ? "thing" : "things"} to fix
            {:else if result.skipped.length}
                Everything that could be judged passes; {result.skipped.length}
                {result.skipped.length === 1 ? "check" : "checks"} could not be.
            {:else}
                Everything this screen can check is in place.
            {/if}
        </div>
    </div>
</div>

<div class="field m-t-sm">
    <label for="seo-keyword">Focus keyword</label>
    <input
        id="seo-keyword"
        type="text"
        placeholder="The phrase a buyer would type"
        bind:value={keyword}
        {disabled}
    />
</div>
<div class="field-help">
    One phrase, not a list. Everything below is measured against it, and it is
    saved with the product.
</div>

{#if result.failed.length}
    <ul class="seo-checks m-t-sm">
        {#each result.failed as c (c.key)}
            <li class="seo-check is-bad">
                <i class="ri-close-line" aria-hidden="true"></i>
                <div>
                    <div class="txt-bold">{c.label}</div>
                    <div class="txt-hint txt-sm">{c.hint}</div>
                </div>
            </li>
        {/each}
    </ul>
{/if}

{#if result.skipped.length}
    <!-- Each names what it waits on. Reporting them all as "waiting on a
         focus keyword" was wrong whenever the keyword was set and the
         picture was the missing thing, which is the common case. -->
    <ul class="seo-checks m-t-sm">
        {#each result.skipped as c (c.key)}
            <li class="seo-check is-waiting">
                <i class="ri-time-line" aria-hidden="true"></i>
                <div>
                    <div>{c.label}</div>
                    <div class="txt-hint txt-sm">{c.why} — not counted either way.</div>
                </div>
            </li>
        {/each}
    </ul>
{/if}

{#if result.passed.length}
    <button
        type="button"
        class="btn sm transparent secondary seo-passed-toggle"
        aria-expanded={showPassed}
        onclick={() => (showPassed = !showPassed)}
    >
        <i class={showPassed ? "ri-arrow-down-s-line" : "ri-arrow-right-s-line"} aria-hidden="true"
        ></i>
        <span class="txt">{result.passed.length} already right</span>
    </button>
    {#if showPassed}
        <ul class="seo-checks">
            {#each result.passed as c (c.key)}
                <li class="seo-check is-good">
                    <i class="ri-check-line" aria-hidden="true"></i>
                    <div>
                        <div>{c.label}</div>
                    </div>
                </li>
            {/each}
        </ul>
    {/if}
{/if}

<p class="txt-hint txt-sm seo-caveat">
    Measured from this product alone. The storefront is somebody else's, so nothing here has read
    the rendered page — its headings, its links and how fast it loads are not in this number.
</p>
