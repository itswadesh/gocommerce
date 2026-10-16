<script>
    /**
     * Home's "Set up your store": the steps in order, the next one to do
     * highlighted with a button that goes there, and every step after an
     * unfinished required one locked — one thing to do, not twenty screens.
     * Recommended steps sit below the required ones and never lock anything.
     */
    import { base } from "$app/paths";
    import { setup } from "$lib/setup.svelte.js";

    const required = $derived(setup.steps.filter((s) => s.required));
    const recommended = $derived(setup.steps.filter((s) => !s.required));
    const pct = $derived(
        setup.progress.total ? Math.round((setup.progress.done / setup.progress.total) * 100) : 0,
    );
    // Where a locked step's "after step N" points: the step that holds it.
    const blocker = $derived(required.find((s) => !s.done));
</script>

{#snippet row(step, n)}
    {@const current = step.key === setup.next}
    <li class="setup-step" class:done={step.done} class:current class:locked={step.locked}>
        <span class="setup-step-mark" aria-hidden="true">
            {#if step.done}
                <i class="ri-check-line"></i>
            {:else if step.locked}
                <i class="ri-lock-line"></i>
            {:else}
                {n}
            {/if}
        </span>
        <div class="setup-step-text">
            <span class="setup-step-title">{step.title}</span>
            {#if !step.done}<span class="setup-step-why">{step.why}</span>{/if}
            {#if step.locked && blocker}
                <span class="setup-step-why">Opens after “{blocker.title}”.</span>
            {/if}
        </div>
        {#if current}
            <a class="btn sm" href="{base}{step.href}">
                <span class="txt">{step.required ? "Do this now" : "Set it up"}</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </a>
        {:else if step.done}
            <a class="btn sm transparent secondary" href="{base}{step.href}">
                <span class="txt">Change</span>
            </a>
        {:else if !step.locked}
            <a class="btn sm transparent secondary" href="{base}{step.href}">
                <span class="txt">Set it up</span>
            </a>
        {/if}
    </li>
{/snippet}

{#if setup.loaded && !setup.complete}
    <section class="setup-guide tw:rounded-xl tw:border tw:bg-card" aria-labelledby="setup-title">
        <header class="setup-guide-head">
            <div>
                <h2 id="setup-title" class="setup-guide-title">
                    {setup.requiredDone ? "Your store can sell — two more to finish" : "Set up your store"}
                </h2>
                <p class="setup-guide-sub">
                    {#if setup.requiredDone}
                        Everything a customer needs to buy is in place. These make it better.
                    {:else}
                        {setup.progress.done} of {setup.progress.total} done. One step at a time — each opens
                        the next.
                    {/if}
                </p>
            </div>
            <div class="setup-guide-bar" role="progressbar" aria-valuenow={pct} aria-valuemin="0" aria-valuemax="100" aria-label="Setup progress">
                <div class="setup-guide-fill" style="transform: scaleX({pct / 100})"></div>
            </div>
        </header>
        <ol class="setup-steps">
            {#each required as step, i (step.key)}
                {@render row(step, i + 1)}
            {/each}
        </ol>
        {#if recommended.length}
            <h3 class="setup-guide-group">Recommended</h3>
            <ol class="setup-steps" start={required.length + 1}>
                {#each recommended as step, i (step.key)}
                    {@render row(step, required.length + i + 1)}
                {/each}
            </ol>
        {/if}
    </section>
{/if}
