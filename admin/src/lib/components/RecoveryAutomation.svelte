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
     */
    import { base } from "$app/paths";
    import { flip } from "svelte/animate";
    import { can, recovery } from "$lib/api.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import {
        UNITS,
        durationWords,
        joinMinutes,
        settingsError,
        splitMinutes,
        validStorefront,
    } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { rightLabel } from "$lib/rights.js";
    import { flipDuration, rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import SaveBar from "$lib/components/SaveBar.svelte";
    import Select from "$lib/components/Select.svelte";

    /** @type {{ kind: "checkout" | "cart" }} */
    let { kind } = $props();

    const WORDS = {
        checkout: {
            title: "Abandoned checkout recovery",
            noun: "checkout",
            what: "A basket that reached checkout: the shopper typed an email address, then left.",
        },
        cart: {
            title: "Abandoned cart recovery",
            noun: "cart",
            what: "A basket left before checkout. It can be written to only when the shopper was signed in, through their account's address.",
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
    /** { form, threshold, storefront, steps: { [index]: message } } */
    let errors = $state({ steps: {} });

    let keySeq = 0;

    $effect(() => {
        if (readable && hasModule("cart-recovery")) load();
    });

    async function load() {
        loading = true;
        try {
            view = await recovery.settings();
            draft = shape(view.settings[kind], view.settings.storefront_url);
            errors = { steps: {} };
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /** The editable form of one automation: waits split into a number and a unit. */
    function shape(automation, storefront) {
        return {
            enabled: !!automation.enabled,
            threshold: splitMinutes(automation.abandon_after_minutes),
            steps: (automation.steps ?? []).map((s) => ({
                _key: ++keySeq,
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

    /** "After step 1 · lands 21 hours after abandonment" — what the wait means. */
    function whenWords(i) {
        const from = i === 0 ? "After the basket is abandoned" : `After step ${i}`;
        const at = arrivals[i];
        return at === null || at === undefined ? from : `${from} · lands ${durationWords(at)} after abandonment`;
    }

    function validate() {
        const next = { steps: {} };
        const t = joinMinutes(draft.threshold.value, draft.threshold.unit);
        if (t === null) next.threshold = "Type a whole number.";
        else if (t < 1 || t > MAX_THRESHOLD) next.threshold = "Between 1 minute and 7 days.";
        draft.steps.forEach((s, i) => {
            const m = joinMinutes(s.value, s.unit);
            if (m === null) next.steps[i] = "Type the wait as a whole number.";
            else if (m < 1 || m > MAX_WAIT) next.steps[i] = "A wait is between 1 minute and 30 days.";
            else if (!actionOptions.find((a) => a.value === s.action && !a.disabled)) {
                next.steps[i] = "Choose an action this store can take.";
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
            draft = shape(view.settings[kind], view.settings.storefront_url);
            errors = { steps: {} };
            toast.success(`${words.title} saved`);
        } catch (err) {
            if (err.status === 400) {
                const where = settingsError(err.message);
                const next = { steps: {} };
                if (where.field === "storefront") next.storefront = where.message;
                else if (where.side && where.side !== kind) next.form = where.message;
                else if (where.field === "threshold") next.threshold = where.message;
                else if (where.field === "step") next.steps[where.step] = where.message;
                else next.form = where.message;
                errors = next;
            }
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function discard() {
        draft = shape(view.settings[kind], view.settings.storefront_url);
        errors = { steps: {} };
    }

    function resetToDefaults() {
        // The sequence only: the storefront address is shared by both
        // automations and has no default of its own to go back to.
        draft = shape(view.defaults[kind], draft.storefront);
        errors = { steps: {} };
        toast.info("The defaults are in the form. Save to keep them.");
    }

    function addStep() {
        if (draft.steps.length >= MAX_STEPS) return;
        const n = draft.steps.length;
        const template = templateOptions[Math.min(n, templateOptions.length - 1)]?.value ?? "";
        const action = actionOptions.find((a) => !a.disabled)?.value ?? "";
        draft.steps = [...draft.steps, { _key: ++keySeq, value: 1, unit: "days", action, template }];
    }

    function removeStep(index) {
        draft.steps = draft.steps.filter((_, i) => i !== index);
        errors = { ...errors, steps: {} };
    }

    function moveStep(index, by) {
        const to = index + by;
        if (to < 0 || to >= draft.steps.length) return;
        const next = [...draft.steps];
        [next[index], next[to]] = [next[to], next[index]];
        draft.steps = next;
        errors = { ...errors, steps: {} };
    }

    const readOnlyNote = `Your role can see this automation but not change it: that needs “${rightLabel("abandonment.automate")}”.`;
</script>

<svelte:head><title>{words.title} · GoCommerce</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="the marketing automations" />
{:else}
    <div class="page page-recovery-automation shopify-skin skin-recessed recovery-editor">
        <div class="page-content full-height">
            <SaveBar {dirty} {saving} onsave={save} ondiscard={discard} />

            <header class="page-header">
                <nav class="breadcrumbs">
                    <a href="{base}/dash/marketing/automations">Automations</a>
                    <div>{words.title}</div>
                </nav>
                {#if view && draft}
                    <div class="page-header-primary-btns">
                        <span class="recovery-state" class:is-on={draft.enabled}>
                            <span class="recovery-dot {draft.enabled ? 'success' : 'neutral'}" aria-hidden="true"></span>
                            {draft.enabled ? "On" : "Off"}
                        </span>
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
                <div class="wrapper recovery-page">
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

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-flow-chart" aria-hidden="true"></i>
                            {words.title}
                        </h6>
                        <p class="txt-hint m-b-sm">{words.what}</p>
                        <div class="field">
                            <input
                                type="checkbox"
                                id="recovery-enabled-{kind}"
                                class="switch"
                                disabled={!writable}
                                bind:checked={draft.enabled}
                            />
                            <label for="recovery-enabled-{kind}">
                                {draft.enabled ? "On — the sequence below runs" : "Off — nothing is sent"}
                            </label>
                        </div>
                        <div class="field-help">
                            Switching it off stops every pending reminder the moment you save; switching it on
                            schedules them again from each basket's last message.
                        </div>
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-timer-line" aria-hidden="true"></i>
                            When a {words.noun} counts as abandoned
                        </h6>
                        <div class="recovery-sentence">
                            <span>A {words.noun} counts as abandoned after</span>
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
                            <span>without activity.</span>
                        </div>
                        {#if errors.threshold}
                            <div class="field-help error" transition:rise>{errors.threshold}</div>
                        {:else}
                            <div class="field-help">
                                Any change to the basket — an item added, an address typed — starts the clock again. A
                                basket abandoned is recorded whether or not this automation is on.
                            </div>
                        {/if}
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-mail-send-line" aria-hidden="true"></i>
                            The sequence
                        </h6>

                        {#if draft.steps.length}
                            <ol class="recovery-steps">
                                {#each draft.steps as step, i (step._key)}
                                    <li
                                        class="recovery-step"
                                        class:has-error={!!errors.steps?.[i]}
                                        animate:flip={{ duration: flipDuration() }}
                                        transition:rise
                                    >
                                        <span class="recovery-step-n" aria-hidden="true">{i + 1}</span>
                                        <div class="recovery-step-main">
                                            <div class="recovery-step-row">
                                                <span class="recovery-step-word">Wait</span>
                                                <div class="field recovery-num" class:error={!!errors.steps?.[i]}>
                                                    <input
                                                        type="number"
                                                        min="1"
                                                        step="1"
                                                        inputmode="numeric"
                                                        aria-label="Wait before step {i + 1}"
                                                        disabled={!writable}
                                                        bind:value={step.value}
                                                    />
                                                </div>
                                                <div class="field recovery-unit">
                                                    <Select
                                                        ariaLabel="Wait unit for step {i + 1}"
                                                        value={step.unit}
                                                        options={unitOptions}
                                                        disabled={!writable}
                                                        onchange={(v) => (step.unit = v)}
                                                    />
                                                </div>
                                                <span class="recovery-step-arrow" aria-hidden="true">→</span>
                                                <div class="field recovery-action">
                                                    <Select
                                                        ariaLabel="Action for step {i + 1}"
                                                        value={step.action}
                                                        options={actionOptions}
                                                        disabled={!writable}
                                                        onchange={(v) => (step.action = v)}
                                                    />
                                                </div>
                                                <span class="recovery-step-word">using</span>
                                                <div class="field recovery-template">
                                                    <Select
                                                        ariaLabel="Message for step {i + 1}"
                                                        value={step.template}
                                                        options={templateOptions}
                                                        disabled={!writable}
                                                        onchange={(v) => (step.template = v)}
                                                    />
                                                </div>
                                            </div>
                                            <div class="recovery-step-foot">
                                                {#if errors.steps?.[i]}
                                                    <span class="field-help error" transition:rise>{errors.steps[i]}</span>
                                                {:else}
                                                    <span class="txt-hint txt-sm">{whenWords(i)}</span>
                                                {/if}
                                                <!-- The words the step sends, one click away: the
                                                     template editor opens on this message. -->
                                                <a
                                                    class="recovery-arrow-link recovery-step-edit"
                                                    href="{base}/dash/notifications/email?template={encodeURIComponent(step.template)}"
                                                >
                                                    Edit wording <span aria-hidden="true">→</span>
                                                </a>
                                            </div>
                                        </div>
                                        {#if writable}
                                            <div class="recovery-step-tools">
                                                <button
                                                    type="button"
                                                    class="btn circle sm transparent secondary"
                                                    aria-label="Move step {i + 1} up"
                                                    title="Move up"
                                                    disabled={i === 0}
                                                    onclick={() => moveStep(i, -1)}
                                                >
                                                    <i class="ri-arrow-up-line" aria-hidden="true"></i>
                                                </button>
                                                <button
                                                    type="button"
                                                    class="btn circle sm transparent secondary"
                                                    aria-label="Move step {i + 1} down"
                                                    title="Move down"
                                                    disabled={i === draft.steps.length - 1}
                                                    onclick={() => moveStep(i, 1)}
                                                >
                                                    <i class="ri-arrow-down-line" aria-hidden="true"></i>
                                                </button>
                                                <button
                                                    type="button"
                                                    class="btn circle sm transparent secondary"
                                                    aria-label="Remove step {i + 1}"
                                                    title="Remove"
                                                    onclick={() => removeStep(i)}
                                                >
                                                    <i class="ri-close-line" aria-hidden="true"></i>
                                                </button>
                                            </div>
                                        {/if}
                                    </li>
                                {/each}
                            </ol>
                        {:else}
                            <p class="txt-hint recovery-no-steps" in:rise>
                                No steps. Baskets are still recorded as abandoned, and nothing is sent about them.
                            </p>
                        {/if}

                        {#if writable}
                            <div class="recovery-add-step">
                                <button
                                    type="button"
                                    class="btn sm secondary"
                                    disabled={draft.steps.length >= MAX_STEPS}
                                    title={draft.steps.length >= MAX_STEPS ? `At most ${MAX_STEPS} steps` : undefined}
                                    onclick={addStep}
                                >
                                    <i class="ri-add-line" aria-hidden="true"></i>
                                    <span class="txt">Add step</span>
                                </button>
                                <span class="txt-hint txt-sm">{draft.steps.length} of {MAX_STEPS}</span>
                            </div>
                        {/if}
                        <div class="field-help">
                            Each wait runs from the step before it. A manual send from a basket's page does not move
                            the sequence on.
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

                    <section class="card recovery-guards">
                        <h6 class="section-title">
                            <i class="ri-shield-check-line" aria-hidden="true"></i>
                            A reminder is only sent if
                            <span class="label sm">For information</span>
                        </h6>
                        <ul class="recovery-guard-list">
                            {#each view.guards as guard (guard)}
                                <li><i class="ri-check-line" aria-hidden="true"></i>{guard}</li>
                            {/each}
                        </ul>
                        <div class="field-help">
                            Checked again at the moment of each send, against the basket as it is then. These are
                            built in and cannot be switched off.
                        </div>
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
