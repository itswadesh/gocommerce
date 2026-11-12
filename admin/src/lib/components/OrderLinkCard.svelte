<script>
    /**
     * The store's quick-order link and its QR code (D255), on Payment methods
     * while Order on WhatsApp is on: put in a WhatsApp status or bio, printed
     * for the counter. The engine draws the QR, so the panel carries no
     * library for it, and a store nobody can reach yet shows nothing here.
     */
    import { request } from "$lib/api.js";
    import { copyText } from "$lib/clipboard.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";

    let link = $state(null);
    let copied = $state(false);
    let saving = $state(false);
    let timer;

    $effect(() => {
        request("GET", "/api/admin/order-link")
            .then((l) => (link = l))
            .catch(() => (link = null));
    });

    const qr = $derived(link?.qr_svg ? "data:image/svg+xml;charset=utf-8," + encodeURIComponent(link.qr_svg) : "");

    async function copy() {
        if (await copyText(link.url)) {
            copied = true;
            clearTimeout(timer);
            timer = setTimeout(() => (copied = false), 2000);
        } else {
            toast.error("The browser would not copy it. Select the link and copy it by hand.");
        }
    }

    // A PNG rather than the SVG: a WhatsApp status takes a picture, and so does
    // a print shop. 1,024 pixels prints sharp well past the size of a counter card.
    async function download() {
        saving = true;
        try {
            const img = new Image();
            img.src = qr;
            await img.decode();
            const canvas = document.createElement("canvas");
            canvas.width = canvas.height = 1024;
            const ctx = canvas.getContext("2d");
            ctx.imageSmoothingEnabled = false;
            ctx.drawImage(img, 0, 0, 1024, 1024);
            const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/png"));
            const a = document.createElement("a");
            a.href = URL.createObjectURL(blob);
            a.download = "order-qr-code.png";
            a.click();
            setTimeout(() => URL.revokeObjectURL(a.href), 1000);
        } catch {
            toast.error("The QR code could not be saved. Right-click it and save the picture instead.");
        } finally {
            saving = false;
        }
    }
</script>

{#if link?.url}
    <section class="card order-link-card m-t-base" aria-labelledby="order-link-title" in:rise>
        <img class="order-link-qr" src={qr} width="132" height="132" alt="QR code that opens {link.url}" />
        <div class="order-link-body">
            <h2 id="order-link-title" class="order-link-title">Your order link</h2>
            <p class="txt-hint order-link-text">
                Shoppers pick what they want on one page, then send you the order on WhatsApp. Put the link in your
                WhatsApp status or bio, and print the QR code for your counter.
            </p>
            <a class="order-link-url" href={link.url} target="_blank" rel="noopener noreferrer">{link.url}</a>
            <div class="flex gap-10 m-t-sm">
                <button type="button" class="btn sm secondary order-link-copy" class:is-copied={copied} onclick={copy}>
                    <i class={copied ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                    <span class="txt">{copied ? "Copied" : "Copy link"}</span>
                </button>
                <button
                    type="button"
                    class="btn sm transparent secondary"
                    class:loading={saving}
                    disabled={saving}
                    onclick={download}
                >
                    <i class="ri-download-2-line" aria-hidden="true"></i>
                    <span class="txt">Download QR code</span>
                </button>
            </div>
            <span class="order-link-status" aria-live="polite">{copied ? "Link copied" : ""}</span>
        </div>
    </section>
{/if}

<style>
    .order-link-card {
        display: flex;
        gap: var(--baseSpacing);
        align-items: flex-start;
    }
    /* A printed code is black on white whatever the panel's theme: a scanner
       needs the contrast, and the plate shows what will be printed. */
    .order-link-qr {
        flex: 0 0 auto;
        background: #fff;
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
    }
    .order-link-body {
        min-width: 0;
    }
    .order-link-title {
        margin: 0 0 4px;
        font-size: 1em;
        font-weight: 600;
    }
    .order-link-text {
        max-width: 68ch;
        margin: 0;
    }
    .order-link-url {
        display: inline-block;
        margin-top: 6px;
        overflow-wrap: anywhere;
        font-variant-numeric: tabular-nums;
    }
    .order-link-copy > i {
        transition: transform 150ms ease-out;
    }
    .order-link-copy.is-copied > i {
        color: var(--successColor);
        transform: scale(1.15);
    }
    .order-link-status {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip-path: inset(50%);
        white-space: nowrap;
    }
    @media (max-width: 520px) {
        .order-link-card {
            flex-direction: column;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .order-link-copy > i {
            transition: none;
        }
        .order-link-copy.is-copied > i {
            transform: none;
        }
    }
</style>
