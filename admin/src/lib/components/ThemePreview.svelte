<script>
    /**
     * The theme editor's live preview (D126): the store's real storefront in a
     * frame, opened on the home page with a preview key and this panel's
     * origin. The storefront then draws the draft this screen posts to it on
     * every edit — before the autosave reaches the engine — and posts back the
     * id of a section clicked there.
     *
     * Messages go only to the storefront's origin, and one is read only when
     * it comes from this frame's window and that origin. A storefront that
     * does not answer (one built before the editor) still shows the saved
     * draft: the frame reloads after each save instead.
     */
    import { fade } from "svelte/transition";
    import { absoluteMedia } from "$lib/layouts.js";

    let {
        src = "",
        origin = "",
        document = null,
        selected = null,
        savedTick = 0,
        device = "desktop",
        onselect = () => {},
        onblocked = () => {},
    } = $props();

    let frame = $state();
    let ready = $state(false);
    let loaded = $state(false);
    let legacy = $state(false);
    let legacyTimer = null;

    $effect(() => {
        // A new address (a fresh key) is a new page: wait for it to say so.
        src;
        ready = false;
        loaded = false;
        legacy = false;
        clearTimeout(legacyTimer);
    });

    function post(message) {
        if (!ready || !frame?.contentWindow || !origin) return;
        frame.contentWindow.postMessage({ source: "gocommerce-editor", ...message }, origin);
    }

    // At most one draft per frame, however fast the typing.
    let queued = null;
    let raf = 0;
    $effect(() => {
        // A JSON copy: the screen's sections are state proxies, which
        // postMessage cannot clone. Pictures from Files are addressed to this
        // panel's origin, which is the engine's: the storefront would look
        // for /media/… on its own host.
        const doc = document ? absoluteMedia(JSON.parse(JSON.stringify(document)), location.origin) : null;
        if (!ready || !doc) return;
        queued = doc;
        if (raf) return;
        raf = requestAnimationFrame(() => {
            raf = 0;
            post({ type: "draft", document: queued });
        });
    });

    $effect(() => {
        const id = selected;
        if (ready) post({ type: "select", id });
    });

    let lastTick = savedTick;
    $effect(() => {
        if (savedTick === lastTick) return;
        lastTick = savedTick;
        if (ready) post({ type: "saved" });
        else if (legacy && frame) frame.src = src;
    });

    $effect(() => {
        function onMessage(event) {
            if (!frame || event.source !== frame.contentWindow || event.origin !== origin) return;
            const data = event.data;
            if (!data || typeof data !== "object" || data.source !== "gocommerce-storefront") return;
            if (data.type === "ready") {
                ready = true;
                legacy = false;
                clearTimeout(legacyTimer);
            } else if (data.type === "select" && typeof data.id === "string") {
                onselect(data.id);
            }
        }
        function onViolation(event) {
            const directive = event.effectiveDirective || event.violatedDirective || "";
            if (directive.startsWith("frame-src") && origin && String(event.blockedURI).startsWith(origin)) onblocked();
        }
        window.addEventListener("message", onMessage);
        window.addEventListener("securitypolicyviolation", onViolation);
        return () => {
            window.removeEventListener("message", onMessage);
            window.removeEventListener("securitypolicyviolation", onViolation);
            cancelAnimationFrame(raf);
            clearTimeout(legacyTimer);
        };
    });

    function onload() {
        loaded = true;
        clearTimeout(legacyTimer);
        // Only an editor waits on the storefront's answer; someone who may
        // only look is shown the live page, which never gives one.
        if (!document) return;
        legacyTimer = setTimeout(() => {
            if (!ready) legacy = true;
        }, 4000);
    }
</script>

<div class="tp-stage" class:mobile={device === "mobile"}>
    <div class="tp-device">
        {#if src}
            <iframe
                bind:this={frame}
                {src}
                title="Storefront preview"
                {onload}
                sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
                referrerpolicy="no-referrer"
                class:shown={loaded}
            ></iframe>
        {/if}
        {#if !loaded}
            <div class="tp-loading" out:fade={{ duration: 150 }} aria-hidden="true">
                <span class="skeleton-loader"></span>
            </div>
        {/if}
    </div>
    {#if legacy}
        <p class="tp-note txt-hint txt-sm" transition:fade={{ duration: 150 }}>
            The storefront is not answering the editor, so the preview updates after each save rather than as you type. If it stays blank, check the storefront's address.
        </p>
    {/if}
</div>

<style>
    .tp-stage {
        position: relative;
        display: flex;
        flex-direction: column;
        align-items: center;
        height: 100%;
        min-height: 0;
    }
    .tp-device {
        position: relative;
        flex: 1 1 auto;
        width: 100%;
        min-height: 0;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--baseColor, #fff);
        overflow: hidden;
    }
    .tp-stage.mobile .tp-device {
        width: 390px;
        max-width: 100%;
    }
    iframe {
        display: block;
        width: 100%;
        height: 100%;
        border: 0;
        opacity: 0;
        transition: opacity 200ms ease-out;
    }
    iframe.shown {
        opacity: 1;
    }
    .tp-loading {
        position: absolute;
        inset: 0;
        padding: 16px;
        pointer-events: none;
    }
    .tp-note {
        margin: 8px 0 0;
        text-align: center;
    }
    @media (prefers-reduced-motion: reduce) {
        iframe {
            transition: none;
        }
    }
</style>
