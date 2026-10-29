<script>
    import { base } from "$app/paths";
    import { api, can } from "$lib/api.js";
    import { formatMicro } from "$lib/ai.js";
    import { sentence } from "$lib/format.js";
    import { rise } from "$lib/motion.js";
    import { applied, refusal, rowsFor } from "$lib/catalogai.js";
    import Drawer from "$lib/components/Drawer.svelte";

    let {
        open = false,
        productId,
        form,
        media = [],
        categories = [],
        canWriteMedia = false,
        dirty = false,
        onapply,
        onclose,
    } = $props();

    let view = $state({ kind: "loading" });
    let chosen = $state(new Set());
    let generation = 0;

    const rows = $derived(
        view.kind === "ready" ? rowsFor(view.proposal, { form, media, categories, canWriteMedia }) : [],
    );

    $effect(() => {
        if (open) ask();
    });

    async function ask() {
        const mine = ++generation;
        view = { kind: "loading" };
        try {
            const status = await api.get("/api/admin/x/catalog-ai/status");
            if (mine !== generation) return;
            if (!status.ready) {
                view = { kind: "refused", ...refusal({ code: "ai_not_ready", message: status.reason }) };
                return;
            }
            const proposal = await api.post(`/api/admin/x/catalog-ai/products/${productId}/improve`);
            if (mine !== generation || proposal.product_id !== productId) return;
            view = { kind: "ready", proposal };
            chosen = new Set(rowsFor(proposal, { form, media, categories, canWriteMedia }).map((r) => r.key));
        } catch (err) {
            if (mine === generation) view = { kind: "refused", ...refusal(err) };
        }
    }

    function close() {
        generation++;
        onclose?.();
    }

    function toggle(key) {
        const next = new Set(chosen);
        if (next.has(key)) next.delete(key);
        else next.add(key);
        chosen = next;
    }

    function apply() {
        const keys = new Set(rows.map((r) => r.key).filter((k) => chosen.has(k)));
        generation++;
        onapply?.(applied(form, view.proposal.changes, keys), keys.size);
    }

    const picked = $derived(rows.filter((r) => chosen.has(r.key)).length);
</script>

