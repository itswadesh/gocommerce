<script>
    /**
     * A store has asked this person to join by their phone number, and there is
     * no link for them to open, so the panel asks as soon as they are signed in
     * (D205). Pressing Escape leaves the question for the next sign-in; only
     * Decline tells the store no.
     */
    import { onMount } from "svelte";
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { stores as storesAPI } from "$lib/api.js";
    import { dismissable } from "$lib/dismiss.js";
    import { trapFocus } from "$lib/focus.js";
    import { portal } from "$lib/portal.js";
    import { reloadStoreData } from "$lib/store-data.js";
    import { enterStore, loadStores, myStores } from "$lib/stores.svelte.js";
    import { toast } from "$lib/toast.svelte.js";

    let waiting = $state([]);
    let working = $state("");
    let error = $state("");
    const invite = $derived(waiting[0]);
    const open = $derived(!!invite);

    onMount(async () => {
        try {
            waiting = (await storesAPI.invitations()) ?? [];
        } catch {
            // The question comes back at the next sign-in; nothing here is
            // worth stopping somebody from reaching their store for.
        }
    });

    async function answer(accept) {
        if (working) return;
        const asked = invite;
        working = accept ? "accept" : "decline";
        error = "";
        try {
            await storesAPI.answerInvitation(asked.store_id, asked.id, accept);
            waiting = waiting.slice(1);
            if (!accept) return;
            await loadStores();
            if (myStores.current) {
                toast.success(`You joined ${asked.store_name}. Switch to it from the store menu.`);
            } else {
                await enterStore(asked.store_id);
                await reloadStoreData();
                await goto(base + "/dash", { replaceState: true });
            }
        } catch (err) {
            error = err?.message || "That did not go through. Try again.";
        } finally {
            working = "";
        }
    }
</script>

<div
    class="modal popup sm"
    data-modal-state={open ? "open" : "closed"}
    inert={!open}
    role="alertdialog"
    aria-modal="true"
    aria-label="Join {invite?.store_name ?? 'a store'}"
    tabindex="-1"
    use:portal
    use:trapFocus={open}
    use:dismissable={{ onclose: () => (waiting = waiting.slice(1)), enabled: open && !working }}
>
    {#if invite}
        <div class="modal-content">
            <h5 class="m-b-sm">Join {invite.store_name}?</h5>
            <div class="txt-hint">
                {invite.invited_by || "The store"} added you to the team as {invite.role}. Accept, and
                this store is added to your account.
            </div>
            {#if error}<div class="field-help error m-t-sm" role="alert">{error}</div>{/if}
        </div>
        <footer class="modal-footer">
            <button
                type="button"
                class="btn transparent m-r-auto"
                class:loading={working === "decline"}
                disabled={!!working}
                onclick={() => answer(false)}
            >
                <span class="txt">Decline</span>
            </button>
            <button
                type="button"
                class="btn"
                class:loading={working === "accept"}
                disabled={!!working}
                onclick={() => answer(true)}
            >
                <span class="txt">Accept</span>
            </button>
        </footer>
    {/if}
</div>
