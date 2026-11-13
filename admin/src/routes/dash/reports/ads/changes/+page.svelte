<script>
    /*
     * Ad changes (D268): what the engine proposes to change in the connected
     * Google Ads and Meta accounts, each waiting for somebody with ads.approve.
     * Approving makes the change on the platform; every attempt is listed with
     * what was sent and what the platform answered.
     */
    import { base } from "$app/paths";
    import { slide, fade } from "svelte/transition";
    import { ads, can } from "$lib/api.js";
    import { formatDate, formatMoney, pluralize } from "$lib/format.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const readable = $derived(can("ads.read"));
    const writable = $derived(can("ads.write"));
    const approver = $derived(can("ads.approve"));
    const missing = $derived(modulesKnown() && !hasModule("ads") && readable);

    const still = typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    const ms = (n) => (still ? 0 : n);

    const PLATFORM = { google: "Google Ads", meta: "Meta" };
    const STATUS = {
        proposed: ["Waiting", "watch"],
        approved: ["Being made", "watch"],
        failed: ["Failed", "wasting"],
        applied: ["Made", "working"],
        dismissed: ["Dismissed", "watch"],
        undone: ["Undone", "watch"],
    };

    let view = $state(null);
    let loading = $state(true);
    let busy = $state(""); // the id, "find" or "all" being worked on

    const waiting = $derived((view?.changes ?? []).filter((c) => ["proposed", "failed", "approved"].includes(c.status)));
    const history = $derived((view?.changes ?? []).filter((c) => !["proposed", "failed", "approved"].includes(c.status)));
    const proposed = $derived(waiting.filter((c) => c.status === "proposed").length);

    $effect(() => {
        if (readable && hasModule("ads")) load();
        else loading = false;
    });

    async function load() {
        loading = true;
        try {
            view = await ads.changes();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function run(key, fn) {
        busy = key;
        try {
            return await fn();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    const find = () =>
        run("find", async () => {
            view = await ads.propose();
            toast.success(proposed ? `${pluralize(proposed, "change")} waiting.` : "Nothing to change on this reading.");
        });

    const approveAll = () =>
        run("all", async () => {
            view = await ads.approveAll();
            const failed = view.changes.filter((c) => c.status === "failed").length;
            if (failed) toast.error(`${pluralize(failed, "change")} failed: see each one's reason.`);
            else toast.success("All changes made.");
        });

    function act(c, fn, done) {
        return run(c.id, async () => {
            const next = await fn(c.id);
            if (next.status === "failed") toast.error(next.log.at(-1)?.error || "The platform refused the change.");
            else toast.success(done);
            await load();
        });
    }

    const pause = () =>
        run("pause", async () => {
            view = await ads.pauseAutopilot();
            toast.success("Autopilot paused. Nothing more is changed alone.");
        });

    const ACTION = {
        budget_down: "lower a budget",
        negative_keywords: "add negative keywords",
        exclude_placements: "exclude placements",
        pause: "pause a campaign",
        budget_up: "raise a budget",
        ad_schedule: "set an ad schedule",
    };
    const pilot = $derived(view?.autopilot);

    const lastError = (c) => [...c.log].reverse().find((l) => !l.ok)?.error;
</script>

<svelte:head><title>{titled("Ad changes")}</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="ads.read" what="the ad changes" />
{:else}
    <div class="page page-ads shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <div class="recovery-title-block">
                    <a class="txt-hint txt-sm" href="{base}/dash/reports/ads"><i class="ri-arrow-left-line" aria-hidden="true"></i> Ads performance</a>
                    <h1 class="recovery-h1">Ad changes</h1>
                </div>
                <div class="flex-fill"></div>
                {#if view?.connected.length}
                    <div class="page-header-primary-btns">
                        {#if writable}
                            <button type="button" class="btn sm secondary ads-press" class:btn-loading={busy === "find"} disabled={!!busy} onclick={find} title="Find changes" aria-label="Find changes">
                                <i class="ri-search-eye-line" aria-hidden="true"></i>
                                <span class="txt">Find changes</span>
                            </button>
                        {/if}
                        {#if approver && proposed}
                            <button type="button" class="btn sm ads-press" class:btn-loading={busy === "all"} disabled={!!busy} onclick={approveAll}>
                                <span class="txt">Approve all ({proposed})</span>
                            </button>
                        {/if}
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="ads" what="Ad changes are ext/ads's, and this binary does not have it." />
            {:else if loading && !view}
                <span class="skeleton-loader" style="height: 180px"></span>
            {:else if view}
                <p class="txt-hint txt-sm">
                    Changes to your campaigns, budgets and keywords, worked out from the ads report and the platform's own figures.
                    Nothing is changed in an ad account until it is approved here. These connections have not yet been run against
                    a real ad account, so check the first changes in the platform itself.
                </p>

                {#if !view.connected.length}
                    <div class="recovery-empty-block recovery-report-card" in:fade={{ duration: ms(200) }}>
                        <i class="ri-plug-line" aria-hidden="true"></i>
                        <p><strong>No ad account is connected</strong></p>
                        <p class="txt-hint">
                            Nothing is proposed or changed until Google Ads or Meta is connected with an account that may make changes.
                        </p>
                        <p>
                            <a class="btn sm secondary ads-press" href="{base}/dash/plugins?open=google-ads-spend">Google Ads settings</a>
                            <a class="btn sm secondary ads-press" href="{base}/dash/plugins?open=meta-ads-spend">Meta settings</a>
                        </p>
                    </div>
                {/if}

                {#if pilot}
                    <section class="recovery-report-card" aria-labelledby="ads-pilot-title">
                        <div class="ads-pace-head">
                            <h2 class="recovery-report-title" id="ads-pilot-title"><i class="ri-steering-2-line" aria-hidden="true"></i> Autopilot</h2>
                            {#key pilot.enabled}
                                <span class="ads-kind is-{pilot.enabled ? 'working' : 'watch'}" in:fade={{ duration: ms(180) }}>{pilot.enabled ? "On" : "Off"}</span>
                            {/key}
                            <span class="flex-fill"></span>
                            {#if approver && pilot.enabled}
                                <button type="button" class="btn sm danger ads-press" class:btn-loading={busy === "pause"} disabled={!!busy} onclick={pause}>
                                    <i class="ri-pause-circle-line" aria-hidden="true"></i>
                                    <span class="txt">Pause autopilot</span>
                                </button>
                            {/if}
                            <a class="btn sm transparent secondary ads-press" href="{base}/dash/plugins?open=ads-autopilot">Settings</a>
                        </div>
                        <p class="txt-hint txt-sm">
                            {#if pilot.enabled}
                                Each day it makes the waiting changes that it may {pilot.actions.map((a) => ACTION[a] ?? a).join(", ") || "(none ticked)"},
                                no budget move over {pilot.max_budget_change_pct}%, one change per campaign every {pilot.cooling_hours} hours,
                                {pilot.daily_increase_cap ? `raises of at most ${pilot.daily_increase_cap} a day in the account's currency` : "no raises"},
                                and never past the month's ad budget. It emails {pilot.notify_email} each change; everything else waits here.
                            {:else}
                                Off: every change waits for approval here. Switched on, it makes only the changes inside the limits you set, and
                                emails you each one.
                            {/if}
                        </p>
                    </section>
                {/if}

                {#if view.connected.length || waiting.length}
                    <section class="recovery-report-card" aria-labelledby="ads-waiting-title">
                        <h2 class="recovery-report-title" id="ads-waiting-title">Waiting for approval</h2>
                        {#if !waiting.length}
                            <p class="txt-hint txt-sm">Nothing is waiting. The platforms are read again each day.</p>
                        {:else}
                            <ul class="ads-points">
                                {#each waiting as c (c.id)}
                                    <li class="ads-point" transition:slide={{ duration: ms(220) }}>
                                        <div>
                                            <span class="ads-kind is-{STATUS[c.status][1]}">{STATUS[c.status][0]}</span>
                                            <span class="txt-hint txt-sm">{PLATFORM[c.platform]} · {c.target_kind === "ad_set" ? "ad set" : "campaign"} {c.target_name}</span>
                                            <p><strong>{c.summary}</strong></p>
                                            <p class="txt-sm">{c.reason}</p>
                                            {#if c.items.length}
                                                <ul class="ads-items">
                                                    {#each c.items as item (item)}<li>{item}</li>{/each}
                                                </ul>
                                            {/if}
                                            {#if c.status === "failed"}
                                                <p class="ads-problem txt-sm" role="status">{lastError(c)}</p>
                                            {/if}
                                            {#if approver}
                                                <div class="ads-change-btns">
                                                    <button type="button" class="btn sm ads-press" class:btn-loading={busy === c.id} disabled={!!busy} onclick={() => act(c, ads.approve, "Change made.")}>
                                                        {c.status === "failed" ? "Try again" : "Approve"}
                                                    </button>
                                                    <button type="button" class="btn sm transparent secondary ads-press" disabled={!!busy} onclick={() => act(c, ads.dismiss, "Dismissed.")}>Dismiss</button>
                                                </div>
                                            {/if}
                                        </div>
                                        <dl class="ads-figures">
                                            {#if c.budget_before}
                                                <div><dt>Daily budget now</dt><dd>{formatMoney(c.budget_before)}</dd></div>
                                                <div><dt>After</dt><dd>{formatMoney(c.budget_after)}</dd></div>
                                            {:else if c.before || c.after}
                                                <div><dt>Now</dt><dd>{c.before}</dd></div>
                                                <div><dt>After</dt><dd>{c.after}</dd></div>
                                            {/if}
                                            <div><dt>Rests on</dt><dd>{c.figure}</dd></div>
                                        </dl>
                                    </li>
                                {/each}
                            </ul>
                            {#if !approver}
                                <p class="txt-hint txt-sm tw:mt-2">Only somebody with the right to approve ad changes can make these; the owner has it.</p>
                            {/if}
                        {/if}
                    </section>
                {/if}

                {#if history.length}
                    <section class="recovery-report-card" aria-labelledby="ads-history-title">
                        <h2 class="recovery-report-title" id="ads-history-title">Decided</h2>
                        <ul class="ads-points">
                            {#each history as c (c.id)}
                                <li class="ads-point" in:fade={{ duration: ms(200) }}>
                                    <div>
                                        <span class="ads-kind is-{STATUS[c.status][1]}">{STATUS[c.status][0]}</span>
                                        <span class="txt-hint txt-sm">{PLATFORM[c.platform]} · {c.target_name}</span>
                                        <p><strong>{c.summary}</strong></p>
                                        <p class="txt-hint txt-sm">{c.decided_by}{c.decided_at ? `, ${formatDate(c.decided_at)}` : ""}</p>
                                        {#if c.log.length}
                                            <details class="ads-log">
                                                <summary>What was sent ({c.log.length})</summary>
                                                {#each c.log as l, i (i)}
                                                    <p>{l.kind === "undo" ? "Undo" : "Make"} by {l.actor}, {formatDate(l.at)}: {l.ok ? "done" : l.error}</p>
                                                    <pre>{l.request}</pre>
                                                    {#if l.response}<pre>{l.response}</pre>{/if}
                                                {/each}
                                            </details>
                                        {/if}
                                        {#if approver && c.undoable}
                                            <div class="ads-change-btns">
                                                <button type="button" class="btn sm secondary ads-press" class:btn-loading={busy === c.id} disabled={!!busy} onclick={() => act(c, ads.undo, "Undone.")}>Undo</button>
                                            </div>
                                        {/if}
                                    </div>
                                    <dl class="ads-figures">
                                        {#if c.budget_before}
                                            <div><dt>Before</dt><dd>{formatMoney(c.budget_before)}</dd></div>
                                            <div><dt>After</dt><dd>{formatMoney(c.budget_after)}</dd></div>
                                        {/if}
                                        <div><dt>Rested on</dt><dd>{c.figure}</dd></div>
                                    </dl>
                                </li>
                            {/each}
                        </ul>
                    </section>
                {/if}
            {/if}
        </div>
    </div>
{/if}
