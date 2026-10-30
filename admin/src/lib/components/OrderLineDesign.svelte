<script>
    /**
     * A designed order line as the print operator needs it (D159): the
     * finished product's picture, larger on a press, the print file to
     * download, the shopper's original photos, and the words and fonts in it.
     * Everything is read from the copy the order line keeps
     * (`metadata.design`), so it is the design as it was bought.
     */
    import { designSummary } from "$lib/customizer.js";

    let { design, title = "" } = $props();

    const summary = $derived(designSummary(design));
    let dialog = $state(null);
    let opener = null;

    function open(e) {
        opener = e.currentTarget;
        dialog.showModal();
    }

    function closed() {
        opener?.focus();
    }
</script>

<div class="line-design">
    <button type="button" class="line-design-thumb" onclick={open} aria-label="Show the design of {title} larger">
        <img src={design.preview_url} alt="" loading="lazy" />
    </button>
    <div class="line-design-text">
        <div class="txt-sm">
            <i class="ri-brush-line" aria-hidden="true"></i>
            Made to the shopper's design
        </div>
        {#if summary.words.length}
            <ul class="line-design-words txt-sm">
                {#each summary.words as word, i (i)}
                    <li>
                        <span class="line-design-swatch" style="background: {word.color}" aria-hidden="true"></span>
                        “{word.text}” in {word.font}
                    </li>
                {/each}
            </ul>
        {/if}
        <div class="txt-hint txt-sm">
            {[
                summary.photos && `${summary.photos} ${summary.photos === 1 ? "photo" : "photos"}`,
                summary.shapes && `${summary.shapes} ${summary.shapes === 1 ? "shape" : "shapes"}`,
                summary.stickers && `${summary.stickers} ${summary.stickers === 1 ? "sticker" : "stickers"}`,
                summary.background && `background ${summary.background}`,
            ]
                .filter(Boolean)
                .join(" · ")}
        </div>
        <ul class="line-design-links txt-sm">
            <li>
                <a href={design.print_url} download>
                    <i class="ri-download-2-line" aria-hidden="true"></i>
                    Download print file
                </a>
            </li>
            {#each design.photos ?? [] as photo, i (photo.upload_id)}
                <li>
                    <a href={photo.url} target="_blank" rel="noopener">
                        <i class="ri-image-line" aria-hidden="true"></i>
                        Photo {i + 1} ({photo.width} × {photo.height})
                    </a>
                </li>
            {/each}
        </ul>
    </div>
</div>

<dialog bind:this={dialog} class="line-design-dialog" aria-label="The design of {title}" onclose={closed}>
    <form method="dialog">
        <button class="btn circle sm transparent secondary line-design-close" aria-label="Close">
            <i class="ri-close-line" aria-hidden="true"></i>
        </button>
    </form>
    <img src={design.preview_url} alt="The design of {title}" />
</dialog>

<style>
    .line-design {
        display: flex;
        gap: 10px;
        margin-top: 8px;
    }
    .line-design-thumb {
        flex: none;
        width: 56px;
        height: 72px;
        padding: 0;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
        overflow: hidden;
        cursor: zoom-in;
        transition: border-color 0.15s ease;
    }
    .line-design-thumb:hover {
        border-color: var(--surfaceTxtHintColor);
    }
    .line-design-thumb img {
        width: 100%;
        height: 100%;
        object-fit: contain;
    }
    .line-design-text {
        min-width: 0;
    }
    .line-design-words {
        list-style: none;
        margin: 2px 0;
        padding: 0;
    }
    .line-design-words li {
        overflow-wrap: anywhere;
    }
    .line-design-swatch {
        display: inline-block;
        width: 10px;
        height: 10px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: 2px;
        vertical-align: -1px;
    }
    .line-design-links {
        display: flex;
        flex-wrap: wrap;
        gap: 2px 14px;
        margin: 4px 0 0;
        padding: 0;
        list-style: none;
    }
    .line-design-links a {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        min-height: 24px;
        transition: opacity 0.15s ease;
    }
    .line-design-links a:hover {
        opacity: 0.75;
    }
    .line-design-dialog {
        max-width: min(92vw, 560px);
        max-height: 90vh;
        padding: 12px;
        border: 0;
        border-radius: var(--borderRadius);
        background: var(--surfaceColor);
        opacity: 1;
        transition:
            opacity 0.2s ease,
            display 0.2s allow-discrete,
            overlay 0.2s allow-discrete;
    }
    .line-design-dialog:not([open]) {
        opacity: 0;
    }
    @starting-style {
        .line-design-dialog[open] {
            opacity: 0;
        }
    }
    .line-design-dialog::backdrop {
        background: rgba(0, 0, 0, 0.5);
    }
    .line-design-dialog img {
        display: block;
        max-width: 100%;
        max-height: calc(90vh - 70px);
        margin: 0 auto;
    }
    .line-design-close {
        float: right;
    }
    @media (max-width: 600px) {
        .line-design {
            flex-direction: column;
        }
    }
    @media (prefers-reduced-motion: no-preference) {
        .line-design-dialog {
            transition:
                opacity 0.2s ease,
                transform 0.2s ease,
                display 0.2s allow-discrete,
                overlay 0.2s allow-discrete;
        }
        .line-design-dialog:not([open]) {
            transform: translateY(8px);
        }
        @starting-style {
            .line-design-dialog[open] {
                transform: translateY(8px);
            }
        }
    }
</style>
