/**
 * The mark on each card of Payment methods and Shipping providers.
 *
 * A provider that Simple Icons carries is drawn with its own mark. The path
 * data below is copied from simple-icons 16.35.0 (https://simpleicons.org;
 * the icon data is CC0), so the panel ships every logo it shows. Nothing is
 * fetched at run time: a logo hotlinked from a CDN tells a third party who
 * opened the screen, and breaks the day the file moves.
 *
 * The marks are drawn in the text colour, not in the brands' own colours.
 * Green is kept for a provider that is switched on and working (D90), and
 * Adyen's colour is green. Razorpay's navy and USPS's indigo vanish on the
 * dark theme, and Paddle's and Lemon Squeezy's yellows on the light one. In
 * the text colour every mark reads in both themes, and colour on these
 * screens still means the provider's state.
 *
 * A provider Simple Icons does not carry gets no drawn substitute, because a
 * made-up logo would be a claim about someone else's brand. It gets a Remix
 * glyph for what it is instead: a card for a gateway, a truck for a carrier.
 * The methods that are not a company at all (cash on delivery, payment on
 * account, shipping by hand) get a glyph for what they do.
 *
 * Every Simple Icons path is drawn on a 24 by 24 grid, scaled so its longer
 * side spans it. A mark much wider than tall (Adyen's wordmark, the USPS
 * eagle in its box) is marked `wide`, and the tile draws it across most of
 * its width rather than in the 20px the others take. At 20px Adyen's
 * wordmark was a smudge about 6px tall; across the tile it reads.
 */