{#snippet value(v)}
    {#if Array.isArray(v)}
        {#if v.length}
            <ul class="improve-list">
                {#each v as line, i (i)}<li>{line}</li>{/each}
            </ul>
        {:else}
            <span class="txt-hint">None</span>
        {/if}
    {:else if String(v ?? "").trim()}
        <span class="improve-text">{v}</span>
    {:else}
        <span class="txt-hint">Empty</span>
    {/if}
{/snippet}

<Drawer {open} title="Improve with AI" size="md" onclose={close}>
    {#if view.kind === "loading"}
        <div class="improve-loading" role="status" aria-live="polite">
            <p class="tw:text-sm tw:text-muted-foreground tw:mb-3">
                Reading the product and asking the model. This can take up to a minute.
            </p>
            {#each [0, 1, 2] as i (i)}
                <div class="improve-skeleton" style:animation-delay="{i * 150}ms" aria-hidden="true">
                    <span></span><span></span><span></span>
                </div>
            {/each}
        </div>
    {:else if view.kind === "refused"}
        <div class="alert improve-refused {view.fix === 'retry' ? 'danger' : 'warning'}" role="alert" in:rise>
            <i class="ri-error-warning-line" aria-hidden="true"></i>
            <div>
                <p class="tw:font-semibold">{view.title}</p>
                <p>{sentence(view.message)}</p>
            </div>
        </div>
    {:else}
        {@const p = view.proposal}
        <p class="tw:text-sm tw:text-muted-foreground tw:mb-3" in:rise>
            {#if rows.length}
                Choose the changes to keep. Nothing is saved until you press Save.
            {:else}
                Nothing to improve: the model would keep every field as it is.
            {/if}
            {#if dirty}
                The proposal is from the last saved version, and applying a field replaces what you typed in it.
            {/if}
        </p>

        {#if p.verify?.length}
            <div class="alert warning improve-verify" in:rise>
                <i class="ri-alert-line" aria-hidden="true"></i>
                <div>
                    <p class="tw:font-semibold">Check these before you save</p>
                    <ul class="improve-list">
                        {#each p.verify as line, i (i)}<li>{line}</li>{/each}
                    </ul>
                </div>
            </div>
        {/if}

        {#if p.pictures_note}
            <div class="alert info improve-note" in:rise>
                <i class="ri-image-line" aria-hidden="true"></i>
                <p>{p.pictures_note}</p>
            </div>
        {/if}

        {#if p.suggested_category}
            <p class="field-help m-b-sm" in:rise>
                None of your categories fits. You could create “{p.suggested_category}” and file the product there.
            </p>
        {/if}

        <ul class="improve-rows">
            {#each rows as row, i (row.key)}
                <li class="improve-row" class:off={!chosen.has(row.key)} in:rise={{ delay: Math.min(i, 6) * 40 }}>
                    <div class="field improve-pick">
                        <input
                            type="checkbox"
                            id="improve-{row.key}"
                            checked={chosen.has(row.key)}
                            onchange={() => toggle(row.key)}
                        />
                        <label for="improve-{row.key}">{row.label}</label>
                        {#if row.image}<img class="improve-thumb" src={row.image} alt="" />{/if}
                    </div>
                    <div class="improve-compare">
                        <div class="improve-side">
                            <span class="improve-side-label">Now</span>
                            {@render value(row.now)}
                        </div>
                        <div class="improve-side proposed">
                            <span class="improve-side-label">Proposed</span>
                            {@render value(row.proposed)}
                        </div>
                    </div>
                    {#if row.reason}<p class="improve-reason">{row.reason}</p>{/if}
                </li>
            {/each}
        </ul>

        {#if p.model}
            <p class="tw:text-xs tw:text-muted-foreground tw:mt-3">
                {p.pictures ? `Saw ${p.pictures} ${p.pictures === 1 ? "picture" : "pictures"}. ` : ""}Answered
                by {p.model} for {formatMicro(p.cost_micro_minor, p.currency)}.
            </p>
        {/if}
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent secondary" onclick={close}>
            <span class="txt">{view.kind === "ready" && rows.length ? "Cancel" : "Close"}</span>
        </button>
        {#if view.kind === "refused" && view.fix === "settings"}
            <a class="btn" href="{base}/dash/plugins?open=ai-gateway">
                <i class="ri-settings-3-line" aria-hidden="true"></i>
                <span class="txt">AI gateway settings</span>
            </a>
        {:else if view.kind === "refused" && view.fix === "credit"}
            {#if can("ai.read")}
                <a class="btn" href="{base}/dash/ai">
                    <i class="ri-sparkling-2-line" aria-hidden="true"></i>
                    <span class="txt">AI credit</span>
                </a>
            {/if}
        {:else if view.kind === "refused"}
            <button type="button" class="btn" onclick={ask}>
                <i class="ri-refresh-line" aria-hidden="true"></i>
                <span class="txt">Try again</span>
            </button>
        {:else if view.kind === "ready" && rows.length}
            <button type="button" class="btn" disabled={!picked} onclick={apply}>
                <span class="txt">Apply selected ({picked})</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<style>
    .improve-skeleton {
        display: grid;
        gap: 8px;
        padding: 12px 0;
        border-top: 1px solid var(--surfaceAlt2Color);
        animation: improvePulse 1.2s ease-in-out infinite;
    }
    .improve-skeleton span {
        display: block;
        height: 12px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt2Color);
    }
    .improve-skeleton span:first-child {
        width: 30%;
    }
    .improve-skeleton span:last-child {
        width: 70%;
    }
    @keyframes improvePulse {
        50% {
            opacity: 0.45;
        }
    }

    .improve-refused,
    .improve-verify,
    .improve-note {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        margin-bottom: 12px;
    }
    .improve-refused i,
    .improve-verify i,
    .improve-note i {
        font-size: 1.1rem;
        line-height: var(--smLineHeight);
    }

    .improve-list {
        margin: 4px 0 0;
        padding-left: 18px;
        list-style: disc;
    }
    .improve-list li + li {
        margin-top: 2px;
    }

    .improve-rows {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .improve-row {
        padding: 12px 0;
        border-top: 1px solid var(--surfaceAlt2Color);
        transition: opacity var(--activeAnimationSpeed) ease-out;
    }
    .improve-row.off .improve-compare,
    .improve-row.off .improve-reason {
        opacity: 0.55;
    }
    .improve-pick {
        justify-content: space-between;
    }
    .improve-pick label {
        flex: 1;
        min-height: 44px;
        padding-top: 12px;
        padding-bottom: 12px;
        font-weight: 600;
    }
    .improve-pick label::before,
    .improve-pick label::after {
        top: 12px;
    }
    .improve-thumb {
        width: 44px;
        height: 44px;
        object-fit: cover;
        border-radius: var(--borderRadius);
        border: 1px solid var(--surfaceAlt2Color);
    }
    .improve-compare {
        display: grid;
        grid-template-columns: 1fr;
        gap: 8px;
        transition: opacity var(--activeAnimationSpeed) ease-out;
    }
    @media (min-width: 600px) {
        .improve-compare {
            grid-template-columns: 1fr 1fr;
        }
    }
    .improve-side {
        min-width: 0;
        padding: 8px 10px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
        overflow-wrap: anywhere;
    }
    .improve-side.proposed {
        background: var(--surfaceSuccessColor);
    }
    .improve-side-label {
        display: block;
        font-size: var(--smFontSize);
        color: var(--surfaceTxtHintColor);
        margin-bottom: 2px;
    }
    .improve-text {
        white-space: pre-wrap;
    }
    .improve-reason {
        margin: 6px 0 0;
        font-size: var(--smFontSize);
        color: var(--surfaceTxtHintColor);
    }

    @media (prefers-reduced-motion: no-preference) {
        .improve-refused {
            animation: improveShake 260ms ease-out;
        }
        @keyframes improveShake {
            25% {
                transform: translateX(-4px);
            }
            75% {
                transform: translateX(4px);
            }
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .improve-skeleton {
            animation-duration: 2.4s;
        }
    }
</style>
