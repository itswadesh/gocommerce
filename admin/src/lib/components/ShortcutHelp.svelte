<script>
    /**
     * What the keyboard does, on the keyboard.
     *
     * A shortcut nobody can discover is a shortcut that exists for the person
     * who wrote it. `?` is the conventional way to ask, and this is the only
     * place in the panel where the answer is written down.
     */
    import { dismissable } from "$lib/dismiss.js";
    import { portal } from "$lib/portal.js";
    import { trapFocus } from "$lib/focus.js";
    import { SHORTCUTS } from "$lib/shortcuts.js";

    let { open = $bindable(false) } = $props();

    /* navigator.platform is deprecated and still the only thing that answers
       this without a permissions prompt; a wrong guess costs one wrong glyph. */
    const mac =
        typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || "");
</script>

<div
    class="modal popup sm"
    data-modal-state={open ? "open" : "closed"}
    inert={!open}
    role="dialog"
    aria-modal="true"
    aria-label="Keyboard shortcuts"
    tabindex="-1"
    use:portal
    use:trapFocus={open}
    use:dismissable={{ onclose: () => (open = false), enabled: open }}
>
    <div class="modal-header">
        <h5 class="modal-title">Keyboard shortcuts</h5>
    </div>
    <div class="modal-content">
        <div class="list sm">
            {#each SHORTCUTS as row (row.what)}
                <div class="list-item">
                    <span class="shortcut-keys">
                        {#each (mac && row.mac) || row.keys as key (key)}
                            <kbd>{key}</kbd>
                        {/each}
                    </span>
                    <div class="content">
                        <span class="txt">{row.what}</span>
                    </div>
                </div>
            {/each}
        </div>
    </div>
    <footer class="modal-footer">
        <button type="button" class="btn transparent m-l-auto" onclick={() => (open = false)}>
            <span class="txt">Close</span>
        </button>
    </footer>
</div>
