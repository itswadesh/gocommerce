<script>
    /**
     * Writing to a shopper about one basket, now.
     *
     * Everything the dialog offers comes off the record's own `recovery`
     * block — the channels with whether each can be used, the messages a send
     * may use, the one the sequence would send next — so the panel decides
     * nothing about what is allowed. The module re-checks every rule when the
     * request lands, because the screen this was opened from may be minutes
     * old: the shopper may have bought in between, and a refusal saying so
     * (409, with the module's own sentence) is the expected outcome rather
     * than a fault. That sentence stays in the dialog, where the operator is
     * looking, and the record behind it is re-read so the page agrees.
     *
     * A channel that cannot be used is shown disabled with its reason, not
     * hidden: "why is there no SMS" is a question the list answers.
     */
    import { untrack } from "svelte";
    import { recovery } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";

    let {
        open = false,
        /** The detail record — `GET …/abandonments/{id}` — not a list row. */
        record = null,
        onclose,
        /** Called with the module's answer: `{ sent_to, sent_at, abandonment }`. */
        onsent,
        /** Re-read the record after a refusal. */
        onrefresh,
    } = $props();

    const uid = Math.random().toString(36).slice(2, 10);
    const formID = "recovery-send-" + uid;

    const options = $derived(record?.recovery ?? null);
    const channels = $derived(options?.channels ?? []);
    const templates = $derived(
        (options?.templates ?? []).map((t) => ({
            value: t.event,
            label: t.title,
            note: t.customized ? "Reworded for this store" : "",
        })),
    );

    let channel = $state("email");
    let template = $state("");
    let busy = $state(false);
    /** The module's own sentence for a refusal, kept on screen until closed. */
    let refusal = $state("");

    /* Re-seeded each time it opens, from the record as it is then; untracked,
       so the page's thirty-second re-read does not reset a choice somebody is
       in the middle of making. */
    $effect(() => {
        if (!open) return;
        untrack(() => {
            const firstUsable = channels.find((c) => c.available)?.key ?? "email";
            channel = firstUsable;
            template = options?.default_template || templates[0]?.value || "";
            refusal = "";
        });
    });

    const blocked = $derived(busy || !options?.can_send || !channel || !template);

    async function submit(event) {
        event?.preventDefault();
        if (blocked || !record) return;
        busy = true;
        refusal = "";
        try {
            const result = await recovery.send(record.id, { channel, template });
            toast.success(`Recovery email sent to ${result.sent_to}`);
            onsent?.(result);
        } catch (err) {
            if (err.status === 409 || err.status === 502) {
                // 409: the basket moved on (bought, suppressed, deleted, no
                // provider). 502: the provider said no. Either way the words
                // are the module's and belong next to the button pressed.
                refusal = err.message;
                if (err.status === 409) onrefresh?.();
            } else {
                toast.error(err);
            }
        } finally {
            busy = false;
        }
    }
</script>

<Drawer {open} size="popup sm" title="Send a recovery email" onclose={() => onclose?.()}>
    {#if record}
        <form id={formID} class="recovery-dialog" onsubmit={submit}>
            {#if refusal}
                <div class="alert danger recovery-refusal" role="alert" transition:rise>
                    <p>{refusal}</p>
                </div>
            {:else if options && !options.can_send && options.send_blocked_reason}
                <div class="alert warning" role="status" transition:rise>
                    <p>{options.send_blocked_reason}</p>
                </div>
            {/if}

            <fieldset class="recovery-choices">
                <legend class="recovery-legend">Channel</legend>
                {#each channels as c (c.key)}
                    <div class="field">
                        <input
                            type="radio"
                            id="{formID}-ch-{c.key}"
                            name="{formID}-channel"
                            value={c.key}
                            disabled={!c.available}
                            bind:group={channel}
                        />
                        <label for="{formID}-ch-{c.key}">
                            {c.label}
                            {#if !c.available && c.reason}
                                <span class="recovery-choice-note">{c.reason}</span>
                            {/if}
                        </label>
                    </div>
                {/each}
            </fieldset>

            <div class="field m-t-sm">
                <label for="{formID}-template">Message</label>
                <Select
                    id="{formID}-template"
                    value={template}
                    options={templates}
                    onchange={(v) => (template = v)}
                />
            </div>
            <div class="field-help">
                The wording is the store's, edited under Notifications › Setup Email. Sending by hand
                does not move the automation on: its next reminder still goes out on schedule.
            </div>

            <div class="field m-t-sm readonly-field">
                <span class="readonly-label">Recipient</span>
                <span class="readonly-value">{record.email || "No email address"}</span>
            </div>
        </form>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">{refusal ? "Close" : "Cancel"}</span>
        </button>
        <button
            type="submit"
            form={formID}
            class="btn expanded"
            class:loading={busy}
            disabled={blocked}
            title={!options?.can_send && options?.send_blocked_reason ? options.send_blocked_reason : undefined}
        >
            <i class="ri-mail-send-line" aria-hidden="true"></i>
            <span class="txt">Send recovery</span>
        </button>
    {/snippet}
</Drawer>
