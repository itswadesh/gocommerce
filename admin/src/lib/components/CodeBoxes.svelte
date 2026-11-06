<script>
    /**
     * A texted code, shown as one box per digit.
     *
     * One real input lies over the boxes, so typing, deleting, pasting a whole
     * code and the phone's own one-time-code suggestion all work as they do in
     * any field, and a screen reader hears one field. Keeping a separate input
     * per digit would have to rebuild all of that by hand. No maxlength: it would
     * cut a pasted "654 321" before the spaces come out.
     */
    let {
        id,
        value = $bindable(""),
        length = 6,
        invalid = false,
        describedby = undefined,
        autofocus = false,
        oninput = undefined,
    } = $props();

    function digitsOnly(e) {
        const digits = e.currentTarget.value.replace(/\D/g, "").slice(0, length);
        e.currentTarget.value = digits;
        value = digits;
        oninput?.();
    }
</script>

<div class="code-boxes" class:invalid style="--code-length: {length}">
    <!-- svelte-ignore a11y_autofocus -->
    <input
        {id}
        type="text"
        inputmode="numeric"
        pattern={`[0-9]{${length}}`}
        required
        {autofocus}
        autocomplete="one-time-code"
        autocapitalize="off"
        spellcheck="false"
        enterkeyhint="go"
        aria-invalid={invalid}
        aria-describedby={describedby}
        {value}
        oninput={digitsOnly}
    />
    <div class="code-cells" aria-hidden="true">
        {#each { length }, i}
            <span class="code-cell" class:filled={i < value.length} class:next={i === Math.min(value.length, length - 1)}>{value[i] ?? ""}</span>
        {/each}
    </div>
</div>

<style>
    .code-boxes {
        position: relative;
        width: 100%;
        max-width: calc(var(--code-length) * 52px + (var(--code-length) - 1) * 8px);
    }
    /* Over the boxes and see-through, so a tap anywhere lands in the field. */
    input {
        position: absolute;
        inset: 0;
        z-index: 1;
        width: 100%;
        height: 100%;
        padding: 0;
        border: 0;
        outline: 0;
        background: transparent;
        color: transparent;
        caret-color: transparent;
        font-size: 16px;
        cursor: text;
    }
    input::selection {
        background: transparent;
        color: transparent;
    }
    .code-cells {
        display: grid;
        grid-template-columns: repeat(var(--code-length), minmax(0, 1fr));
        gap: 8px;
    }
    .code-cell {
        display: flex;
        align-items: center;
        justify-content: center;
        min-width: 0;
        height: 52px;
        border: 1px solid var(--border);
        border-radius: 8px;
        background: var(--background, transparent);
        color: var(--foreground, inherit);
        font-size: 22px;
        font-weight: 600;
        font-variant-numeric: tabular-nums;
        transition: border-color 0.15s ease, box-shadow 0.15s ease, transform 0.15s ease;
    }
    .code-cell.filled {
        border-color: var(--primary);
        transform: scale(1.03);
    }
    input:focus-visible + .code-cells .code-cell.next {
        border-color: var(--primary);
        box-shadow: 0 0 0 3px color-mix(in oklch, var(--primary) 20%, transparent);
    }
    .invalid .code-cell {
        border-color: var(--danger, oklch(55% 0.2 27));
    }
    @media (prefers-reduced-motion: reduce) {
        .code-cell {
            transition: border-color 0.15s ease, box-shadow 0.15s ease;
        }
        .code-cell.filled {
            transform: none;
        }
    }
</style>
