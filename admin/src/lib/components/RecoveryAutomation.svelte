<script>
    /**
     * One recovery automation's editor: whether it runs, when a basket counts
     * as abandoned, and the sequence of steps it takes.
     *
     * Both sequences live in one settings object and the engine replaces it
     * whole (`PUT …/settings`), so this edits its own half and sends the other
     * half back exactly as it read it — a save here can never move the other
     * automation. The storefront address is shared by both and is edited from
     * either page.
     *
     * The screen is built around the module's list of actions rather than
     * around email: every action the module names is offered, and the ones it
     * cannot take yet are shown disabled with its reason. Waits are typed in
     * minutes, hours or days and sent as `wait_minutes`; each runs from the step
     * before it, which is how the list reads top to bottom.
     *
     * A refused save names its field in the engine's own sentence ("checkout
     * step 2: wait_minutes must be …"), and the message is put under that box.
     *
     * Drawn as kitcommerce-admin draws it: a workflow of nodes — the trigger,
     * the conditions, a wait and a message per step, the end — with the
     * selected node's settings beside it. Its canvas is a library there; here
     * the sequence is always a straight line, so the nodes are a row of
     * buttons laid out by CSS and the panel ships no graph library for it.
     */
    import { tick, untrack } from "svelte";
    import { flip } from "svelte/animate";
    import { base } from "$app/paths";
    import { can, recovery } from "$lib/api.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import {
        UNITS,
        durationWords,
        joinMinutes,
        settingsError,
        splitMinutes,
        validStorefront,
        workflowNodes,
    } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { rightLabel } from "$lib/rights.js";
    import { flipDuration, rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import SaveBar from "$lib/components/SaveBar.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";

    /** @type {{ kind: "checkout" | "cart" }} */
    let { kind } = $props();

    const WORDS = {
        checkout: {
            title: "Abandoned checkout recovery",
            noun: "checkout",
            what: "Writes to shoppers who started checkout — typed their email address — and left without paying.",
        },
        cart: {
            title: "Abandoned cart recovery",
            noun: "cart",
            what: "Writes to shoppers who added items to their cart and left before starting checkout. Only a shopper who was signed in can be reached.",
        },
    };
    const words = $derived(WORDS[kind]);

    /* The engine's limits, restated so a box can say so before the request. */
    const MAX_STEPS = 5;
    const MAX_THRESHOLD = 7 * 24 * 60;
    const MAX_WAIT = 30 * 24 * 60;

    const readable = $derived(can("abandonment.read"));
    const writable = $derived(can("abandonment.automate"));
    /* Trusted only where the store could have been asked: the probe is made for
       an operator holding this right or orders.read (modules.svelte.js), and
       for anybody else the rights answer below is the true one. */
    const missing = $derived(
        modulesKnown() && !hasModule("cart-recovery") && (readable || can("orders.read")),
    );

    let view = $state(null);
    let draft = $state(null);
    let loading = $state(true);
    let saving = $state(false);
    /** { form, threshold, storefront, steps: { [index]: message },
        stepField: { [index]: "wait" | "action" } } — which box of the step it is */
    let errors = $state({ steps: {}, stepField: {} });

    let keySeq = 0;

    $effect(() => {
        if (readable && hasModule("cart-recovery")) load();
    });

    async function load() {
        loading = true;
        try {
            view = await recovery.settings();
            draft = shape(view.settings[kind], view.settings.storefront_url);
            errors = { steps: {}, stepField: {} };
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /** The editable form of one automation: waits split into a number and a
        unit. `keep` is the draft being replaced: its steps' keys are reused by
        position, so the node whose settings are open stays open. */
    function shape(automation, storefront, keep = []) {
        return {
            enabled: !!automation.enabled,
            threshold: splitMinutes(automation.abandon_after_minutes),
            steps: (automation.steps ?? []).map((s, i) => ({
                _key: keep[i]?._key ?? ++keySeq,
                ...splitMinutes(s.wait_minutes),
                action: s.action,
                template: s.template,
            })),
            storefront: storefront ?? "",
        };
    }

    /** The draft as the API takes it. An unparseable wait travels as null and
     *  is caught by validate() before anything is sent. */
    function wire(d) {
        return {
            enabled: d.enabled,
            abandon_after_minutes: joinMinutes(d.threshold.value, d.threshold.unit),
            steps: d.steps.map((s) => ({
                wait_minutes: joinMinutes(s.value, s.unit),
                action: s.action,
                template: s.template,
            })),
        };
    }

    function serverShape(a) {
        return {
            enabled: !!a.enabled,
            abandon_after_minutes: a.abandon_after_minutes,
            steps: (a.steps ?? []).map((s) => ({ wait_minutes: s.wait_minutes, action: s.action, template: s.template })),
        };
    }

    const dirty = $derived(
        !!view &&
            !!draft &&
            (JSON.stringify(wire(draft)) !== JSON.stringify(serverShape(view.settings[kind])) ||
                draft.storefront.trim() !== (view.settings.storefront_url ?? "")),
    );

    const atDefaults = $derived(
        !!view && !!draft && JSON.stringify(wire(draft)) === JSON.stringify(serverShape(view.defaults[kind])),
    );

    const templateOptions = $derived(
        (view?.templates ?? []).map((t) => ({
            value: t.event,
            label: t.title,
            note: t.customized ? "Reworded for this store" : "",
        })),
    );
    const actionOptions = $derived(
        (view?.actions ?? []).map((a) => ({
            value: a.key,
            label: a.label,
            disabled: !a.available,
            note: a.available ? "" : a.reason,
        })),
    );
    const unitOptions = UNITS.map((u) => ({ value: u.value, label: u.label }));

    /* When each step lands, counted from the moment the basket was abandoned. */
    const arrivals = $derived.by(() => {
        if (!draft) return [];
        let total = 0;
        return draft.steps.map((s) => {
            const m = joinMinutes(s.value, s.unit);
            if (m === null || total === null) {
                total = null;
                return null;
            }
            total += m;
            return total;
        });
    });

    function validate() {
        const next = { steps: {}, stepField: {} };
        const t = joinMinutes(draft.threshold.value, draft.threshold.unit);
        if (t === null) next.threshold = "Type a whole number.";
        else if (t < 1 || t > MAX_THRESHOLD) next.threshold = "Between 1 minute and 7 days.";
        draft.steps.forEach((s, i) => {
            const m = joinMinutes(s.value, s.unit);
            if (m === null) [next.steps[i], next.stepField[i]] = ["Type the wait as a whole number.", "wait"];
            else if (m < 1 || m > MAX_WAIT) [next.steps[i], next.stepField[i]] = ["A wait is between 1 minute and 30 days.", "wait"];
            else if (!actionOptions.find((a) => a.value === s.action && !a.disabled)) {
                [next.steps[i], next.stepField[i]] = ["Choose an action this store can take.", "action"];
            }
        });
        if (!validStorefront(draft.storefront)) {
            next.storefront = "An absolute address such as https://shop.example.com.";
        }
        errors = next;
        return !next.threshold && !next.storefront && !Object.keys(next.steps).length;
    }

    async function save() {
        if (!draft || saving) return;
        if (!validate()) {
            toast.warning("Some fields need a look before this can be saved");
            const bad = nodes.find((n) => nodeError(n));
            if (bad) select(bad.id, { keep: true });
            return;
        }
        saving = true;
        try {
            // Both sequences, always: this side from the draft, the other as read.
            const body = {
                ...view.settings,
                [kind]: wire(draft),
                storefront_url: draft.storefront.trim(),
            };
            view = await recovery.saveSettings(body);
            draft = shape(view.settings[kind], view.settings.storefront_url, draft.steps);
            errors = { steps: {}, stepField: {} };
            toast.success(`${words.title} saved`);
        } catch (err) {
            if (err.status === 400) {
                const where = settingsError(err.message);
                const next = { steps: {}, stepField: {} };
                if (where.field === "storefront") next.storefront = where.message;
                else if (where.side && where.side !== kind) next.form = where.message;
                else if (where.field === "threshold") next.threshold = where.message;
                else if (where.field === "step") {
                    next.steps[where.step] = where.message;
                    next.stepField[where.step] = /wait_minutes/.test(where.message) ? "wait" : "action";
                } else next.form = where.message;
                errors = next;
                const bad = nodes.find((n) => nodeError(n));
                if (bad) select(bad.id, { keep: true });
            }
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function discard() {
        draft = shape(view.settings[kind], view.settings.storefront_url, draft.steps);
        errors = { steps: {}, stepField: {} };
    }

    function resetToDefaults() {
        // The sequence only: the storefront address is shared by both
        // automations and has no default of its own to go back to.
        draft = shape(view.defaults[kind], draft.storefront, draft.steps);
        errors = { steps: {}, stepField: {} };
        toast.info("The defaults are in the form. Save to keep them.");
    }

    /* ------------------------------------------------------------ canvas */

    /** The node whose settings are open beside the canvas, by id. */
    let selected = $state("");
    let zoom = $state(1);
    /** Once somebody zooms by hand, the canvas stops fitting itself. */
    let manualZoom = false;
    let canvas = $state(null);
    let inspector = $state(null);

    const ZOOM_MIN = 0.4;
    const ZOOM_MAX = 1.5;
    const AUTO_FIT_MIN = 0.8;

    const stepAt = (id) => draft?.steps.findIndex((s) => id === `wait-${s._key}` || id === `send-${s._key}`) ?? -1;

    const templateTitle = (event) => templateOptions.find((t) => t.value === event)?.label ?? "";
    const actionLabel = (key) => actionOptions.find((a) => a.value === key)?.label ?? key;

    const nodes = $derived(
        draft
            ? workflowNodes({
                  kind,
                  threshold: joinMinutes(draft.threshold.value, draft.threshold.unit),
                  steps: draft.steps.map((s) => ({ ...s, wait: joinMinutes(s.value, s.unit) })),
                  guards: view?.guards?.length ?? 0,
                  actionLabel,
                  templateTitle,
              })
            : [],
    );
    const selectedStep = $derived(stepAt(selected));
    const selectedNode = $derived(nodes.find((n) => n.id === selected) ?? null);

    /** Whether a node carries a refused or unparseable value, for its badge:
        a step's wait or its message, whichever box it was. */
    function nodeError(node) {
        if (node.kind === "trigger") return !!errors.threshold;
        if (node.step === undefined || !errors.steps?.[node.step]) return false;
        return (errors.stepField?.[node.step] ?? "action") === (node.kind === "wait" ? "wait" : "action");
    }
    const stepError = (field) => (errors.stepField?.[selectedStep] === field ? errors.steps?.[selectedStep] : "");

    /** A node's name for a screen reader: a wait says which message it is before. */
    function nodeName(node) {
        const name = node.kind === "wait" ? `Wait before message ${node.step + 1}` : node.label;
        return `${name}: ${node.sub}${nodeError(node) ? " (needs a look)" : ""}`;
    }

    /* Selecting a node moves focus to its settings, so a keyboard is one Tab
       from the boxes rather than past every node; Escape brings it back. */
    async function select(id, { keep = false } = {}) {
        selected = selected === id && !keep ? "" : id;
        if (!selected) return;
        await tick();
        inspector?.querySelector(".recovery-inspector-title")?.focus();
    }

    async function focusNode(id) {
        await tick();
        canvas?.querySelector(`[data-node="${id}"]`)?.focus();
    }

    function onKey(event) {
        if (event.key !== "Escape" || !selected) return;
        const id = selected;
        selected = "";
        focusNode(id);
    }

    function setZoom(next) {
        manualZoom = true;
        zoom = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, Math.round(next * 10) / 10));
    }

    /* Fit: the whole row in the canvas's width, never larger than life. The
       row's natural width is known from its parts — 112px a node, 36px a link
       and 56px of padding, as the stylesheet draws them — rather than read
       back through a zoom, and the step is rounded down so it always fits. */
    function fit({ floor = ZOOM_MIN } = {}) {
        if (!canvas) return;
        const natural = nodes.length * 112 + (nodes.length - 1) * 36 + 56;
        const room = canvas.clientWidth / natural;
        zoom = Math.min(1, Math.max(floor, Math.floor(room * 10) / 10));
    }

    /* Fitting on its own stops where the labels stay readable; past that the
       row scrolls, and the Fit button goes the rest of the way. */
    const autoFit = () => !manualZoom && fit({ floor: AUTO_FIT_MIN });

    /* The workflow arrives fitted to its canvas, and keeps fitting as the
       window or the number of nodes changes — until somebody zooms by hand. */
    $effect(() => {
        if (!canvas) return;
        const ro = new ResizeObserver(autoFit);
        ro.observe(canvas);
        return () => ro.disconnect();
    });
    $effect(() => {
        nodes.length;
        if (canvas) untrack(autoFit);
    });

    /** A wait and a message, inserted at `at` — between two nodes, or at the end. */
    function insertStep(at) {
        if (!writable || draft.steps.length >= MAX_STEPS) return;
        const template = templateOptions[Math.min(at, templateOptions.length - 1)]?.value ?? "";
        const action = actionOptions.find((a) => !a.disabled)?.value ?? "";
        const step = { _key: ++keySeq, value: 1, unit: "days", action, template };
        draft.steps = [...draft.steps.slice(0, at), step, ...draft.steps.slice(at)];
        errors = { ...errors, steps: {}, stepField: {} };
        selected = `send-${step._key}`;
    }

    /* The deleted node's button had focus; it goes to the node that took its
       place, or to End. */
    function removeStep(index) {
        draft.steps = draft.steps.filter((_, i) => i !== index);
        errors = { ...errors, steps: {}, stepField: {} };
        selected = "";
        const next = draft.steps[Math.min(index, draft.steps.length - 1)];
        focusNode(next ? `send-${next._key}` : "end");
    }

    function moveStep(index, by) {
        const to = index + by;
        if (to < 0 || to >= draft.steps.length) return;
        const next = [...draft.steps];
        [next[index], next[to]] = [next[to], next[index]];
        draft.steps = next;
        errors = { ...errors, steps: {}, stepField: {} };
    }

    /* When each step lands, from abandonment — for the header and the waits. */
    const lastArrival = $derived(arrivals.length ? arrivals[arrivals.length - 1] : null);

    const NODE_ICONS = {
        filter: "ri-filter-3-line",
        wait: "ri-time-line",
        action: "ri-mail-line",
        end: "ri-flag-line",
    };
    const nodeIcon = (node) =>
        node.kind === "trigger" ? (kind === "cart" ? "ri-shopping-cart-2-line" : "ri-bank-card-line") : NODE_ICONS[node.kind];

    const readOnlyNote = `Your role can see this automation but not change it: that needs “${rightLabel("abandonment.automate")}”.`;
</script>

<svelte:head><title>{titled(words.title)}</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="the marketing automations" />
{:else}
    <div class="page page-recovery-automation shopify-skin skin-recessed recovery-editor">
        <div class="page-content full-height">
            <SaveBar {dirty} {saving} onsave={save} ondiscard={discard} />

            <header class="page-header recovery-detail-header">
                <a
                    class="btn circle transparent secondary recovery-back"
                    href="{base}/dash/marketing/automations"
                    aria-label="Back to automations"
                    title="Back to automations"
                >
                    <i class="ri-arrow-left-line" aria-hidden="true"></i>
                </a>
                <div class="recovery-title-block">
                    <h1 class="recovery-h1">{words.title}</h1>
                    <p class="txt-hint txt-sm">{words.what}</p>
                </div>
                <div class="flex-fill"></div>
                {#if view && draft}
                    <div class="page-header-primary-btns">
                        <button
                            type="button"
                            class="btn sm secondary"
                            disabled={!writable || atDefaults}
                            title={!writable ? readOnlyNote : atDefaults ? "Already the defaults" : "Put this sequence back to the one the store ships with"}
                            onclick={resetToDefaults}
                        >
                            <i class="ri-restart-line" aria-hidden="true"></i>
                            <span class="txt">Reset to defaults</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="cart-recovery"
                    what="The recovery automations are ext/cart-recovery's, and this binary does not have it."
                />
            {:else if loading && !draft}
                <div class="wrapper">
                    <section class="card"><span class="skeleton-loader lg"></span><span class="skeleton-loader"></span></section>
                    <section class="card"><span class="skeleton-loader"></span><span class="skeleton-loader"></span><span class="skeleton-loader"></span></section>
                </div>
            {:else if draft && view}
                <div class="wrapper recovery-page recovery-editor-page">
                    {#if !writable}
                        <div class="alert info m-b-sm" role="status">
                            <p>{readOnlyNote}</p>
                        </div>
                    {/if}
                    {#if !view.email_delivers}
                        <div class="alert warning recovery-banner m-b-sm" role="status">
                            <i class="ri-mail-close-line" aria-hidden="true"></i>
                            <div>
                                <p><strong>No email provider is set up, so no step can send.</strong></p>
                                <p>
                                    <a class="recovery-arrow-link" href="{base}/dash/notifications/email">
                                        Set one up in Notifications › Setup Email <span aria-hidden="true">→</span>
                                    </a>
                                </p>
                            </div>
                        </div>
                    {/if}
                    {#if errors.form}
                        <div class="alert danger m-b-sm" role="alert" transition:rise>
                            <p>{errors.form}</p>
                        </div>
                    {/if}

                    <section class="card recovery-workflow">
                        <div class="recovery-workflow-head">
                            <div>
                                <h6 class="section-title m-b-0">Workflow</h6>
                                <p class="txt-hint txt-sm m-0">
                                    {draft.steps.length} of {MAX_STEPS} messages{lastArrival !== null && draft.steps.length
                                        ? ` · last ≈ ${durationWords(lastArrival)} after abandonment`
                                        : ""}
                                </p>
                            </div>
                            <div class="field recovery-workflow-switch">
                                <input
                                    type="checkbox"
                                    id="recovery-enabled-{kind}"
                                    class="switch"
                                    disabled={!writable}
                                    bind:checked={draft.enabled}
                                />
                                <label for="recovery-enabled-{kind}">{draft.enabled ? "Active" : "Inactive"}</label>
                            </div>
                        </div>
                        <p class="txt-hint txt-sm recovery-workflow-help">
                            Switching it off stops every pending reminder the moment you save; switching it on schedules
                            them again from each {words.noun}'s last message.
                        </p>

                        <!-- svelte-ignore a11y_no_static_element_interactions -->
                        <div class="recovery-flow-layout" onkeydown={onKey}>
                            <div class="recovery-canvas" class:is-off={!draft.enabled} bind:this={canvas} role="group" aria-label="Workflow">
                                <div class="recovery-canvas-scroll">
                                <div class="recovery-flow" style:zoom={zoom}>
                                    {#each nodes as node, i (node.id)}
                                        <!-- One element per node and its incoming link, so a
                                             move slides and a delete fades rather than jumping.
                                             The slide is skipped while zoomed: flip measures
                                             the screen, and the zoom would scale its distance. -->
                                        <div
                                            class="recovery-flow-item"
                                            animate:flip={{ duration: zoom === 1 ? flipDuration() : 0 }}
                                            transition:rise
                                        >
                                        {#if i > 0}
                                            {@const insertAt =
                                                node.kind === "wait" ? node.step : node.kind === "end" ? draft.steps.length : -1}
                                            <span class="recovery-edge" aria-hidden={insertAt < 0 || !writable}>
                                                {#if insertAt >= 0 && writable && draft.steps.length < MAX_STEPS}
                                                    <button
                                                        type="button"
                                                        class="recovery-edge-add"
                                                        aria-label={insertAt === draft.steps.length
                                                            ? "Add a step at the end"
                                                            : `Add a step before message ${insertAt + 1}`}
                                                        title="Add node"
                                                        onclick={() => insertStep(insertAt)}
                                                    >
                                                        <i class="ri-add-line" aria-hidden="true"></i>
                                                    </button>
                                                {/if}
                                            </span>
                                        {/if}
                                        <button
                                            type="button"
                                            class="recovery-node is-{node.kind}"
                                            class:is-selected={selected === node.id}
                                            class:has-error={nodeError(node)}
                                            data-node={node.id}
                                            aria-pressed={selected === node.id}
                                            aria-label={nodeName(node)}
                                            onclick={() => select(node.id)}
                                        >
                                            <span class="recovery-node-box">
                                                {#if node.kind === "trigger"}
                                                    <i class="ri-flashlight-fill recovery-node-zap" aria-hidden="true"></i>
                                                {/if}
                                                <span class="recovery-node-icon" aria-hidden="true">
                                                    <i class={nodeIcon(node)}></i>
                                                </span>
                                                {#if nodeError(node)}
                                                    <span class="recovery-node-badge" aria-hidden="true">
                                                        <i class="ri-error-warning-fill"></i>
                                                    </span>
                                                {/if}
                                            </span>
                                            <span class="recovery-node-label">{node.label}</span>
                                            <span class="recovery-node-sub">{node.sub}</span>
                                        </button>
                                        </div>
                                    {/each}
                                </div>
                                </div>

                                <div class="recovery-canvas-controls">
                                    <button
                                        type="button"
                                        class="btn xs circle secondary"
                                        aria-label="Zoom out"
                                        title="Zoom out"
                                        class:is-blocked={zoom <= ZOOM_MIN}
                                        aria-disabled={zoom <= ZOOM_MIN}
                                        onclick={() => setZoom(zoom - 0.1)}
                                    >
                                        <i class="ri-zoom-out-line" aria-hidden="true"></i>
                                    </button>
                                    <button
                                        type="button"
                                        class="btn xs circle secondary"
                                        aria-label="Zoom in"
                                        title="Zoom in"
                                        class:is-blocked={zoom >= ZOOM_MAX}
                                        aria-disabled={zoom >= ZOOM_MAX}
                                        onclick={() => setZoom(zoom + 0.1)}
                                    >
                                        <i class="ri-zoom-in-line" aria-hidden="true"></i>
                                    </button>
                                    <button
                                        type="button"
                                        class="btn xs circle secondary"
                                        aria-label="Fit to view"
                                        title="Fit to view"
                                        onclick={() => {
                                            manualZoom = false;
                                            fit();
                                        }}
                                    >
                                        <i class="ri-fullscreen-exit-line" aria-hidden="true"></i>
                                    </button>
                                </div>
                                {#if writable}
                                    <button
                                        type="button"
                                        class="btn xs secondary recovery-canvas-add"
                                        disabled={draft.steps.length >= MAX_STEPS}
                                        title={draft.steps.length >= MAX_STEPS ? `Up to ${MAX_STEPS} messages can be connected` : undefined}
                                        onclick={() => insertStep(draft.steps.length)}
                                    >
                                        <i class="ri-add-line" aria-hidden="true"></i>
                                        <span class="txt">Add node</span>
                                    </button>
                                {/if}
                            </div>

                            <aside class="recovery-inspector" bind:this={inspector} aria-label="Node settings">
                                {#if !selectedNode}
                                    <div class="recovery-inspector-empty" in:rise>
                                        <i class="ri-cursor-line" aria-hidden="true"></i>
                                        {#if writable}
                                            <p class="txt-bold">Select a node to edit it</p>
                                            <ul class="txt-hint txt-sm">
                                                <li>Click <strong>+</strong> between two nodes to add a wait and a message there.</li>
                                                <li>Select a message to choose its template, move it or delete it.</li>
                                                <li>Select the trigger to change when a {words.noun} counts as abandoned.</li>
                                            </ul>
                                        {:else}
                                            <p class="txt-bold">Select a node to see its settings</p>
                                        {/if}
                                    </div>
                                {:else if selectedNode.kind === "trigger"}
                                    <div in:rise>
                                        <h6 class="recovery-inspector-title" tabindex="-1">{selectedNode.label}</h6>
                                        <p class="txt-sm m-b-sm">When should a {words.noun} become abandoned?</p>
                                        <div class="recovery-sentence">
                                            <div class="field recovery-num" class:error={!!errors.threshold}>
                                                <input
                                                    type="number"
                                                    min="1"
                                                    step="1"
                                                    inputmode="numeric"
                                                    aria-label="Threshold"
                                                    disabled={!writable}
                                                    bind:value={draft.threshold.value}
                                                />
                                            </div>
                                            <div class="field recovery-unit">
                                                <Select
                                                    ariaLabel="Threshold unit"
                                                    value={draft.threshold.unit}
                                                    options={unitOptions}
                                                    disabled={!writable}
                                                    onchange={(v) => (draft.threshold.unit = v)}
                                                />
                                            </div>
                                            <span>of inactivity</span>
                                        </div>
                                        {#if errors.threshold}
                                            <div class="field-help error" transition:rise>{errors.threshold}</div>
                                        {:else}
                                            <div class="field-help">
                                                Any change to the {words.noun} — an item added, an address typed — starts the
                                                clock again. It is recorded as abandoned whether or not this automation is on.
                                            </div>
                                        {/if}
                                    </div>
                                {:else if selectedNode.kind === "filter"}
                                    <div in:rise>
                                        <h6 class="recovery-inspector-title" tabindex="-1">Only continue if</h6>
                                        <ul class="recovery-guard-list">
                                            {#each view.guards as guard (guard)}
                                                <li><i class="ri-check-line" aria-hidden="true"></i>{guard}</li>
                                            {/each}
                                        </ul>
                                        <div class="field-help">
                                            Built in and always on, and checked again at the moment of each send. {words.noun ===
                                            "cart"
                                                ? "Carts"
                                                : "Checkouts"} that went idle before recovery was set up for this store are listed but
                                            never messaged.
                                        </div>
                                    </div>
                                {:else if selectedNode.kind === "wait" && selectedStep >= 0}
                                    {@const step = draft.steps[selectedStep]}
                                    <div in:rise>
                                        <h6 class="recovery-inspector-title" tabindex="-1">Wait</h6>
                                        <div class="recovery-sentence">
                                            <span>Wait for</span>
                                            <div class="field recovery-num" class:error={!!stepError("wait")}>
                                                <input
                                                    type="number"
                                                    min="1"
                                                    step="1"
                                                    inputmode="numeric"
                                                    aria-label="Wait before message {selectedStep + 1}"
                                                    disabled={!writable}
                                                    bind:value={step.value}
                                                />
                                            </div>
                                            <div class="field recovery-unit">
                                                <Select
                                                    ariaLabel="Wait unit"
                                                    value={step.unit}
                                                    options={unitOptions}
                                                    disabled={!writable}
                                                    onchange={(v) => (step.unit = v)}
                                                />
                                            </div>
                                        </div>
                                        {#if stepError("wait")}
                                            <div class="field-help error" transition:rise>{stepError("wait")}</div>
                                        {:else}
                                            <div class="field-help">
                                                {selectedStep === 0 ? "Counted from abandonment." : "Counted from the message before."}
                                                {#if arrivals[selectedStep] !== null && arrivals[selectedStep] !== undefined}
                                                    Message {selectedStep + 1} goes ≈ {durationWords(arrivals[selectedStep])} after
                                                    abandonment.
                                                {/if}
                                            </div>
                                        {/if}
                                    </div>
                                {:else if selectedNode.kind === "action" && selectedStep >= 0}
                                    {@const step = draft.steps[selectedStep]}
                                    <div in:rise>
                                        <h6 class="recovery-inspector-title" tabindex="-1">Message {selectedStep + 1}</h6>
                                        <div class="field">
                                            <label for="recovery-action-{kind}">Action</label>
                                            <Select
                                                id="recovery-action-{kind}"
                                                value={step.action}
                                                options={actionOptions}
                                                disabled={!writable}
                                                onchange={(v) => (step.action = v)}
                                            />
                                        </div>
                                        <div class="field m-t-sm">
                                            <label for="recovery-template-{kind}">Template</label>
                                            <Select
                                                id="recovery-template-{kind}"
                                                value={step.template}
                                                options={templateOptions}
                                                disabled={!writable}
                                                onchange={(v) => (step.template = v)}
                                            />
                                        </div>
                                        <a
                                            class="recovery-arrow-link m-t-5"
                                            href="{base}/dash/notifications/email?template={encodeURIComponent(step.template)}"
                                        >
                                            Edit the wording of {templateTitle(step.template) || "this message"}
                                            <span aria-hidden="true">→</span>
                                        </a>
                                        {#if stepError("action")}
                                            <div class="field-help error" transition:rise>{stepError("action")}</div>
                                        {/if}
                                        {#if writable}
                                            <div class="recovery-inspector-tools">
                                                <!-- aria-disabled, not disabled: a button that disables
                                                     itself under the cursor drops focus to the page. -->
                                                <button
                                                    type="button"
                                                    class="btn sm secondary"
                                                    class:is-blocked={selectedStep === 0}
                                                    aria-disabled={selectedStep === 0}
                                                    onclick={() => moveStep(selectedStep, -1)}
                                                >
                                                    <i class="ri-arrow-left-line" aria-hidden="true"></i>
                                                    <span class="txt">Earlier</span>
                                                </button>
                                                <button
                                                    type="button"
                                                    class="btn sm secondary"
                                                    class:is-blocked={selectedStep === draft.steps.length - 1}
                                                    aria-disabled={selectedStep === draft.steps.length - 1}
                                                    onclick={() => moveStep(selectedStep, 1)}
                                                >
                                                    <i class="ri-arrow-right-line" aria-hidden="true"></i>
                                                    <span class="txt">Later</span>
                                                </button>
                                                <button
                                                    type="button"
                                                    class="btn sm transparent danger m-l-auto"
                                                    onclick={() => removeStep(selectedStep)}
                                                >
                                                    <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                                    <span class="txt">Delete node</span>
                                                </button>
                                            </div>
                                            <div class="field-help">
                                                Deleting a message takes its wait with it. A message sent by hand from a
                                                {words.noun}'s page does not move the sequence on.
                                            </div>
                                        {/if}
                                    </div>
                                {:else if selectedNode.kind === "end"}
                                    <div in:rise>
                                        <h6 class="recovery-inspector-title" tabindex="-1">End</h6>
                                        <p class="txt-sm">
                                            {draft.steps.length
                                                ? "Nothing more is sent after the last message."
                                                : `No messages are connected, so nothing is sent. Abandoned ${words.noun}s are still recorded.`}
                                        </p>
                                        <div class="field-help">Up to {MAX_STEPS} messages can be connected.</div>
                                    </div>
                                {/if}
                            </aside>
                        </div>
                    </section>

                    <section class="card" id="storefront">
                        <h6 class="section-title">
                            <i class="ri-store-2-line" aria-hidden="true"></i>
                            Where the link lands
                        </h6>
                        <div class="field" class:error={!!errors.storefront}>
                            <label for="recovery-storefront-{kind}">Storefront address</label>
                            <input
                                id="recovery-storefront-{kind}"
                                type="url"
                                inputmode="url"
                                autocomplete="off"
                                placeholder={view.storefront_fallback || "https://shop.example.com"}
                                disabled={!writable}
                                bind:value={draft.storefront}
                            />
                        </div>
                        {#if errors.storefront}
                            <div class="field-help error" transition:rise>{errors.storefront}</div>
                        {:else}
                            <div class="field-help">
                                Shared by both automations. A reminder's link opens this address plus
                                <code class="txt-code">/cart/&lt;token&gt;</code>.
                                {#if view.storefront_fallback}
                                    Left empty, the store's configured {view.storefront_fallback} is used.
                                {:else}
                                    Left empty, a reminder carries the basket's reference instead of a link.
                                {/if}
                                {#if !view.tracked}
                                    Clicks are not counted: the store has no public address of its own set.
                                {/if}
                            </div>
                        {/if}
                    </section>

                </div>

                <footer class="page-footer">
                    <span class="txt txt-hint">
                        {#if view.customized && view.updated_at}
                            Last saved <span title={formatDate(view.updated_at)}>{relativeTime(view.updated_at)}</span>{view.updated_by
                                ? ` by ${view.updated_by === "token" ? "an admin token" : view.updated_by}`
                                : ""}
                        {:else}
                            Running the defaults
                        {/if}
                    </span>
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>
{/if}
