<script>
    /**
     * Ask AI (D233): a conversation that lives in this drawer and nowhere else,
     * one blocking question at a time. Everything from the model is drawn as
     * text; a link is only ever one the engine kept, and a change is only ever a
     * card with Apply.
     */
    import { tick } from "svelte";
    import { fade } from "svelte/transition";
    import { base } from "$app/paths";
    import { api } from "$lib/api.js";
    import { NAV, flatNav } from "$lib/nav.js";
    import { rise } from "$lib/motion.js";
    import { capEntries, historyFor, MAX_QUESTION, needsSupport, refreshedProposal, safeLinks, screensFor, valueText } from "$lib/askai.js";
    import Drawer from "$lib/components/Drawer.svelte";

    let { open = false, onclose, onapplied } = $props();

    const SUGGESTIONS = ["What should I do next?", "How do I take payments?", "Change my store's timezone"];

    let entries = $state([]);
    let question = $state("");
    let working = $state(false);
    let refusal = $state(null);
    let log = $state();

    async function toBottom() {
        await tick();
        // The drawer's own body is what scrolls.
        const body = log?.closest(".modal-content");
        body?.scrollTo?.({ top: body.scrollHeight });
    }

    async function ask(text) {
        text = text.trim();
        if (!text || working) return;
        refusal = null;
        question = "";
        working = true;
        entries = capEntries([...entries, { role: "user", text }]);
        toBottom();
        try {
            const res = await api.post(
                "/api/admin/x/ask-ai/turn",
                { messages: historyFor(entries), screens: screensFor(flatNav(NAV)) });
            entries = capEntries([
                ...entries,
                {
                    role: "assistant",
                    text: res.answer,
                    links: safeLinks(res.links),
                    notice: res.notice ?? "",
                    proposal: res.proposal ?? null,
                    state: res.proposal ? "open" : "",
                    error: "",
                },
            ]);
        } catch (err) {
            // The question goes back in the box, so trying again is one press and
            // a question nobody answered is not sent as history.
            entries = entries.slice(0, -1);
            question = text;
            refusal = { message: err?.message || "Ask AI couldn't answer. Try again.", support: needsSupport(err) };
        } finally {
            working = false;
            toBottom();
        }
    }

    function onkeydown(e) {
        if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            ask(question);
        }
    }

    async function apply(entry) {
        entry.state = "applying";
        entry.error = "";
        try {
            const done = await api.post(
                `/api/admin/x/ask-ai/proposals/${entry.proposal.id}/apply`,
                { preview_hash: entry.proposal.preview_hash });
            // What the engine says it saved, not what was proposed.
            entry.proposal.changes = entry.proposal.changes.map((c) => ({
                ...c,
                proposed: done.changes.find((d) => d.field === c.field)?.proposed ?? c.proposed,
            }));
            entry.state = "applied";
            onapplied?.();
        } catch (err) {
            const fresh = refreshedProposal(entry.proposal, err);
            if (fresh) {
                entry.proposal = fresh;
                entry.state = "open";
            } else {
                entry.state = err?.code === "ask_ai_not_applied" ? "failed" : "open";
            }
            entry.error = err?.message || "That change was not applied.";
        }
    }

    async function dismiss(entry) {
        entry.state = "dismissing";
        entry.error = "";
        try {
            await api.post(`/api/admin/x/ask-ai/proposals/${entry.proposal.id}/dismiss`, undefined);
            entry.state = "dismissed";
        } catch (err) {
            entry.state = "open";
            entry.error = err?.message || "That could not be dismissed.";
        }
    }
</script>

