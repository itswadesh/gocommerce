<script>
    /**
     * The email asking a store's owner to verify their address (D221).
     *
     * The words are fixed and the install's owner may change any of them before
     * sending (D225). The live link is the engine's to add as it sends, so all
     * this screen ever holds is the mark showing where it will go.
     *
     * A native <dialog>: focus stays inside, Escape closes it and the page behind
     * cannot be reached, none of which has to be written here.
     */
    import { untrack } from "svelte";
    import { stores } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";

    /** target is { superuser_id, store_id, email, store }, or null while closed. */
    let { target = null, onclose, onsent } = $props();

    const MARK = "{{verify_link}}";

    let dialog = $state(null);
    let loading = $state(false);
    let sending = $state(false);
    let problem = $state("");
    let from = $state("");
    let subject = $state("");
    let body = $state("");

    const ids = $derived(target ? { superuser_id: target.superuser_id, store_id: target.store_id } : null);
    const canSend = $derived(!loading && !sending && subject.trim() !== "" && body.trim() !== "");

    $effect(() => {
        if (!target || !dialog) return;
        untrack(() => {
            subject = "";
            body = "";
            problem = "";
            dialog.showModal();
            loadDraft();
        });
    });

    async function loadDraft() {
        loading = true;
        problem = "";
        try {
            const draft = await stores.ownerEmailDraft(ids);
            from = draft.from;
            subject = draft.subject;
            body = draft.body;
        } catch (err) {
            problem = err?.message || "The email could not be opened.";
        } finally {
            loading = false;
        }
    }

    async function send(event) {
        event.preventDefault();
        if (!canSend) return;
        sending = true;
        problem = "";
        try {
            const result = await stores.ownerEmailSend({ ...ids, subject, body });
            toast.success(`Sent to ${result.sent_to}`);
            onsent?.(result.email_sent);
            dialog.close();
        } catch (err) {
            // The provider's reason belongs beside the button that was pressed.
            problem = err?.message || "The email was not sent.";
        } finally {
            sending = false;
        }
    }
</script>

<dialog bind:this={dialog} class="owner-email shopify-skin tw:bg-background tw:text-foreground" aria-labelledby="owner-email-title" onclose={() => onclose?.()}>
    <form onsubmit={send}>
        <h2 id="owner-email-title" class="tw:text-lg tw:font-semibold">Verify {target?.email ?? ""}</h2>
        <dl class="addresses">
            <dt>From</dt>
            <dd>{from || "…"}</dd>
            <dt>To</dt>
            <dd>{target?.email ?? ""}</dd>
            <dt>Store</dt>
            <dd>{target?.store ?? ""}</dd>
        </dl>

        <div class="field">
            <label for="owner-email-subject">Subject</label>
            <input id="owner-email-subject" type="text" maxlength="200" bind:value={subject} disabled={loading || sending} />
        </div>
        <div class="field">
            <label for="owner-email-body">Message</label>
            <textarea id="owner-email-body" rows="10" maxlength="5000" bind:value={body} disabled={loading || sending}></textarea>
        </div>
        <p class="txt-hint hint">The verification link goes where {MARK} is. It works once, for 7 days.</p>

        {#if problem}
            <p class="problem" role="alert"><i class="ri-error-warning-line" aria-hidden="true"></i> {problem}</p>
        {/if}

        <div class="actions">
            <span class="gap"></span>
            <button type="button" class="btn transparent secondary" disabled={sending} onclick={() => dialog.close()}>
                <span class="txt">Cancel</span>
            </button>
            <button type="submit" class="btn" disabled={!canSend}>
                <i class="ri-send-plane-line" aria-hidden="true"></i>
                <span class="txt">{sending ? "Sending…" : "Send"}</span>
            </button>
        </div>
    </form>
</dialog>

<style>
    .owner-email {
        width: min(640px, calc(100vw - 32px));
        max-height: calc(100dvh - 32px);
        margin: auto;
        padding: 20px;
        overflow: auto;
        border: 1px solid var(--border, currentColor);
        border-radius: 12px;
        opacity: 0;
        transform: translateY(6px);
        transition:
            opacity 180ms ease,
            transform 180ms ease,
            overlay 180ms allow-discrete,
            display 180ms allow-discrete;
    }
    .owner-email[open] {
        opacity: 1;
        transform: none;
    }
    @starting-style {
        .owner-email[open] {
            opacity: 0;
            transform: translateY(6px);
        }
    }
    .owner-email::backdrop {
        background: rgb(0 0 0 / 0.45);
    }
    form {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }
    .addresses {
        display: grid;
        grid-template-columns: auto 1fr;
        gap: 2px 12px;
        margin: 0;
    }
    .addresses dt {
        opacity: 0.7;
    }
    .addresses dd {
        margin: 0;
        overflow-wrap: anywhere;
    }
    textarea {
        width: 100%;
        resize: vertical;
    }
    .hint,
    .problem {
        margin: 0;
    }
    .problem {
        color: var(--dangerColor);
    }
    .actions {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
    }
    .gap {
        flex: 1;
    }
    @media (prefers-reduced-motion: reduce) {
        .owner-email {
            transition: none;
            transform: none;
        }
    }
</style>
