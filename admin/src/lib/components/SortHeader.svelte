<script>
    /**
     * A clickable column header.
     *
     * It renders exactly one `<th>` and adds zero CSS. `th.sort-handle`, its
     * `.asc` / `.desc` arrows and its hover, `:focus-visible` and `:active`
     * states are PocketBase's own, sitting unused in table.css since the panel
     * was copied from it — so this is behaviour on a class that was already
     * there rather than a new rule in a file rule 12 freezes.
     *
     * The `<th>` itself is the focusable element, not a button inside it: the
     * stylesheet styles `:focus-visible` on the th, and a nested button would
     * draw the ring in the wrong place. It keeps its implicit columnheader
     * role for the same reason — `aria-sort` is only meaningful on one, so
     * `role="button"` would cancel the attribute that carries the meaning.
     *
     * PocketBase draws `.asc` as a down arrow (\EA4C) and `.desc` as an up one
     * (\EA76), which is the "A at the top, Z below" reading rather than the
     * "values rising" one. The classes stay semantic, because relabelling them
     * means editing table.css; `aria-sort` and the tooltip say which way it is
     * in words, which is what makes the glyph unambiguous either way.
     */
    let { field, label, sort, onsort, firstDesc = false, class: klass = "" } = $props();

    const active = $derived(sort.field === field);
    const direction = $derived(active ? (sort.desc ? "descending" : "ascending") : "none");
    /* Says in words what the arrow says in a glyph, and what the NEXT click will
       do — which is the half an operator cannot guess from a three-state
       control they have not met before. */
    const hint = $derived(
        !active
            ? `Sort by ${label}`
            : sort.desc === firstDesc
              ? `Sorted ${direction} — click to reverse`
              : `Sorted ${direction} — click to clear`,
    );

    function activate() {
        onsort(field, firstDesc);
    }

    function onkeydown(event) {
        if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            activate();
        }
    }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<th
    class="sort-handle {klass}"
    class:asc={active && !sort.desc}
    class:desc={active && sort.desc}
    tabindex="0"
    aria-sort={direction}
    title={hint}
    onclick={activate}
    {onkeydown}
>
    {label}
</th>
