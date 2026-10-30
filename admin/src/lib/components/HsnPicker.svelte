<script>
    /**
     * An HSN/SAC code box that searches India's GST master as you type (D171).
     *
     * The value is whatever is in the box, so a code the master does not list
     * — it trails the tariff — can still be typed and saved; the line under
     * the box says what the code means, or that the master does not have it.
     * Words search too: "cotton t-shirts" offers 61091000.
     *
     * The suggestions are a native <datalist>: each option's value is the code
     * and its label the description, so picking one puts the bare code in the
     * box, the browser already filters and draws the list on every platform,
     * and the keyboard behaves as it does in every other form.
     *
     * Usage: <HsnPicker id="x" bind:value={form.hs_code} />
     */
    import { gst } from "$lib/api.js";
    import { rise } from "$lib/motion.js";

    let { value = $bindable(""), id = undefined, placeholder = "6109.10 or “t-shirts”", disabled = false, ...rest } = $props();

    const listId = "hsn-" + Math.random().toString(36).slice(2, 10);
    let options = $state([]);
    // What the current value means: null while unknown, empty or not yet a
    // whole level (master codes have 2, 4, 6 or 8 digits), false when the
    // master has no such code.
    let found = $state(null);

    const typed = $derived(String(value ?? "").trim());
    const digits = $derived(/^[\d.\s]+$/.test(typed) ? typed.replace(/[.\s]/g, "") : "");

    // One timer and one sequence number per instance: a reply that comes back
    // after a newer keystroke's is dropped rather than overwriting it.
    let seq = 0;
    $effect(() => {
        const q = typed;
        const code = digits;
        const mine = ++seq;
        if (q.length < 2) {
            options = [];
            found = null;
            return;
        }
        const timer = setTimeout(async () => {
            try {
                const list = (await gst.hsn(q))?.data ?? [];
                if (mine !== seq) return;
                options = list;
                // A code search is a prefix search in code order, so the code
                // itself, when the master has it, is the first match.
                found = list[0]?.code === code ? list[0] : code.length % 2 === 0 && code ? false : null;
            } catch {
                // The box still takes a typed code; suggestions are a help.
                if (mine === seq) options = [];
            }
        }, 200);
        return () => clearTimeout(timer);
    });

    const describe = (c) => (c.heading ? `${c.heading} > ${c.description}` : c.description);
</script>

<input
    {id}
    {disabled}
    {placeholder}
    type="text"
    autocomplete="off"
    list={listId}
    title={found ? describe(found) : undefined}
    {...rest}
    bind:value
/>
<datalist id={listId}>
    {#each options as c (c.code)}
        <option value={c.code} label="{c.service ? 'SAC · ' : ''}{describe(c)}"></option>
    {/each}
</datalist>
{#if found}
    {#key found.code}
        <div class="hsn-meaning txt-hint" in:rise>
            {found.service ? "SAC" : "HSN"} {found.code}: {describe(found)}
        </div>
    {/key}
{:else if found === false}
    <div class="hsn-meaning txt-hint" in:rise>Not in the GST portal's HSN/SAC list. It will still be saved.</div>
{/if}

<style>
    .hsn-meaning {
        /* Inside the field's box, under the input, at the input's inset. */
        margin: 0 var(--inputPadding) 8px;
        font-size: var(--smFontSize, 12px);
        line-height: 1.35;
        display: -webkit-box;
        -webkit-line-clamp: 2;
        line-clamp: 2;
        -webkit-box-orient: vertical;
        overflow: hidden;
    }
</style>
