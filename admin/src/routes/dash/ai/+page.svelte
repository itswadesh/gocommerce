<script>
    /**
     * AI › Usage: what the models cost this month against the cap, and every
     * run behind that figure.
     *
     * The head of the screen answers the owner's two questions before any
     * table: can this store make calls at all (and if not, the reason, with a
     * way to the settings), and how much of the month's cap is gone. The cap
     * bar shows what is held for calls in flight beside what is spent, because
     * a reservation is money the gateway will not let anything else use.
     *
     * Costs come back in millionths of a minor unit and are formatted here
     * (lib/ai.js): a run is usually a fraction of a cent, which a price
     * formatter would print as nothing.
     *
     * Someone who may decide proposals but not read usage is sent on to the
     * queue, since this is where the AI section's own link lands.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney, relativeTime } from "$lib/format.js";
    import { rise } from "$lib/motion.js";
    import {
        formatMicro,
        formatTokens,
        outcomeLabel,
        outcomeClass,
        OUTCOME_OPTIONS,
        POLICY_CLASSES,
        stepName,
        stepOutcomeLabel,
        stepOk,
    } from "$lib/ai.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const API = "/api/admin/x/ai-gateway";
    const readable = $derived(can("ai.read"));
    // Granting AI credit and setting the free trial are the platform operator's,
    // gated on the operator-level right store owners hold but managers do not.
    const operator = $derived(can("store.operate"));
    const missing = $derived(modulesKnown() && !hasModule("ai-gateway"));
    const ready = $derived(readable && modulesKnown() && hasModule("ai-gateway"));

    $effect(() => {
        if (!readable && can("ai.approve")) goto(base + "/dash/ai/approvals", { replaceState: true });
    });

    const list = listState({ outcome: "", page: 1, limit: 20 });
    const perPage = $derived(list.params.limit);

    let budget = $state(null);
    let credit = $state(null);
    let ledger = $state(null);
    let ops = $state(false); // the operator's grant / free-trial panel is open
    let grantMinor = $state("");
    let freeMinor = $state("");
    let saving = $state("");
    let days = $state(null);
    let byTask = $state(null);
    let byModel = $state(null);
    let summaryLoading = $state(true);
    let runs = $state([]);
    let meta = $state(null);
    let runsLoading = $state(true);
    let checking = $state(false);
    let check = $state(null);
    let shown = $state(null);
    let runOpen = $state(false);
    let reqId = 0;

    $effect(() => {
        if (ready) loadSummary();
    });
    $effect(() => {
        list.params;
        if (ready) loadRuns();
    });

    async function loadSummary() {
        summaryLoading = true;
        try {
            const [b, c, d, t, m] = await Promise.all([
                api.get(`${API}/budget`),
                api.get(`${API}/credit`),
                api.get(`${API}/usage?by=day`),
                api.get(`${API}/usage?by=task`),
                api.get(`${API}/usage?by=model`),
            ]);
            budget = b;
            credit = c;
            days = d;
            byTask = t;
            byModel = m;
        } catch (err) {
            toast.error(err);
        } finally {
            summaryLoading = false;
        }
        if (operator) loadLedger();
    }

    async function loadLedger() {
        try {
            const res = await api.get(`${API}/credit/ledger?limit=5`);
            ledger = res.data ?? [];
        } catch {
            // The ledger is the operator's extra; a store admin reading this
            // screen without the right simply does not see it.
            ledger = null;
        }
    }

    async function changeCredit(path, minor, what) {
        const amount = Math.round(Number(minor));
        if (!Number.isFinite(amount) || amount < 0 || (path === "grant" && amount <= 0)) {
            toast.error("Type the amount in minor units, a whole number.");
            return;
        }
        saving = path;
        try {
            credit = await api.post(`${API}/credit/${path}`, { amount_minor: amount });
            grantMinor = "";
            freeMinor = "";
            toast.success(what);
            loadLedger();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = "";
        }
    }

    async function loadRuns() {
        const mine = ++reqId;
        runsLoading = true;
        try {
            const p = list.params;
            const result = await api.get(`${API}/executions` + query({ outcome: p.outcome, page: p.page, limit: p.limit }));
            if (mine !== reqId) return;
            runs = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) runsLoading = false;
        }
    }

    function refresh() {
        loadSummary();
        loadRuns();
    }

    async function runCheck() {
        checking = true;
        check = null;
        try {
            check = await api.post(`${API}/check`);
        } catch (err) {
            toast.error(err);
        } finally {
            checking = false;
        }
    }

    async function openRun(run) {
        shown = run;
        runOpen = true;
        try {
            shown = await api.get(`${API}/executions/${run.id}`);
        } catch (err) {
            toast.error(err);
        }
    }

    const currency = $derived(budget?.currency ?? days?.currency ?? "USD");
    const capMicro = $derived(budget?.cap_minor != null ? budget.cap_minor * 1e6 : null);
    const spentShare = $derived(capMicro ? Math.min(1, (budget?.spent_micro_minor ?? 0) / capMicro) : 0);
    const heldShare = $derived(capMicro ? Math.min(1 - spentShare, (budget?.reserved_micro_minor ?? 0) / capMicro) : 0);
    /* A month's first few cents are well under one per cent of any cap, and
       "0%" beside a figure that is plainly not nothing reads as a bug. */
    const capShare = $derived(
        spentShare > 0 && spentShare < 0.01 ? "Under 1%" : `${Math.round(spentShare * 100)}%`,
    );
    const peak = $derived((days?.rows ?? []).reduce((top, r) => Math.max(top, r.cost_micro_minor), 0));

    // The credit meter: how much of the free trial plus the topped-up balance
    // has been spent, drawn from the left like the cap's.
    const creditTotal = $derived((credit?.free_allowance_micro_minor ?? 0) + (credit?.granted_micro_minor ?? 0));
    const consumedShare = $derived(creditTotal ? Math.min(1, (credit?.consumed_micro_minor ?? 0) / creditTotal) : 0);
    const creditHeldShare = $derived(
        creditTotal ? Math.min(1 - consumedShare, (credit?.reserved_micro_minor ?? 0) / creditTotal) : 0,
    );

    function monthName(iso) {
        if (!iso) return "";
        const [y, m] = iso.split("-").map(Number);
        return new Intl.DateTimeFormat(undefined, { month: "long", year: "numeric", timeZone: "UTC" }).format(
            new Date(Date.UTC(y, m - 1, 1)),
        );
    }
    function dayName(iso) {
        const [y, m, d] = iso.split("-").map(Number);
        return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", timeZone: "UTC" }).format(
            new Date(Date.UTC(y, m - 1, d)),
        );
    }
    const busiest = $derived(
        (days?.rows ?? []).reduce((top, r) => (r.cost_micro_minor > (top?.cost_micro_minor ?? 0) ? r : top), null),
    );
    const chartLabel = $derived(
        days
            ? `Spend by day, ${dayName(days.from)} to ${dayName(days.to)}: ${formatMicro(days.total.cost_micro_minor, currency)} in all` +
                  (busiest ? `, most on ${dayName(busiest.key)} at ${formatMicro(busiest.cost_micro_minor, currency)}.` : ".")
            : "",
    );
