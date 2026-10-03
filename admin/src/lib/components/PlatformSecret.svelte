<script>
    /**
     * A credential the platform hands back once — a store's admin token, an
     * owner's generated password — shown in full, with a way to copy it.
     *
     * Full width and readonly for AccessTokenCard's reason: it is the one
     * string on the screen that has to be read correctly, and the platform
     * keeps no copy that could be asked for again.
     */
    import { toast } from "$lib/toast.svelte.js";

    let { id, label, value, hint = "" } = $props();
    let copied = $state(false);

    async function copy() {
        try {
            await navigator.clipboard.writeText(value);
            copied = true;
            setTimeout(() => (copied = false), 2000);
        } catch {
            // Refused on an insecure origin or by a browser setting; the value
            // is on screen and selectable.
            toast.info("Copy it from the box");
        }
    }
</script>

<div class="platform-secret">
    <div class="field">
        <label for={id}>{label}</label>
        <input {id} type="text" class="txt-code" readonly {value} onfocus={(e) => e.currentTarget.select()} />
    </div>
    <div class="platform-secret-row">
        <button type="button" class="btn sm secondary platform-copy" class:copied onclick={copy}>
            <i class={copied ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
            <span class="txt">{copied ? "Copied" : "Copy"}</span>
        </button>
        {#if hint}<span class="field-help">{hint}</span>{/if}
    </div>
</div>
