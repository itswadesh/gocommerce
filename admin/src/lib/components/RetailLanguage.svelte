<script>
    /** English or Hindi, for the retail assistant's screens. */
    import { retailLanguage, setRetailLanguage } from "$lib/retail-lang.svelte.js";
</script>

<div class="retail-language" role="group" aria-label="Language / भाषा">
    <button type="button" class="btn sm" class:transparent={retailLanguage() !== "en"} class:secondary={retailLanguage() !== "en"} aria-pressed={retailLanguage() === "en"} lang="en" onclick={() => setRetailLanguage("en")}><span class="txt">English</span></button>
    <button type="button" class="btn sm" class:transparent={retailLanguage() !== "hi"} class:secondary={retailLanguage() !== "hi"} aria-pressed={retailLanguage() === "hi"} lang="hi" onclick={() => setRetailLanguage("hi")}><span class="txt">हिंदी</span></button>
</div>

<style>
    .retail-language {
        display: inline-flex;
        gap: 4px;
        margin-left: auto;
    }
</style>
