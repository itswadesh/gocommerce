<script>
    /**
     * A failed delivery (an NDR) on a Shiprocket parcel: why the courier says
     * it failed, how many attempts, and the two answers Shiprocket documents —
     * try again, or send it back. Shiprocket's third answer, disputing the
     * attempt as fake, needs recorded proof the panel cannot collect, so it is
     * left to Shiprocket's own dashboard.
     */
    import { request } from "$lib/api.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";

    let { row, onanswered } = $props();

    let ndr = $state(null);
    let loading = $state(true);
    let error = $state("");
    let comments = $state("");
    let deferred = $state("");
    let sending = $state("");

    async function load(id) {
        loading = true;
        error = "";
        try {
            ndr = await request("GET", `/api/admin/x/fulfill-shiprocket/shipments/${id}/ndr`);
        } catch (err) {
            error = err?.message || "Shiprocket could not be asked.";
        } finally {
            loading = false;
        }
    }

    const rowId = $derived(row.id);
    $effect(() => {
        load(rowId);
    });

    async function answer(action) {
        sending = action;
        try {
            const updated = await request("POST", `/api/admin/x/fulfill-shiprocket/shipments/${row.id}/ndr`, {
                body: {
                    action,
                    comments: comments.trim(),
                    ...(action === "re-attempt" && deferred ? { deferred_date: deferred } : {}),
                },
            });
            toast.success(action === "re-attempt" ? "Shiprocket will try the delivery again" : "Shiprocket will send the parcel back");
            comments = "";
            onanswered?.(updated);
        } catch (err) {
            toast.error(err);
        } finally {
            sending = "";
        }
    }
</script>

<div class="sr-ndr" in:rise aria-busy={loading}>
    <div class="sr-ndr-title">
        <i class="ri-error-warning-line" aria-hidden="true"></i>
        Delivery failed
    </div>
    {#if loading}
        <div class="txt-hint txt-sm"><span class="sr-spinner" aria-hidden="true"></span> Asking Shiprocket why…</div>
    {:else if error}
        <div class="txt-hint txt-sm">{error}</div>
    {:else if ndr}
        <div class="txt-sm">
            {ndr.reason || "No reason given"} · {ndr.attempts} attempt{ndr.attempts === 1 ? "" : "s"}{ndr.raised_at ? `, last ${ndr.raised_at}` : ""}
        </div>
    {/if}
    <label class="sr-ndr-field">
        <span class="txt-hint txt-sm">Note for the courier (Shiprocket needs one)</span>
        <textarea rows="2" bind:value={comments} placeholder="Customer is home after 6 pm"></textarea>
    </label>
    <label class="sr-ndr-field sr-ndr-date">
        <span class="txt-hint txt-sm">Try again on (optional)</span>
        <input type="date" bind:value={deferred} />
    </label>
    <div class="sr-actions">
        <button
            type="button"
            class="btn sm"
            class:loading={sending === "re-attempt"}
            disabled={!!sending || !comments.trim()}
            onclick={() => answer("re-attempt")}
        >
            <span class="txt">Try delivery again</span>
        </button>
        <button
            type="button"
            class="btn sm transparent secondary"
            class:loading={sending === "return"}
            disabled={!!sending || !comments.trim()}
            onclick={() => answer("return")}
        >
            <span class="txt">Send it back to me</span>
        </button>
    </div>
</div>
