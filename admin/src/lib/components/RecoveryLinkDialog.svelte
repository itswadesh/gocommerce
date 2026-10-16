<script>
    /**
     * A recovery link the browser would not put on the clipboard, shown so it
     * can be copied by hand rather than claimed as copied when it was not.
     *
     * The link opens the cart, so it works for as long as the cart does; the
     * module sends that date with the link, and this says it.
     */
    import { copyText } from "$lib/clipboard.js";
    import { formatDate } from "$lib/format.js";
    import Drawer from "$lib/components/Drawer.svelte";

    /** @type {{ link: { url: string, tracked?: boolean, expires_at?: string } | null, onclose: () => void }} */
    let { link = null, onclose } = $props();

    let copied = $state(false);
    let timer;

    $effect(() => {
        if (link) copied = false;
    });

    async function copy() {
        if (!link) return;
        if (await copyText(link.url)) {
            copied = true;
            clearTimeout(timer);
            timer = setTimeout(() => (copied = false), 2000);
        }
    }

    function select(event) {
        event.currentTarget.select();
    }
</script>

<Drawer open={!!link} size="popup sm" title="Recovery link" onclose={() => onclose?.()}>
    {#if link}
        <p class="txt-hint m-b-sm">
            It takes the customer back to their cart.{link.expires_at ? ` Works until ${formatDate(link.expires_at)}.` : ""}
            {#if link.tracked === false}
                Clicks on it are not counted: the store has no public address set.
            {/if}
        </p>
        <div class="recovery-link-row">
            <input
                class="txt-code"
                type="text"
                readonly
                value={link.url}
                aria-label="Recovery link"
                onfocus={select}
                onclick={select}
            />
            <button type="button" class="btn sm secondary recovery-copy" class:is-copied={copied} onclick={copy}>
                <i class={copied ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                <span class="txt">{copied ? "Copied" : "Copy"}</span>
            </button>
        </div>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-l-auto" onclick={() => onclose?.()}>
            <span class="txt">Close</span>
        </button>
    {/snippet}
</Drawer>
