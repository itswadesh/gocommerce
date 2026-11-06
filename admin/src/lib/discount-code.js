/**
 * What is wrong with a typed discount code, or "" (the engine's
 * validateDiscountCode). A code the discount already has passes: one made before
 * the rule keeps working until somebody changes it.
 */
export function discountCodeProblem(code, saved = "") {
    const typed = (code ?? "").trim();
    if (!typed || typed === saved) return "";
    if (typed.length > 20) return "A discount code can be at most 20 characters.";
    if (!/^[A-Za-z0-9]+$/.test(typed)) return "A discount code can only use letters and digits.";
    return "";
}