</script>

<svelte:head><title>{titled("AI usage")}</title></svelte:head>

{#if !readable}
    <NoAccess right="ai.read" what="AI usage" />
{:else}
    <div class="page page-ai shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">AI</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Usage</div>
                </nav>
                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        disabled={summaryLoading || runsLoading}
                        onclick={refresh}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
                {#if !missing}
                    <div class="page-header-primary-btns">
                        {#if can("plugins.read")}
                            <a class="btn secondary" href="{base}/dash/plugins?open=ai-gateway" title="The AI gateway's settings" aria-label="Settings">
                                <i class="ri-settings-3-line" aria-hidden="true"></i>
                                <span class="txt">Settings</span>
                            </a>
                        {/if}
                        <!-- The platform's key is the install's to check, not a store's. -->
                        {#if can("ai.configure") && budget?.key_source !== "platform"}
                            <button
                                type="button"
                                class="btn"
                                class:loading={checking}
                                disabled={checking}
                                title="Ask the provider whether the key works; no tokens are spent"
                                aria-label="Check connection"
                                onclick={runCheck}
                            >
                                <i class="ri-plug-line" aria-hidden="true"></i>
                                <span class="txt">Check connection</span>
                            </button>
                        {/if}
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="ai-gateway" what="AI usage is served by ext/ai-gateway, and this binary does not have it." />
            {:else}
                <div aria-live="polite">
                    {#if check}
                        <div class="alert {check.ok ? 'success' : 'danger'} ai-check m-b-base" transition:rise>
                            <i class={check.ok ? "ri-checkbox-circle-line" : "ri-error-warning-line"} aria-hidden="true"></i>
                            <div class="flex-fill">
                                <strong>{check.ok ? "The key works." : "The connection did not work."}</strong>
                                {check.message}
                            </div>
                            <button
                                type="button"
                                class="btn circle sm transparent secondary"
                                aria-label="Dismiss"
                                onclick={() => (check = null)}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </div>
                    {/if}
                </div>

                {#if budget && !budget.ready}
                    <div class="alert warning m-b-base" transition:rise>
                        <i class="ri-pause-circle-line" aria-hidden="true"></i>
                        <div class="flex-fill">
                            <strong>No model is called for this store yet.</strong>
                            {budget.reason.charAt(0).toUpperCase() + budget.reason.slice(1)}.
                        </div>
                    </div>
                {/if}

                {#if credit}
                    <section class="ai-credit m-b-base" class:is-exhausted={credit.exhausted} transition:rise>
                        <div class="ai-credit-head">
                            <div class="ai-tile-label">AI credit</div>
                            {#if credit.exhausted}
                                <span class="label label-warning sm">Out of credit</span>
                            {/if}
                        </div>

                        {#if credit.exhausted}
                            <p class="ai-credit-msg" aria-live="polite">
                                <i class="ri-sparkling-2-line" aria-hidden="true"></i>
                                {credit.granted_micro_minor > 0
                                    ? "You're out of AI credit — top up to keep using AI."
                                    : "You've used your free AI trial — top up to keep using AI."}
                            </p>
                        {/if}

                        <div class="ai-credit-value">
                            {formatMicro(credit.available_micro_minor, currency)}
                            <span class="ai-tile-of">left to spend on AI</span>
                        </div>
                        {#if budget?.key_source === "own"}
                            <p class="ai-tile-hint">This store uses its own key, so its AI is billed by its provider and draws no credit.</p>
                        {/if}

                        <div
                            class="ai-meter ai-credit-meter"
                            role="meter"
                            aria-label="AI credit spent"
                            aria-valuemin="0"
                            aria-valuemax="100"
                            aria-valuenow={Math.round(consumedShare * 100)}
                        >
                            <span class="ai-meter-spent" style="transform: scaleX({consumedShare})"></span>
                            <span class="ai-meter-held" style="transform: translateX({consumedShare * 100}%) scaleX({creditHeldShare})"></span>
                        </div>

                        <dl class="ai-credit-figures">
                            <div>
                                <dt>Free trial left</dt>
                                <dd>{formatMicro(credit.free_remaining_micro_minor, currency)}</dd>
                            </div>
                            <div>
                                <dt>Topped-up balance</dt>
                                <dd>{formatMicro(credit.balance_remaining_micro_minor, currency)}</dd>
                            </div>
                            <div>
                                <dt>Spent so far</dt>
                                <dd>{formatMicro(credit.consumed_micro_minor, currency)}</dd>
                            </div>
                        </dl>

                        {#if operator}
                            <div class="ai-credit-ops">
                                <button
                                    type="button"
                                    class="btn sm secondary"
                                    aria-expanded={ops}
                                    onclick={() => (ops = !ops)}
                                >
                                    <i class={ops ? "ri-arrow-up-s-line" : "ri-add-line"} aria-hidden="true"></i>
                                    <span class="txt">{ops ? "Close" : "Top up or set the free trial"}</span>
                                </button>

                                {#if ops}
                                    <div class="ai-credit-forms" transition:rise>
                                        <form class="ai-credit-form" onsubmit={(e) => { e.preventDefault(); changeCredit("grant", grantMinor, "Credit topped up"); }}>
                                            <label class="ai-tile-label" for="ai-grant">Top up the balance</label>
                                            <div class="inline-flex gap-sm">
                                                <input id="ai-grant" class="txt-input" type="number" min="1" step="1" inputmode="numeric"
                                                    placeholder="Minor units, e.g. 2500 = {formatMoney({ amount_minor: 2500, currency })}"
                                                    bind:value={grantMinor} />
                                                <button type="submit" class="btn sm" class:loading={saving === "grant"} disabled={saving === "grant" || !grantMinor}>
                                                    <span class="txt">Add</span>
                                                </button>
                                            </div>
                                        </form>
                                        <form class="ai-credit-form" onsubmit={(e) => { e.preventDefault(); changeCredit("free-allowance", freeMinor, "Free trial set"); }}>
                                            <label class="ai-tile-label" for="ai-free">Set the free trial</label>
                                            <div class="inline-flex gap-sm">
                                                <input id="ai-free" class="txt-input" type="number" min="0" step="1" inputmode="numeric"
                                                    placeholder="Minor units ({formatMicro(credit.free_allowance_micro_minor, currency)} now)"
                                                    bind:value={freeMinor} />
                                                <button type="submit" class="btn sm secondary" class:loading={saving === "free-allowance"} disabled={saving === "free-allowance" || freeMinor === ""}>
                                                    <span class="txt">Set</span>
                                                </button>
                                            </div>
                                        </form>
                                        <p class="ai-tile-hint">The real figures are yours to set; there is no pricing or self-serve purchase yet. Amounts are minor units of the store's currency.</p>
                                        {#if ledger && ledger.length}
                                            <ul class="ai-credit-log">
                                                {#each ledger as e (e.id)}
                                                    <li>
                                                        <span>{e.kind === "grant" ? "Topped up" : "Free trial set to"} {formatMoney({ amount_minor: e.amount_minor, currency })}</span>
                                                        <span class="txt-hint txt-sm">{e.actor_email || "an operator"} · {relativeTime(e.created_at)}</span>
                                                    </li>
                                                {/each}
                                            </ul>
                                        {/if}
                                    </div>
                                {/if}
                            </div>
                        {/if}
                    </section>
                {/if}

                <div class="ai-tiles m-b-base">
                    <section class="ai-tile ai-tile-wide">
                        <div class="ai-tile-label">Spent in {monthName(budget?.month)}</div>
                        {#if summaryLoading && !budget}
                            <span class="skeleton-loader"></span>
                        {:else}
                            <div class="ai-tile-value">
                                {formatMicro(budget?.spent_micro_minor ?? 0, currency)}
                                <span class="ai-tile-of">
                                    of {budget?.cap_minor != null ? formatMoney({ amount_minor: budget.cap_minor, currency }) : "no cap set"}
                                </span>
                            </div>
                            <div
                                class="ai-meter"
                                role="meter"
                                aria-label="Spent against this month's cap"
                                aria-valuemin="0"
                                aria-valuemax="100"
                                aria-valuenow={Math.round(spentShare * 100)}
                            >
                                <span class="ai-meter-spent" style="transform: scaleX({spentShare})"></span>
                                <span
                                    class="ai-meter-held"
                                    style="transform: translateX({spentShare * 100}%) scaleX({heldShare})"
                                ></span>
                            </div>
                            <div class="ai-tile-hint">
                                {#if capMicro}
                                    {capShare} of the cap.
                                {/if}
                                {#if budget?.reserved_micro_minor}
                                    {formatMicro(budget.reserved_micro_minor, currency)} held for calls in flight.
                                {/if}
                                Work that would pass the cap waits for a person.
                            </div>
                        {/if}
                    </section>

                    <section class="ai-tile">
                        <div class="ai-tile-label">Runs this month</div>
                        <div class="ai-tile-value">{days ? formatTokens(days.total.runs) : "…"}</div>
                        <div class="ai-tile-hint">
                            {#if days}
                                {days.total.escalations} went to the strong model, {days.total.failures} ended without an answer.
                            {/if}
                        </div>
                    </section>

                    <section class="ai-tile">
                        <div class="ai-tile-label">Models</div>
                        {#if budget}
                            {#if budget.key_source === "platform"}
                                <p class="ai-key-source"><i class="ri-cloud-line" aria-hidden="true"></i> Using the platform's AI, paid from AI credit</p>
                            {:else if budget.key_source === "own"}
                                <p class="ai-key-source"><i class="ri-key-2-line" aria-hidden="true"></i> Using your own key, billed by your provider</p>
                            {/if}
                            <dl class="ai-models">
                                <dt>Provider</dt>
                                <dd>{budget.provider === "openai" ? "OpenAI" : "Anthropic"}</dd>
                                <dt>Small</dt>
                                <dd>
                                    <span class="txt-code">{budget.small_model}</span>
                                    {#if !budget.small_priced}<span class="label label-warning sm">no price</span>{/if}
                                </dd>
                                <dt>Strong</dt>
                                <dd>
                                    <span class="txt-code">{budget.strong_model}</span>
                                    {#if !budget.strong_priced}<span class="label label-warning sm">no price</span>{/if}
                                </dd>
                            </dl>
                        {/if}
                    </section>

                    <section class="ai-tile">
                        <div class="ai-tile-label">May leave the store</div>
                        {#if budget}
                            <ul class="ai-policy">
                                {#each POLICY_CLASSES as [key, label] (key)}
                                    <li class:on={budget.policy[key]}>
                                        <i
                                            class={budget.policy[key] ? "ri-check-line" : "ri-close-line"}
                                            aria-hidden="true"
                                        ></i>
                                        {label}<span class="tw:sr-only">: {budget.policy[key] ? "allowed" : "kept in"}</span>
                                    </li>
                                {/each}
                            </ul>
                        {/if}
                    </section>
                </div>

                <h2 class="section-title">Spend by day</h2>
                {#if days}
                    <figure class="ai-chart" role="img" aria-label={chartLabel}>
                        {#each days.rows as day (day.key)}
                            <div
                                class="ai-bar"
                                title="{dayName(day.key)}: {formatMicro(day.cost_micro_minor, currency)}, {day.runs} {day.runs === 1 ? 'run' : 'runs'}"
                            >
                                <span style="transform: scaleY({peak ? day.cost_micro_minor / peak : 0})"></span>
                            </div>
                        {/each}
                    </figure>
                    <div class="ai-axis">
                        <span>{dayName(days.from)}</span>
                        <span>{dayName(days.to)}</span>
                    </div>
                {:else}
                    <span class="skeleton-loader ai-chart-skeleton"></span>
                {/if}

                <div class="ai-pair m-t-base">
                    {#each [["By task", byTask, "Task"], ["By model", byModel, "Model"]] as [title, report, head] (title)}
                        <section>
                            <h2 class="section-title">{title}</h2>
                            <div class="page-table-wrapper tw:rounded-xl tw:border">
                                <table class="table responsive-table">
                                    <thead>
                                        <tr>
                                            <th>{head}</th>
                                            <th class="col-field-type-number min-width">Runs</th>
                                            <th class="col-field-type-number min-width">Cost</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {#each report?.rows ?? [] as row (row.key)}
                                            <tr>
                                                <td data-name={head}>
                                                    <span class="txt-code">{row.key || "no model (code or cache)"}</span>
                                                    {#if row.failures}
                                                        <span class="txt-hint txt-sm">· {row.failures} without an answer</span>
                                                    {/if}
                                                </td>
                                                <td class="col-field-type-number min-width" data-name="Runs">{formatTokens(row.runs)}</td>
                                                <td class="col-field-type-number min-width" data-name="Cost">
                                                    {formatMicro(row.cost_micro_minor, currency)}
                                                </td>
                                            </tr>
                                        {/each}
                                        {#if report && !report.rows.length}
                                            <tr class="empty-row">
                                                <td colspan="3" class="txt-hint txt-center p-base">No runs this month.</td>
                                            </tr>
                                        {/if}
                                    </tbody>
                                </table>
                            </div>
                        </section>
                    {/each}
                </div>

                <div class="ai-runs-head m-t-base">
                    <h2 class="section-title flex-fill">Recent runs</h2>
                    <div class="field ai-filter">
                        <Select
                            ariaLabel="Outcome"
                            value={list.params.outcome}
                            options={OUTCOME_OPTIONS}
                            onchange={(v) => list.set({ outcome: v })}
                        />
                    </div>
                </div>
                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={runsLoading && runs.length > 0}>
                    <table class="table responsive-table">
                        <thead>
                            <tr>
                                <th class="col-field-name-id">Run</th>
                                <th>Model</th>
                                <th class="min-width">Outcome</th>
                                <th class="col-field-type-number min-width">Tokens in / out</th>
                                <th class="col-field-type-number min-width">Cost</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if runsLoading && !runs.length}
                                {#each Array(3) as _, i (i)}
                                    <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each runs as run (run.id)}
                                <tr class="ai-row">
                                    <td class="col-field-name-id" data-name="Run">
                                        <button type="button" class="ai-row-open" onclick={() => openRun(run)}>
                                            <span class="txt-bold txt-code">{run.task}</span>
                                            <span class="txt-hint txt-sm" title={formatDate(run.created_at)}>
                                                #{run.id} · {relativeTime(run.created_at)}
                                            </span>
                                        </button>
                                    </td>
                                    <td data-name="Model"><span class="txt-code">{run.model || "—"}</span></td>
                                    <td class="min-width" data-name="Outcome">
                                        <span class="label {outcomeClass(run.outcome)}">
                                            {#if outcomeClass(run.outcome) === "label-success"}
                                                <i class="ri-check-line state-tick" aria-hidden="true"></i>
                                            {/if}
                                            {outcomeLabel(run.outcome)}
                                        </span>
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Tokens">
                                        {formatTokens(run.usage.input_tokens + run.usage.cache_read_tokens + run.usage.cache_write_tokens)}
                                        / {formatTokens(run.usage.output_tokens)}
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Cost">
                                        {formatMicro(run.cost_micro_minor, run.currency)}
                                        {#if run.cost_estimated}
                                            <span class="txt-hint txt-sm" title="A call was sent and reported nothing back, so it is charged at the most it could cost">estimated</span>
                                        {/if}
                                    </td>
                                </tr>
                            {/each}
                            {#if !runsLoading && !runs.length}
                                <tr class="empty-row">
                                    <td colspan="5">
                                        {#if list.params.outcome}
                                            <EmptyState
                                                icon="ri-filter-off-line"
                                                title="No runs ended this way"
                                                text="Try another outcome, or clear it to see every run."
                                                onclear={() => list.clear()}
                                            />
                                        {:else}
                                            <EmptyState
                                                icon="ri-sparkling-2-line"
                                                title="No model has been called yet"
                                                text="Every call this store makes is listed here with what it cost, once the gateway is switched on and a task asks for one."
                                            />
                                        {/if}
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <Pager
                        {meta}
                        loading={runsLoading}
                        noun="run"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <Drawer open={runOpen} title={shown ? `Run #${shown.id}: ${shown.task}` : "Run"} onclose={() => (runOpen = false)}>
        {#if shown}
            <div class="ai-run">
                <div class="ai-run-head">
                    <span class="label {outcomeClass(shown.outcome)}">{outcomeLabel(shown.outcome)}</span>
                    <span class="txt-hint txt-sm">{formatDate(shown.created_at)}</span>
                </div>
                <dl class="ai-facts">
                    <dt>Cost</dt>
                    <dd>
                        {formatMicro(shown.cost_micro_minor, shown.currency)}
                        {#if shown.cost_estimated}
                            <span class="txt-hint">estimated: a call was sent and reported nothing back, so it is charged at the most it could cost</span>
                        {/if}
                    </dd>
                    <dt>Tokens</dt>
                    <dd>
                        {formatTokens(shown.usage.input_tokens)} in, {formatTokens(shown.usage.output_tokens)} out,
                        {formatTokens(shown.usage.cache_read_tokens)} read from the cache,
                        {formatTokens(shown.usage.cache_write_tokens)} written to it
                    </dd>
                    <dt>Time</dt>
                    <dd>{(shown.latency_ms / 1000).toFixed(1)} s with the provider</dd>
                    <dt>Model</dt>
                    <dd><span class="txt-code">{shown.model_version || shown.model || "none"}</span></dd>
                    {#if shown.confidence != null}
                        <dt>Confidence</dt>
                        <dd>{shown.confidence} of 100</dd>
                    {/if}
                    <dt>Trace</dt>
                    <dd><span class="txt-code txt-sm">{shown.trace_id || "—"}</span></dd>
                    {#if shown.job_id}
                        <dt>Job</dt>
                        <dd><a href="{base}/dash/ai/jobs">#{shown.job_id}</a></dd>
                    {/if}
                </dl>

                {#if shown.steps}
                    <h6 class="section-title">What it tried</h6>
                    <ol class="ai-steps">
                        {#each shown.steps as step, i (i)}
                            <li class:ok={stepOk(step.outcome)} in:rise={{ delay: Math.min(i, 6) * 40 }}>
                                <i
                                    class={stepOk(step.outcome) ? "ri-checkbox-circle-line" : "ri-arrow-down-circle-line"}
                                    aria-hidden="true"
                                ></i>
                                <div>
                                    <div>
                                        <strong>{stepName(step.step)}</strong>
                                        {#if step.model}<span class="txt-code txt-sm">{step.model}</span>{/if}
                                        — {stepOutcomeLabel(step.outcome)}
                                    </div>
                                    {#if step.reason}<div class="txt-hint txt-sm">{step.reason}</div>{/if}
                                    {#if step.usage}
                                        <div class="txt-hint txt-sm">
                                            {formatTokens(step.usage.input_tokens + step.usage.cache_read_tokens + step.usage.cache_write_tokens)} in,
                                            {formatTokens(step.usage.output_tokens)} out ·
                                            {formatMicro(step.cost_micro_minor ?? 0, shown.currency)}{step.estimated ? " (estimated)" : ""} ·
                                            {((step.latency_ms ?? 0) / 1000).toFixed(1)} s
                                        </div>
                                    {/if}
                                    {#if step.dropped?.length}
                                        <div class="txt-hint txt-sm">
                                            Left out by the store's policy: {step.dropped.join(", ").replaceAll("_", " ")}
                                        </div>
                                    {/if}
                                </div>
                            </li>
                        {/each}
                    </ol>

                    <h6 class="section-title">What was allowed to leave</h6>
                    <ul class="ai-policy">
                        {#each POLICY_CLASSES as [key, label] (key)}
                            <li class:on={shown.policy?.[key]}>
                                <i class={shown.policy?.[key] ? "ri-check-line" : "ri-close-line"} aria-hidden="true"></i>
                                {label}<span class="tw:sr-only">: {shown.policy?.[key] ? "allowed" : "kept in"}</span>
                            </li>
                        {/each}
                    </ul>
                    {#if shown.source_refs?.length}
                        <h6 class="section-title">What it read</h6>
                        <ul class="ai-sources">
                            {#each shown.source_refs as ref, i (i)}
                                <li><span class="txt-code">{ref.kind.replaceAll("_", " ")} {ref.id}</span></li>
                            {/each}
                        </ul>
                    {/if}
                {:else}
                    <span class="skeleton-loader"></span>
                {/if}
            </div>
        {/if}
    </Drawer>
{/if}