/** Brand marks, keyed by the provider code the engine reports. */
export const BRAND_MARKS = {
    adyen: {
        wide: true,
        path: "M11.64703 9.88245v2.93377c0 .13405.10867.24271.24272.24271h.46316V9.88245h1.76474v5.1503c0 .46916-.38033.8495-.8495.8495H9.94303v-1.23507h2.40991v-.52942h-1.62108c-.46917 0-.8495-.38033-.8495-.8495V9.88245h1.76467Zm-8.26124.00001c.46917 0 .8495.38034.8495.8495v3.3858H.8495c-.46916 0-.8495-.38033-.8495-.8495v-.94805c0-.46917.38034-.8495.8495-.8495h.91521v1.3455c0 .13406.10867.24272.24272.24272h.46316V11.184c0-.13405-.10867-.24271-.24272-.24271l-2.16719-.00002V9.88246Zm5.79068-1.76471v6.00001H5.79068c-.46917 0-.8495-.38033-.8495-.8495v-2.53631c0-.46917.38033-.8495.8495-.8495h.91515v2.93377c0 .13405.10867.24271.24272.24271h.46316l.00005-4.94118h1.76471Zm9.03286 1.76471a.8495.8495 0 0 1 .8495.8495v.94805c0 .46917-.38033.8495-.8495.8495h-.9152v-1.3455c0-.13404-.10868-.2427-.24272-.2427h-.46317v1.8749c0 .13406.10867.24272.24272.24272h2.16719v1.05883h-3.32511c-.46917 0-.8495-.38033-.8495-.8495v-3.3858Zm4.94117 0c.46916 0 .8495.38034.8495.8495v3.3858h-1.7647V11.184c-.0004-.13388-.10884-.24232-.24272-.24272h-.46316v3.1765H19.7647V9.88245Z",
    },
    lemonsqueezy: {
        path: "m7.4916 10.835 2.3748-6.5114a3.1497 3.1497 0 0 0-.065-2.3418C9.0315.183 6.9427-.398 5.2928.265 3.643.929 2.71 2.4348 3.512 4.3046l2.8197 6.5615c.219.509.97.489 1.16-.03m1.6798 1.0969 6.5334-2.7758c2.1699-.9219 2.7218-3.6907 1.022-5.2905l-.068-.063c-1.6669-1.5469-4.4217-1.002-5.3706 1.0359L8.3566 11.135c-.234.503.295 1.0199.8159.7979m.373.87 6.6454-2.5119c2.2078-.8349 4.6206.745 4.5886 3.0398l-.002.09c-.048 2.2358-2.3938 3.7376-4.5536 2.9467l-6.6724-2.4418a.595.595 0 0 1-.006-1.1229m-.386 1.9269 6.4375 2.9767a3.2997 3.2997 0 0 1 1.6658 1.6989c.769 1.7998-.283 3.6396-1.9328 4.3016-1.6499.662-3.4097.235-4.2097-1.6359l-2.8027-6.5694c-.217-.509.328-1.009.8419-.772",
    },
    paddle: {
        path: "M2.363 7.904v.849a3.95 3.95 0 0 1 3.65 2.425c.198.476.3.987.299 1.502h.791c0-1.04.416-2.037 1.157-2.772a3.962 3.962 0 0 1 2.792-1.149V7.91a3.959 3.959 0 0 1-3.65-2.425 3.893 3.893 0 0 1-.299-1.502h-.791c0 1.04-.416 2.037-1.157 2.772a3.96 3.96 0 0 1-2.792 1.149M13.105 2.51H6.312V0h6.793c4.772 0 8.532 3.735 8.532 8.314 0 4.58-3.76 8.314-8.532 8.314H9.156V24H6.312v-9.882h6.793c3.319 0 5.688-2.352 5.688-5.804 0-3.451-2.37-5.804-5.688-5.804",
    },
    razorpay: {
        path: "M22.436 0l-11.91 7.773-1.174 4.276 6.625-4.297L11.65 24h4.391l6.395-24zM14.26 10.098L3.389 17.166 1.564 24h9.008l3.688-13.902Z",
    },
    revenuecat: {
        path: "M4.3036.3999c-1.5246 0-3.2129.1508-4.303.4136v14.9997c.3083.1722.8432.28 1.5632.28.7404 0 1.2553-.1072 1.5433-.28v-5.2323a14.8588 14.8588 0 0 0 2.121.1512h.3294l2.8604 5.0588c.432.195 1.0288.3024 1.9348.3024.8033 0 1.38-.1104 1.6476-.3024l-3.437-5.8358c1.4195-.8004 2.326-2.2698 2.326-4.4964C10.8894 1.827 8.4232.4 4.3037.4zm15.4543 0c-1.3788 0-2.624.2707-3.6901.7945-2.4552 1.203-3.9609 3.7376-3.9609 7.3627 0 4.8245 2.6552 7.7155 7.1659 7.7155.9 0 1.5868-.3014 2.005.2554.4194.5568-.3582 1.2165-.7746 1.5105-1.6338 1.1544-5.7217-.1024-9.4908-.4804C5.5994 17.015.9264 16.3009.146 19.1928c-.4104 1.5264.1225 2.5013.6421 3.0503 1.044 1.1046 2.882 1.357 4.344 1.357a13.959 13.959 0 0 0 2.0508-.1558 1.311 1.311 0 0 0 1.023-.8063c.1674-.4254.0861-.904-.212-1.2562a1.3464 1.3464 0 0 0-1.2352-.4523c-1.5012.2706-3.6213.8685-4.4343.0105-.2748-.291-.2268-1.0037 0-1.2257.6048-.8748 4.493-.5393 8.4127-.0293 4.329.4344 8.4023 1.8609 10.945.6351.9955-.48 2.318-1.1941 2.318-3.792h-.0012c0-1.1473-.1489-2.274-.4476-3.3797-1.3818.1872-2.4783.2857-3.2883.2941-2.845 0-4.869-1.4484-4.869-5.0963 0-3.648 2.0461-5.1573 5.0179-5.1573 1.2011 0 2.129.2512 3.1405.7336.1062-.9234-.1058-2.1605-.5906-2.8523-.78-.4608-2.0014-.6703-3.2038-.6703zM4.51 3.1889c2.0579 0 3.2108.7111 3.2108 2.4421 0 1.6824-1.0912 2.3554-2.8816 2.3554a10.2838 10.2838 0 0 1-1.7706-.1511V3.3166a7.7782 7.7782 0 0 1 1.4413-.1277z",
    },
    stripe: {
        path: "M13.976 9.15c-2.172-.806-3.356-1.426-3.356-2.409 0-.831.683-1.305 1.901-1.305 2.227 0 4.515.858 6.09 1.631l.89-5.494C18.252.975 15.697 0 12.165 0 9.667 0 7.589.654 6.104 1.872 4.56 3.147 3.757 4.992 3.757 7.218c0 4.039 2.467 5.76 6.476 7.219 2.585.92 3.445 1.574 3.445 2.583 0 .98-.84 1.545-2.354 1.545-1.875 0-4.965-.921-6.99-2.109l-.9 5.555C5.175 22.99 8.385 24 11.714 24c2.641 0 4.843-.624 6.328-1.813 1.664-1.305 2.525-3.236 2.525-5.732 0-4.128-2.524-5.851-6.594-7.305h.003z",
    },
    usps: {
        wide: true,
        path: "M3.145 4.577L0 19.423h20.855L24 4.577H3.145zm-.157 3.806h9.436c.157 0 5.064 0 5.159.975H9.09l1.321 4.026c1.51-.723 5.222-2.233 7.455-2.328.944-.031 1.321.126 1.132.252-.126.063-1.038.189-1.761.377-1.258.315-1.321.315-2.642.755-1.478.503-2.705 1.069-4.53 1.919L.723 18.983l2.265-10.6zm16.483 1.698c-.535-.094-2.768.063-3.334.063-.126 0-.472.031-.472-.063 0-.063.126-.063.377-.094s1.006-.157 1.258-.283c.063-.063.22-.157.315-.252.031-.063.063-.094.157-.094h1.164c.755 0 1.195.094 1.132.723-.031.315-.472 1.132-.629 1.384-.063.094-.189.189-.157 0 .126-.503.597-1.321.189-1.384zm.88 8.902H2.076s17.363-6.794 17.552-6.92c0 0 1.541-2.076.629-2.925-.283-.283-.692-.283-2.265-.283 0 0-.063-.598-2.485-1.164-.283-.063-11.858-2.517-11.858-2.517h19.628l-2.926 13.809zm2.925-.695c0-.195-.114-.293-.358-.293h-.406v1.008h.146v-.439h.179l.276.455h.179L23 18.564c.162-.016.276-.097.276-.276zm-.455.146h-.163v-.341h.211c.114 0 .228.016.228.163 0 .162-.13.178-.276.178zm.016-.829a.868.868 0 0 0-.894.878c0 .504.406.894.894.894s.894-.39.894-.894a.878.878 0 0 0-.894-.878zm0 1.642c-.423 0-.731-.325-.731-.764 0-.423.325-.748.731-.748.406 0 .731.325.731.748 0 .439-.325.764-.731.764z",
    },
};

/** Methods that are not a company: a glyph for what each one does. */
export const METHOD_GLYPHS = {
    cod: "ri-hand-coin-line", // cash handed over at the door
    on_account: "ri-file-list-3-line", // billed to a business customer's account
    manual: "ri-box-3-line", // packed and handed to a carrier by the store
};

/** Everyone else, by what kind of provider it is. */
export const KIND_GLYPHS = {
    payments: "ri-bank-card-line",
    shipping: "ri-truck-line",
};

/**
 * providerIcon is what to draw for one provider: `{ path, wide }` for a brand
 * mark on the 24-unit grid, or `{ glyph }` naming a Remix icon class.
 *
 * @param {string} code the provider code from the settings
 * @param {"payments" | "shipping"} kind which screen it is listed on
 * @returns {{ path: string, wide: boolean } | { glyph: string }}
 */
export function providerIcon(code, kind) {
    const mark = BRAND_MARKS[code];
    if (mark) return { path: mark.path, wide: !!mark.wide };
    return { glyph: METHOD_GLYPHS[code] ?? KIND_GLYPHS[kind] ?? "ri-plug-line" };
}
