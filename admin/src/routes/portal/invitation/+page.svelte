<script>
    /**
     * Accepting a company invitation: the page the invitation email links to
     * (GOCOMMERCE_B2B_INVITE_URL, pointed at /portal/invitation?token={token}).
     *
     * The invitation is for an address, and accepting it from an account at
     * that address is what proves the address and joins the company (D69).
     * So the page needs somebody signed in as that address: a new buyer
     * registers with it, an existing one signs in, and either way the
     * acceptance follows straight on — one form, not two.
     *
     * Nothing here can say which address was invited: the token is the only
     * thing the link carries, and the engine does not answer "who is this
     * token for" to whoever holds it. A wrong address is told so by the
     * engine, which names the right one masked.
     */
    import { onMount } from "svelte";
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { toast } from "$lib/toast.svelte.js";
    import { trade, tradeApi, register, signOut, loadMe, loadBasket, sentence } from "$lib/trade.svelte.js";
    import SignInForm from "../SignInForm.svelte";

    const token = $derived(page.url.searchParams.get("token") ?? "");

    let mode = $state("new");
    let email = $state("");
    let name = $state("");
    let password = $state("");
    let busy = $state(false);
    let problem = $state(null);
    let attempt = $state(0);

    async function accept() {
        busy = true;
        problem = null;
        try {
            await tradeApi.post("/x/b2b/invitations/accept", { token });
            await loadMe();
            loadBasket().catch(() => {});
            toast.success(trade.me ? `Welcome to ${trade.me.company.name}.` : "Invitation accepted.");
            goto(`${base}/portal`);
        } catch (err) {
            if (err.handled) return;
            attempt += 1;
            problem =
                err.code === "invalid_token"
                    ? { kind: "spent", text: "This invitation has expired or has already been used. Ask your company's account admin to send a new one." }
                    : err.status === 403
                      ? { kind: "address", text: sentence(err.message) }
                      : err.status === 409
                        ? { kind: "taken", text: sentence(err.message) }
                        : { kind: "other", text: sentence(err.message) || "We couldn't accept the invitation." };
        } finally {
            busy = false;
        }
    }

    async function createAndAccept(event) {
        event.preventDefault();
        if (busy) return;
        problem = null;
        if (password.length < 8) {
            attempt += 1;
            problem = { kind: "form", text: "Choose a password of at least 8 characters." };
            return;
        }
        busy = true;
        try {
            await register(email, password, name);
            password = "";
        } catch (err) {
            busy = false;
            attempt += 1;
            if (err.status === 409) {
                mode = "existing";
                problem = { kind: "form", text: "There's already an account for that address — sign in with it below." };
            } else {
                problem = { kind: "form", text: sentence(err.message) || "We couldn't create the account." };
            }
            return;
        }
        await accept();
    }

    async function useAnotherAddress() {
        await signOut();
        problem = null;
        mode = "existing";
    }

    onMount(() => {
        email = page.url.searchParams.get("email") ?? "";
    });
</script>

{#if !token}
    <div class="alert warning portal-notice" role="status">
        <p>This link is missing its invitation code. Open the link in your invitation email again, the whole of it.</p>
    </div>
{:else}
    <h2 class="portal-out-heading">Join your company's trade account</h2>

    {#if trade.token}
        <p class="txt-hint">
            You're signed in as <strong>{trade.account?.email}</strong>. Accept to start ordering for your company at its
            prices.
        </p>
        {#key attempt}
            {#if problem}
                <div class="alert {problem.kind === 'spent' ? 'warning' : 'danger'} portal-refusal m-b-sm" role="alert">
                    <p>{problem.text}</p>
                </div>
            {/if}
        {/key}
        <button type="button" class="btn lg block portal-press" class:loading={busy} disabled={busy} onclick={accept}>
            <span class="txt">Accept the invitation</span>
        </button>
        <button type="button" class="btn block transparent secondary m-t-sm portal-press" onclick={useAnotherAddress}>
            <span class="txt">Use a different address</span>
        </button>
    {:else}
        <p class="txt-hint">Use the address the invitation was sent to.</p>
        <div class="portal-tabs portal-tabs-fill m-b-base" role="tablist" aria-label="Your account">
            <button type="button" role="tab" class="portal-tab" class:active={mode === "new"} aria-selected={mode === "new"} onclick={() => (mode = "new")}>
                I'm new here
            </button>
            <button
                type="button"
                role="tab"
                class="portal-tab"
                class:active={mode === "existing"}
                aria-selected={mode === "existing"}
                onclick={() => (mode = "existing")}
            >
                I have an account
            </button>
        </div>

        {#key attempt}
            {#if problem}
                <div class="alert {problem.kind === 'spent' ? 'warning' : 'danger'} portal-refusal m-b-sm" role="alert">
                    <p>{problem.text}</p>
                </div>
            {/if}
        {/key}

        {#if mode === "new"}
            <form class="portal-form portal-notice" onsubmit={createAndAccept} novalidate>
                <div class="field required">
                    <label for="inv-name">Your name</label>
                    <input id="inv-name" type="text" autocomplete="name" required bind:value={name} />
                </div>
                <div class="field required m-t-sm">
                    <label for="inv-email">Work email</label>
                    <input id="inv-email" type="email" autocomplete="email" required bind:value={email} />
                </div>
                <div class="field required m-t-sm">
                    <label for="inv-password">Choose a password</label>
                    <input id="inv-password" type="password" autocomplete="new-password" minlength="8" required bind:value={password} />
                </div>
                <div class="field-help">At least 8 characters.</div>
                <button
                    type="submit"
                    class="btn lg block next m-t-base portal-press"
                    class:loading={busy}
                    disabled={busy || !email.trim() || !name.trim() || !password}
                >
                    <span class="txt">Create account and join</span>
                    <i class="ri-arrow-right-line" aria-hidden="true"></i>
                </button>
            </form>
        {:else}
            <div class="portal-notice">
                <SignInForm bind:email submitLabel="Sign in and join" onsignedin={accept} />
            </div>
        {/if}
    {/if}
{/if}
