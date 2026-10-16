<script>
    /**
     * Stopping the reminders for one basket, with a reason.
     *
     * The reasons are the module's own list (`recovery.suppress_reasons`),
     * worded by it, so a reason added there appears here without a release of
     * the panel. "Other" needs a note — the module refuses it without one — and
     * the box says so before the request rather than after. The record is never
     * hidden afterwards: it stays in the list as suppressed, and the reason, the
     * note and who chose them are shown on its page.
     */
    import { untrack } from "svelte";
    import { recovery } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";

    let { open = false, record = null, onclose, ondone, onrefresh } = $props();

    const uid = Math.random().toString(36).slice(2, 10);
    const formID = "recovery-suppress-" + uid;
    const NOTE_MAX = 1000;

    const reasons = $derived(record?.recovery?.suppress_reasons ?? []);

    let reason = $state("");
    let note = $state("");
    let busy = $state(false);
    /** Held for a beat after it lands, so the button can say it worked. */
    let done = $state(false);
    let refusal = $state("");
    let tried = $state(false);

    $effect(() => {
        if (!open) return;
        untrack(() => {
            reason = "";
            note = "";
            refusal = "";
            tried = false;
            done = false;
        });
    });

    const needsNote = $derived(reason === "other");
    const noteMissing = $derived(needsNote && !note.trim());
    const blocked = $derived(busy || !reason || noteMissing || note.length > NOTE_MAX);

    async function submit(event) {
        event?.preventDefault();
        tried = true;
        if (blocked || !record) return;
        busy = true;
        refusal = "";
        try {
            const detail = await recovery.suppress(record.id, { reason, note: note.trim() });
            done = true;
            toast.success("Recovery suppressed — nothing more will be sent");
            setTimeout(() => ondone?.(detail), 400);
        } catch (err) {
            if (err.status === 409 || err.status === 400) {
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

<Drawer {open} size="popup sm" title="Suppress recovery" onclose={() => onclose?.()}>
    {#if record}
        <form id={formID} class="recovery-dialog" onsubmit={submit}>
            <p class="txt-hint m-b-sm">
                Stops every recovery message for this abandoned {record.kind === "cart" ? "cart" : "checkout"}. It
                stays in the list, marked as suppressed.
            </p>

            {#if refusal}
                <div class="alert danger recovery-refusal" role="alert" transition:rise>
                    <p>{refusal}</p>
                </div>
            {/if}

            <fieldset class="recovery-choices recovery-radio-cards">
                <legend class="recovery-legend">Reason</legend>
                {#each reasons as r (r.key)}
                    <label class="recovery-radio-card" class:is-on={reason === r.key}>
                        <input type="radio" name="{formID}-reason" value={r.key} bind:group={reason} />
                        <span>{r.label}</span>
                    </label>
                {/each}
            </fieldset>

            <div class="field m-t-sm" class:required={needsNote} class:error={tried && noteMissing}>
                <label for="{formID}-note">
                    Note <span class="txt-hint">{needsNote ? "(required for Other)" : "(optional)"}</span>
                </label>
                <textarea
                    id="{formID}-note"
                    rows="3"
                    maxlength={NOTE_MAX}
                    placeholder="What happened, for whoever looks at this next"
                    bind:value={note}
                ></textarea>
            </div>
            {#if tried && noteMissing}
                <div class="field-help error" transition:rise>Say why in the note when the reason is Other.</div>
            {:else}
                <div class="field-help">
                    {needsNote ? "Required for Other." : "Shown on the record beside the reason."}
                    {note.length}/{NOTE_MAX}
                </div>
            {/if}
        </form>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form={formID}
            class="btn expanded"
            class:loading={busy}
            disabled={busy || done || !reason}
        >
            <i class={done ? "ri-check-line" : "ri-forbid-line"} aria-hidden="true"></i>
            <span class="txt">{done ? "Suppressed" : busy ? "Suppressing…" : "Suppress recovery"}</span>
        </button>
    {/snippet}
</Drawer>