{#snippet cell(label, field, value)}
    <div class="ask-side">
        <span class="ask-side-label">{label}</span>
        {#if valueText(field, value)}
            <span class="ask-value">{valueText(field, value)}</span>
        {:else}
            <span class="tw:text-muted-foreground">Empty</span>
        {/if}
    </div>
{/snippet}

<Drawer {open} title="Ask AI" size="md" {onclose}>
    <div class="ask-log" bind:this={log} aria-live="polite">
        {#if !entries.length && !working}
            <div in:fade={{ duration: 150 }}>
                <p class="tw:text-sm tw:text-muted-foreground tw:mb-3">
                    Ask how to set up your store, or ask to change its name, contact details, address, country, timezone,
                    currency, tax ID or support link. Nothing changes until you press Apply.
                </p>
                <div class="tw:flex tw:flex-wrap tw:gap-2">
                    {#each SUGGESTIONS as s (s)}
                        <button type="button" class="ask-suggestion" onclick={() => ask(s)}>{s}</button>
                    {/each}
                </div>
            </div>
        {/if}

        {#each entries as entry, i (i)}
            {#if entry.role === "user"}
                <p class="ask-mine" in:rise>{entry.text}</p>
            {:else}
                <div class="ask-theirs" in:rise>
                    <p class="ask-text">{entry.text}</p>

                    {#if entry.links?.length}
                        <div class="tw:flex tw:flex-wrap tw:gap-2 tw:mt-2">
                            {#each entry.links as link (link.href)}
                                <a class="ask-link" href="{base}{link.href}" onclick={() => onclose?.()}>
                                    <span>{link.label}</span>
                                    <i class="ri-arrow-right-line" aria-hidden="true"></i>
                                </a>
                            {/each}
                        </div>
                    {/if}

                    {#if entry.notice}
                        <p class="ask-notice">{entry.notice}</p>
                    {/if}

                    {#if entry.proposal}
                        <div class="ask-card" class:done={entry.state === "applied"} class:gone={entry.state === "dismissed"}>
                            <ul class="ask-changes">
                                {#each entry.proposal.changes as c (c.field)}
                                    <li>
                                        <span class="ask-field">{c.label}</span>
                                        <div class="ask-compare">
                                            {@render cell(entry.state === "applied" ? "Was" : "Now", c.field, c.current)}
                                            {@render cell(entry.state === "applied" ? "Saved" : "New", c.field, c.proposed)}
                                        </div>
                                    </li>
                                {/each}
                            </ul>

                            {#each entry.proposal.warnings ?? [] as w (w)}
                                {#if entry.state !== "dismissed"}
                                    <p class="ask-warning">
                                        <i class="ri-alert-line" aria-hidden="true"></i>
                                        <span>{w}</span>
                                    </p>
                                {/if}
                            {/each}

                            {#if entry.error}
                                <p class="ask-error" role="alert" in:rise>
                                    <i class="ri-error-warning-line" aria-hidden="true"></i>
                                    <span>{entry.error}</span>
                                </p>
                            {/if}

                            {#if entry.state === "applied"}
                                <p class="ask-result ok" role="status" in:rise>
                                    <i class="ri-check-line ask-tick" aria-hidden="true"></i>
                                    <span>Applied</span>
                                </p>
                            {:else if entry.state === "dismissed"}
                                <p class="ask-result" role="status" in:rise>Dismissed. Nothing changed.</p>
                            {:else if entry.state !== "failed"}
                                <div class="tw:flex tw:gap-2 tw:mt-3">
                                    <button
                                        type="button"
                                        class="btn ask-action"
                                        class:btn-loading={entry.state === "applying"}
                                        disabled={entry.state === "applying" || entry.state === "dismissing"}
                                        onclick={() => apply(entry)}
                                    >
                                        <span class="txt">Apply</span>
                                    </button>
                                    <button
                                        type="button"
                                        class="btn transparent secondary ask-action"
                                        class:btn-loading={entry.state === "dismissing"}
                                        disabled={entry.state === "applying" || entry.state === "dismissing"}
                                        onclick={() => dismiss(entry)}
                                    >
                                        <span class="txt">Dismiss</span>
                                    </button>
                                </div>
                            {/if}
                        </div>
                    {/if}
                </div>
            {/if}
        {/each}

        {#if working}
            <p class="ask-working" role="status" in:fade={{ duration: 150 }}>
                <span class="ask-dots" aria-hidden="true"><span></span><span></span><span></span></span>
                <span>Working on it…</span>
            </p>
        {/if}

        {#if refusal}
            <div class="ask-error" role="alert" in:rise>
                <i class="ri-error-warning-line" aria-hidden="true"></i>
                <span>
                    {refusal.message}
                    {#if refusal.support}
                        <a href="{base}/dash/support" class="ask-inline" onclick={() => onclose?.()}>Contact support</a>
                    {/if}
                </span>
            </div>
        {/if}
    </div>

    {#snippet footer()}
        <form
            class="ask-form"
            onsubmit={(e) => {
                e.preventDefault();
                ask(question);
            }}
        >
            <label class="ask-sr" for="ask-question">Your question</label>
            <!-- svelte-ignore a11y_autofocus -->
            <textarea
                id="ask-question"
                autofocus
                class="ask-box"
                rows="2"
                maxlength={MAX_QUESTION}
                placeholder="Ask about setting up your store"
                bind:value={question}
                {onkeydown}
            ></textarea>
            <button
                type="submit"
                class="btn circle ask-send"
                class:btn-loading={working}
                aria-label="Send"
                disabled={working || !question.trim()}
            >
                <i class="ri-send-plane-2-line" aria-hidden="true"></i>
            </button>
        </form>
    {/snippet}
</Drawer>

<style>
    .ask-log {
        display: flex;
        flex-direction: column;
        gap: 12px;
        min-height: 100%;
        overflow-wrap: anywhere;
    }
    .ask-mine {
        align-self: flex-end;
        max-width: 85%;
        padding: 8px 12px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt2Color);
        white-space: pre-wrap;
    }
    .ask-theirs {
        max-width: 100%;
    }
    .ask-text {
        white-space: pre-wrap;
    }
    .ask-suggestion,
    .ask-link {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        min-height: 44px;
        padding: 6px 12px;
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
        background: transparent;
        color: var(--surfaceTxtColor);
        font-size: var(--smFontSize);
        text-align: left;
        text-decoration: none;
        cursor: pointer;
        transition:
            background-color 180ms,
            border-color 180ms,
            transform 150ms;
    }
    .ask-suggestion:hover,
    .ask-suggestion:focus-visible,
    .ask-link:hover,
    .ask-link:focus-visible {
        background: var(--surfaceAlt1Color);
        border-color: var(--surfaceTxtHintColor);
    }
    .ask-suggestion:active,
    .ask-link:active {
        transform: scale(0.98);
    }
    .ask-notice,
    .ask-result {
        margin-top: 8px;
        color: var(--surfaceTxtHintColor);
        font-size: var(--smFontSize);
    }
    .ask-card {
        margin-top: 10px;
        padding: 12px;
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
        transition:
            border-color 200ms,
            opacity 200ms;
    }
    .ask-card.done {
        border-color: var(--successColor);
    }
    .ask-card.gone {
        opacity: 0.6;
    }
    .ask-changes {
        display: flex;
        flex-direction: column;
        gap: 10px;
        margin: 0;
        padding: 0;
        list-style: none;
    }
    .ask-field {
        display: block;
        margin-bottom: 4px;
        font-weight: 600;
        font-size: var(--smFontSize);
    }
    .ask-compare {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 8px;
    }
    .ask-side {
        min-width: 0;
        padding: 6px 8px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
        font-size: var(--smFontSize);
    }
    .ask-side-label {
        display: block;
        color: var(--surfaceTxtHintColor);
        font-size: 12px;
    }
    .ask-warning,
    .ask-error {
        display: flex;
        gap: 6px;
        margin-top: 8px;
        font-size: var(--smFontSize);
    }
    .ask-warning {
        color: var(--warningColor);
    }
    .ask-error {
        color: var(--dangerColor);
    }
    .ask-inline {
        text-decoration: underline;
    }
    .ask-result.ok {
        display: flex;
        align-items: center;
        gap: 6px;
        color: var(--successColor);
        font-weight: 600;
    }
    .ask-tick {
        animation: ask-pop 220ms ease-out;
    }
    .ask-action {
        min-height: 44px;
    }
    .ask-working {
        display: flex;
        align-items: center;
        gap: 8px;
        color: var(--surfaceTxtHintColor);
        font-size: var(--smFontSize);
    }
    .ask-dots {
        display: inline-flex;
        gap: 3px;
    }
    .ask-dots > span {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: currentColor;
        animation: ask-beat 900ms ease-in-out infinite;
    }
    .ask-dots > span:nth-child(2) {
        animation-delay: 150ms;
    }
    .ask-dots > span:nth-child(3) {
        animation-delay: 300ms;
    }
    .ask-form {
        display: flex;
        flex-grow: 1;
        align-items: flex-end;
        gap: 8px;
        width: 100%;
    }
    .ask-box {
        flex-grow: 1;
        min-width: 0;
        min-height: 56px;
        padding: 8px 10px;
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
        background: var(--surfaceColor);
        color: var(--surfaceTxtColor);
        font-family: inherit;
        /* 16px: a phone zooms the whole page on a smaller field. */
        font-size: 16px;
        resize: none;
        transition: border-color 180ms;
    }
    .ask-box:focus {
        border-color: var(--surfaceTxtColor);
    }
    .ask-send {
        flex-shrink: 0;
        width: 44px;
        height: 44px;
    }
    .ask-sr {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip-path: inset(50%);
    }
    @keyframes ask-pop {
        from {
            opacity: 0;
            transform: scale(0.6);
        }
    }
    @keyframes ask-beat {
        50% {
            opacity: 0.35;
            transform: translateY(-2px);
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .ask-suggestion,
        .ask-link {
            transition: background-color 180ms, border-color 180ms;
        }
        .ask-suggestion:active,
        .ask-link:active {
            transform: none;
        }
        .ask-tick {
            animation: none;
        }
        /* The feedback stays, by fading in place instead of bouncing. */
        @keyframes ask-beat-still {
            50% {
                opacity: 0.35;
            }
        }
        .ask-dots > span {
            animation-name: ask-beat-still;
        }
    }
</style>
