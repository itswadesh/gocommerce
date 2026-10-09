<script>
    /**
     * The store's health, and the three passes that act on it.
     *
     * The screen is deliberately a renderer. Every row is a Diagnostic the
     * engine produced, in the engine's own order, and nothing here decides what
     * healthy means — `core/doctor.go` does, and a second opinion in the panel
     * is a second thing to keep true. It is the same report `gocommerce doctor`
     * prints and the MCP `store_health` tool returns.
     *
     * The buttons are the other half of the idea. Three of the doctor's hints
     * end in "check that background work is running", which is a diagnosis an
     * operator can read and be unable to act on; so the pass that answers a hint
     * sits on the row that reports it, and it is the same service method the
     * engine's own five-minute ticker calls.
     *
     * A failing check's underlying database error is not here. The engine keeps
     * it separately from the finding and the route strips it, because a pgx
     * message names a host, a port and a user and this is a browser session. It
     * is in the store's log.
     */
    import { can, ops } from "$lib/api.js";
    import { health } from "$lib/health.svelte.js";
    import { relativeTime, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";

    /* base.css has exactly these label variants; nothing here invents one. */
    const STATUS = {
        ok: { cls: "success", icon: "ri-checkbox-circle-line" },
        warn: { cls: "warning", icon: "ri-error-warning-line" },
        fail: { cls: "danger", icon: "ri-close-circle-line" },
    };

    /*
     * Keyed by the engine's own check names, which is the only place the two
     * vocabularies meet — and the reason core/ops_http_test.go pins those three
     * strings. Go cannot see this file, so renaming a check would otherwise
     * remove its button in silence.
     */
    const ACTIONS = {
        outbox: {
            label: "Deliver now",
            icon: "ri-send-plane-line",
            run: () => ops.drainOutbox(),
            done: (r) =>
                `${r.delivered} ${pluralize(r.delivered, "event")} delivered` +
                (r.capped ? " — more remain" : ""),
        },
        "stock reservations": {
            label: "Release now",
            icon: "ri-refresh-line",
            run: () => ops.sweepUnpaid(),
            done: (r) =>
                `${r.cancelled} ${pluralize(r.cancelled, "order")} cancelled` +
                (r.capped ? " — press again for the rest" : ""),
        },
        carts: {
            label: "Sweep now",
            icon: "ri-delete-bin-line",
            run: () => ops.sweepCarts(),
            done: (r) =>
                `${r.abandoned} ${pluralize(r.abandoned, "basket")} recorded, ` +
                `${r.removed} empty ${pluralize(r.removed, "cart")} removed` +
                (r.purged ? `, ${r.purged} purged` : "") +
                (r.capped ? " — press again for the rest" : ""),
        },
    };

    let busy = $state("");

    /*
     * The layout's watch() already fetched on sign-in, so asking again on every
     * visit would be two calls to a fifteen-check endpoint milliseconds apart.
     */
    $effect(() => {
        if (!can("store.operate")) return;
        if (!health.report && !health.loading) health.refresh();
    });

    async function run(name) {
        busy = name;
        try {
            toast.success(ACTIONS[name].done(await ACTIONS[name].run()));
            // The re-read is what makes a capped pass honest: the row re-states
            // itself from the engine rather than from what the button claimed.
            // A 409 from a concurrent drain arrives as the engine's own message.
            await health.refresh();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }
</script>

<svelte:head><title>Diagnostics · GoCommerce</title></svelte:head>

<div class="page page-diagnostics shopify-skin">

    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Diagnostics</div>
            </nav>

            <!-- Guarded with the screen: a refresh button on a report the role
                 may not read is a control that can only 403. -->
            {#if can("store.operate")}
                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        disabled={health.loading}
                        onclick={() => health.refresh()}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
            {/if}
        </header>

        {#if !can("store.operate")}
            <!-- The sidebar already hides the link; this is the direct URL.
                 Saying so before the request is better than a fully drawn screen
                 whose every control 403s, which reads as a broken panel. -->
            <NoAccess right="store.operate" what="the health report" />
        {:else if health.error}
            <div class="alert danger">
                <i class="ri-error-warning-line" aria-hidden="true"></i>
                <div class="content">
                    <p>Could not read the store's health.</p>
                    <p class="txt-sm">{health.error.message}</p>
                </div>
            </div>
        {:else if health.loading && !health.report}
            <div class="block txt-center"><span class="loader lg"></span></div>
        {:else if health.report}
            {#if health.failing}
                <div class="alert danger">
                    <i class="ri-close-circle-line" aria-hidden="true"></i>
                    <div class="content">
                        <p>Something is broken or about to be. The failing rows are below.</p>
                    </div>
                </div>
            {:else if health.warnings > 0}
                <div class="alert warning">
                    <i class="ri-error-warning-line" aria-hidden="true"></i>
                    <div class="content">
                        <p>
                            Nothing has failed, but {health.warnings}
                            {pluralize(health.warnings, "check")} of {health.checks.length}
                            {health.warnings === 1 ? "is" : "are"} worth a look.
                        </p>
                    </div>
                </div>
            {:else}
                <div class="alert success">
                    <i class="ri-checkbox-circle-line" aria-hidden="true"></i>
                    <div class="content"><p>Every check passed.</p></div>
                </div>
            {/if}

            <div class="list diagnostics-list">
                {#each health.checks as check (check.name)}
                    <div class="list-item">
                        <i
                            class="{STATUS[check.status]?.icon ?? 'ri-question-line'} txt-{STATUS[
                                check.status
                            ]?.cls ?? 'hint'}"
                            aria-hidden="true"
                        ></i>
                        <div class="content">
                            <div class="diagnostics-head">
                                <strong>{check.name}</strong>
                                <span class="label sm {STATUS[check.status]?.cls ?? ''}">
                                    {check.status}
                                </span>
                            </div>
                            <span class="txt-hint txt-sm">{check.detail}</span>
                            {#if check.hint}
                                <span class="txt-sm">{check.hint}</span>
                            {/if}
                        </div>
                        {#if ACTIONS[check.name]}
                            <div class="actions">
                                <button
                                    type="button"
                                    class="btn sm secondary"
                                    class:loading={busy === check.name}
                                    disabled={!!busy}
                                    onclick={() => run(check.name)}
                                >
                                    <i class={ACTIONS[check.name].icon} aria-hidden="true"></i>
                                    <span class="txt">{ACTIONS[check.name].label}</span>
                                </button>
                            </div>
                        {/if}
                    </div>
                {/each}
            </div>

            <div class="field-help">
                These are the same checks <code>gocommerce doctor</code> runs, and each button
                runs the same pass the engine's own five-minute ticker runs — so nothing here
                does anything the store would not have done on its own within five minutes.
                Delivering now does not resurrect dead-lettered events: those exhausted their
                retries and have to be requeued deliberately. And when a check fails because it
                could not run, the underlying database error is in the store's log rather than
                on this screen — it names a host, a port and a user, and this is a browser.
            </div>
        {/if}

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            {#if health.report}
                <span class="txt">Checked {relativeTime(health.report.at)}</span>
            {/if}
        </footer>
    </div>
</div>
