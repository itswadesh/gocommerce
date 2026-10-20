<script>
    /**
     * A logged message, shown the way its reader got it: who it went to, the
     * subject and the plain text the store's notifiers send.
     *
     * The log keeps a message's data, not its words, so this is the wording as
     * it reads now filled with the details that message carried — and it says
     * so when the wording was edited after the message went out, rather than
     * passing today's words off as what was sent.
     */
    import { api } from "$lib/api.js";
    import { formatDate } from "$lib/format.js";
    import { languageName } from "$lib/languages.js";
    import { dismissable } from "$lib/dismiss.js";
    import { portal } from "$lib/portal.js";
    import { trapFocus } from "$lib/focus.js";

    /** @type {{ message: object | null, onclose: () => void }} */
    let { message = null, onclose } = $props();

    let preview = $state(null);
    let loading = $state(false);
    let failure = $state("");
    let latest = 0;

    $effect(() => {
        if (message) load(message.id);
    });

    async function load(id) {
        const mine = ++latest;
        loading = true;
        failure = "";
        preview = null;
        try {
            const got = await api.get(`/api/admin/notifications/${id}/preview`);
            if (mine === latest) preview = got;
        } catch (err) {
            if (mine === latest) failure = err?.message || "This message cannot be shown.";
        } finally {
            if (mine === latest) loading = false;
        }
    }

    const short = (tag) => languageName(tag).split(" · ")[0];
    const open = $derived(!!message);
</script>

<div
    class="modal popup lg message-preview"
    data-modal-state={open ? "open" : "closed"}
    inert={!open}
    role="dialog"
    aria-modal="true"
    aria-labelledby="message-preview-title"
    tabindex="-1"
    use:portal
    use:trapFocus={open}
    use:dismissable={{ onclose, enabled: open }}
>
    <header class="modal-header">
        <h5 class="modal-title" id="message-preview-title">
            {(message?.channel === "sms" ? "Text message" : "Email") + (message?.title ? ` · ${message.title}` : "")}
        </h5>
        <button type="button" class="btn sm circle transparent secondary modal-close-btn m-l-auto" aria-label="Close" onclick={onclose}>
            <i class="ri-close-line" aria-hidden="true"></i>
        </button>
    </header>
    <div class="modal-content">
        {#if message}
            <dl class="mp-meta">
                <div><dt>To</dt><dd>{message.to}</dd></div>
                <div><dt>Sent</dt><dd>{formatDate(message.created_at)}</dd></div>
                {#if preview?.language}<div><dt>Language</dt><dd>{short(preview.language)}</dd></div>{/if}
                {#if message.backend}<div><dt>Through</dt><dd>{message.backend}</dd></div>{/if}
            </dl>
        {/if}

        {#if loading}
            <div class="block txt-center p-base"><span class="loader"></span></div>
        {:else if failure}
            <div class="alert danger" role="alert"><p>{failure}</p></div>
        {:else if preview}
            {#if preview.edited_since}
                <div class="alert info m-b-sm" role="status">
                    <p>
                        The wording was edited after this went out, so this is how it reads now, filled with the
                        details this message carried. What was sent may have read differently.
                    </p>
                </div>
            {/if}
            {#if message?.backend === "msg91"}
                <div class="alert info m-b-sm" role="status">
                    <p>MSG91 sends its own DLT-approved template, so the words that went out are MSG91's; this is the store's wording.</p>
                </div>
            {/if}
            <article class="mp-paper" lang={preview.language || undefined}>
                {#if preview.subject}<h6 class="mp-subject">{preview.subject}</h6>{/if}
                <div class="mp-body">{preview.body}</div>
            </article>
            {#if preview.source === "main"}
                <div class="field-help m-t-sm">
                    Not worded in {short(message?.language || preview.language)} yet, so it went in the main language's wording.
                </div>
            {:else if preview.source === "default"}
                <div class="field-help m-t-sm">The engine's own wording: nobody has reworded this message under Setup Email or SMS.</div>
            {/if}
        {/if}
    </div>
    <footer class="modal-footer">
        <button type="button" class="btn transparent m-l-auto" onclick={onclose}><span class="txt">Close</span></button>
    </footer>
</div>

<style>
    .mp-meta {
        display: flex;
        flex-wrap: wrap;
        gap: 6px 20px;
        margin: 0 0 12px;
    }
    .mp-meta dt {
        font-size: 12px;
        color: var(--txtHintColor);
    }
    .mp-meta dd {
        margin: 0;
        overflow-wrap: anywhere;
    }
    /* The message on a sheet of its own, the way a reader's mail app shows
       plain text: the store's words, not the panel's chrome. */
    .mp-paper {
        padding: 16px;
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
        background: var(--surfaceColor);
        max-height: 55vh;
        overflow: auto;
    }
    .mp-subject {
        margin: 0 0 12px;
        padding-bottom: 10px;
        border-bottom: 1px solid var(--surfaceAlt3Color);
        font-weight: 600;
        overflow-wrap: anywhere;
    }
    .mp-body {
        white-space: pre-wrap;
        overflow-wrap: anywhere;
        line-height: 1.55;
    }
</style>
