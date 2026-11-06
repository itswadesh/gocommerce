/**
 * ISO 3166-1 alpha-2, name first.
 *
 * A list rather than a text box because nobody knows the codes: an operator
 * filling a customs form knows "Portugal", not PT, and a two-letter box takes
 * PO or PRT as readily as the right answer. The engine still stores and returns
 * the code — this is only how it is picked, and it still accepts any well-formed
 * code from the API for a place this list has not caught up with.
 *
 * Generated from Intl.DisplayNames, minus the aggregates, the exceptional
 * reservations and the codes ISO has withdrawn (RH and ZW are both "Zimbabwe").
 * Names are English; the panel has no i18n of its own yet.
 */
export const COUNTRIES = [
    { value: "AF", label: "Afghanistan" },
    { value: "AX", label: "Åland Islands" },
    { value: "AL", label: "Albania" },
    { value: "DZ", label: "Algeria" },
    { value: "AS", label: "American Samoa" },
    { value: "AD", label: "Andorra" },
    { value: "AO", label: "Angola" },
    { value: "AI", label: "Anguilla" },
    { value: "AQ", label: "Antarctica" },
    { value: "AG", label: "Antigua & Barbuda" },
    { value: "AR", label: "Argentina" },
    { value: "AM", label: "Armenia" },
    { value: "AW", label: "Aruba" },
    { value: "AU", label: "Australia" },
    { value: "AT", label: "Austria" },
    { value: "AZ", label: "Azerbaijan" },
    { value: "BS", label: "Bahamas" },
    { value: "BH", label: "Bahrain" },
    { value: "BD", label: "Bangladesh" },
    { value: "BB", label: "Barbados" },
    { value: "BY", label: "Belarus" },
    { value: "BE", label: "Belgium" },
    { value: "BZ", label: "Belize" },
    { value: "BJ", label: "Benin" },
    { value: "BM", label: "Bermuda" },
    { value: "BT", label: "Bhutan" },
    { value: "BO", label: "Bolivia" },
    { value: "BA", label: "Bosnia & Herzegovina" },
    { value: "BW", label: "Botswana" },
    { value: "BV", label: "Bouvet Island" },
    { value: "BR", label: "Brazil" },
    { value: "IO", label: "British Indian Ocean Territory" },
    { value: "VG", label: "British Virgin Islands" },
    { value: "BN", label: "Brunei" },
    { value: "BG", label: "Bulgaria" },
    { value: "BF", label: "Burkina Faso" },
    { value: "BI", label: "Burundi" },
    { value: "KH", label: "Cambodia" },
    { value: "CM", label: "Cameroon" },
    { value: "CA", label: "Canada" },
    { value: "CV", label: "Cape Verde" },
    { value: "BQ", label: "Caribbean Netherlands" },
    { value: "KY", label: "Cayman Islands" },
    { value: "CF", label: "Central African Republic" },
    { value: "TD", label: "Chad" },
    { value: "CL", label: "Chile" },
    { value: "CN", label: "China" },
    { value: "CX", label: "Christmas Island" },
    { value: "CC", label: "Cocos (Keeling) Islands" },
    { value: "CO", label: "Colombia" },
    { value: "KM", label: "Comoros" },
    { value: "CG", label: "Congo - Brazzaville" },
    { value: "CD", label: "Congo - Kinshasa" },
    { value: "CK", label: "Cook Islands" },
    { value: "CR", label: "Costa Rica" },
    { value: "CI", label: "Côte d’Ivoire" },
    { value: "HR", label: "Croatia" },
    { value: "CU", label: "Cuba" },
    { value: "CW", label: "Curaçao" },
    { value: "CY", label: "Cyprus" },
    { value: "CZ", label: "Czechia" },
    { value: "DK", label: "Denmark" },
    { value: "DJ", label: "Djibouti" },
    { value: "DM", label: "Dominica" },
    { value: "DO", label: "Dominican Republic" },
    { value: "EC", label: "Ecuador" },
    { value: "EG", label: "Egypt" },
    { value: "SV", label: "El Salvador" },
    { value: "GQ", label: "Equatorial Guinea" },
    { value: "ER", label: "Eritrea" },
    { value: "EE", label: "Estonia" },
    { value: "SZ", label: "Eswatini" },
    { value: "ET", label: "Ethiopia" },
    { value: "FK", label: "Falkland Islands" },
    { value: "FO", label: "Faroe Islands" },
    { value: "FJ", label: "Fiji" },
    { value: "FI", label: "Finland" },
    { value: "FR", label: "France" },
    { value: "GF", label: "French Guiana" },
    { value: "PF", label: "French Polynesia" },
    { value: "TF", label: "French Southern Territories" },
    { value: "GA", label: "Gabon" },
    { value: "GM", label: "Gambia" },
    { value: "GE", label: "Georgia" },
    { value: "DE", label: "Germany" },
    { value: "GH", label: "Ghana" },
    { value: "GI", label: "Gibraltar" },
    { value: "GR", label: "Greece" },
    { value: "GL", label: "Greenland" },
    { value: "GD", label: "Grenada" },
    { value: "GP", label: "Guadeloupe" },
    { value: "GU", label: "Guam" },
    { value: "GT", label: "Guatemala" },
    { value: "GG", label: "Guernsey" },
    { value: "GN", label: "Guinea" },
    { value: "GW", label: "Guinea-Bissau" },
    { value: "GY", label: "Guyana" },
    { value: "HT", label: "Haiti" },
    { value: "HM", label: "Heard & McDonald Islands" },
    { value: "HN", label: "Honduras" },
    { value: "HK", label: "Hong Kong SAR China" },
    { value: "HU", label: "Hungary" },
    { value: "IS", label: "Iceland" },
    { value: "IN", label: "India" },
    { value: "ID", label: "Indonesia" },
    { value: "IR", label: "Iran" },
    { value: "IQ", label: "Iraq" },
    { value: "IE", label: "Ireland" },
    { value: "IM", label: "Isle of Man" },
    { value: "IL", label: "Israel" },
    { value: "IT", label: "Italy" },
    { value: "JM", label: "Jamaica" },
    { value: "JP", label: "Japan" },
    { value: "JE", label: "Jersey" },
    { value: "JO", label: "Jordan" },
    { value: "KZ", label: "Kazakhstan" },
    { value: "KE", label: "Kenya" },
    { value: "KI", label: "Kiribati" },
    { value: "XK", label: "Kosovo" },
    { value: "KW", label: "Kuwait" },
    { value: "KG", label: "Kyrgyzstan" },
    { value: "LA", label: "Laos" },
    { value: "LV", label: "Latvia" },
    { value: "LB", label: "Lebanon" },
    { value: "LS", label: "Lesotho" },
    { value: "LR", label: "Liberia" },
    { value: "LY", label: "Libya" },
    { value: "LI", label: "Liechtenstein" },
    { value: "LT", label: "Lithuania" },
    { value: "LU", label: "Luxembourg" },
    { value: "MO", label: "Macao SAR China" },
    { value: "MG", label: "Madagascar" },
    { value: "MW", label: "Malawi" },
    { value: "MY", label: "Malaysia" },
    { value: "MV", label: "Maldives" },
    { value: "ML", label: "Mali" },
    { value: "MT", label: "Malta" },
    { value: "MH", label: "Marshall Islands" },
    { value: "MQ", label: "Martinique" },
    { value: "MR", label: "Mauritania" },
    { value: "MU", label: "Mauritius" },
    { value: "YT", label: "Mayotte" },
    { value: "MX", label: "Mexico" },
    { value: "FM", label: "Micronesia" },
    { value: "MD", label: "Moldova" },
    { value: "MC", label: "Monaco" },
    { value: "MN", label: "Mongolia" },
    { value: "ME", label: "Montenegro" },
    { value: "MS", label: "Montserrat" },
    { value: "MA", label: "Morocco" },
    { value: "MZ", label: "Mozambique" },
    { value: "MM", label: "Myanmar (Burma)" },
    { value: "NA", label: "Namibia" },
    { value: "NR", label: "Nauru" },
    { value: "NP", label: "Nepal" },
    { value: "NL", label: "Netherlands" },
    { value: "NC", label: "New Caledonia" },
    { value: "NZ", label: "New Zealand" },
    { value: "NI", label: "Nicaragua" },
    { value: "NE", label: "Niger" },
    { value: "NG", label: "Nigeria" },
    { value: "NU", label: "Niue" },
    { value: "NF", label: "Norfolk Island" },
    { value: "KP", label: "North Korea" },
    { value: "MK", label: "North Macedonia" },
    { value: "MP", label: "Northern Mariana Islands" },
    { value: "NO", label: "Norway" },
    { value: "OM", label: "Oman" },
    { value: "PK", label: "Pakistan" },
    { value: "PW", label: "Palau" },
    { value: "PS", label: "Palestinian Territories" },
    { value: "PA", label: "Panama" },
    { value: "PG", label: "Papua New Guinea" },
    { value: "PY", label: "Paraguay" },
    { value: "PE", label: "Peru" },
    { value: "PH", label: "Philippines" },
    { value: "PN", label: "Pitcairn Islands" },
    { value: "PL", label: "Poland" },
    { value: "PT", label: "Portugal" },
    { value: "PR", label: "Puerto Rico" },
    { value: "QA", label: "Qatar" },
    { value: "RE", label: "Réunion" },
    { value: "RO", label: "Romania" },
    { value: "RU", label: "Russia" },
    { value: "RW", label: "Rwanda" },
    { value: "WS", label: "Samoa" },
    { value: "SM", label: "San Marino" },
    { value: "ST", label: "São Tomé & Príncipe" },
    { value: "CQ", label: "Sark" },
    { value: "SA", label: "Saudi Arabia" },
    { value: "SN", label: "Senegal" },
    { value: "RS", label: "Serbia" },
    { value: "SC", label: "Seychelles" },
    { value: "SL", label: "Sierra Leone" },
    { value: "SG", label: "Singapore" },
    { value: "SX", label: "Sint Maarten" },
    { value: "SK", label: "Slovakia" },
    { value: "SI", label: "Slovenia" },
    { value: "SB", label: "Solomon Islands" },
    { value: "SO", label: "Somalia" },
    { value: "ZA", label: "South Africa" },
    { value: "GS", label: "South Georgia & South Sandwich Islands" },
    { value: "KR", label: "South Korea" },
    { value: "SS", label: "South Sudan" },
    { value: "ES", label: "Spain" },
    { value: "LK", label: "Sri Lanka" },
    { value: "BL", label: "St. Barthélemy" },
    { value: "SH", label: "St. Helena" },
    { value: "KN", label: "St. Kitts & Nevis" },
    { value: "LC", label: "St. Lucia" },
    { value: "MF", label: "St. Martin" },
    { value: "PM", label: "St. Pierre & Miquelon" },
    { value: "VC", label: "St. Vincent & Grenadines" },
    { value: "SD", label: "Sudan" },
    { value: "SR", label: "Suriname" },
    { value: "SJ", label: "Svalbard & Jan Mayen" },
    { value: "SE", label: "Sweden" },
    { value: "CH", label: "Switzerland" },
    { value: "SY", label: "Syria" },
    { value: "TW", label: "Taiwan" },
    { value: "TJ", label: "Tajikistan" },
    { value: "TZ", label: "Tanzania" },
    { value: "TH", label: "Thailand" },
    { value: "TL", label: "Timor-Leste" },
    { value: "TG", label: "Togo" },
    { value: "TK", label: "Tokelau" },
    { value: "TO", label: "Tonga" },
    { value: "TT", label: "Trinidad & Tobago" },
    { value: "TN", label: "Tunisia" },
    { value: "TR", label: "Türkiye" },
    { value: "TM", label: "Turkmenistan" },
    { value: "TC", label: "Turks & Caicos Islands" },
    { value: "TV", label: "Tuvalu" },
    { value: "UM", label: "U.S. Outlying Islands" },
    { value: "VI", label: "U.S. Virgin Islands" },
    { value: "UG", label: "Uganda" },
    { value: "UA", label: "Ukraine" },
    { value: "AE", label: "United Arab Emirates" },
    { value: "GB", label: "United Kingdom" },
    { value: "US", label: "United States" },
    { value: "UY", label: "Uruguay" },
    { value: "UZ", label: "Uzbekistan" },
    { value: "VU", label: "Vanuatu" },
    { value: "VA", label: "Vatican City" },
    { value: "VE", label: "Venezuela" },
    { value: "VN", label: "Vietnam" },
    { value: "WF", label: "Wallis & Futuna" },
    { value: "EH", label: "Western Sahara" },
    { value: "YE", label: "Yemen" },
    { value: "ZM", label: "Zambia" },
    { value: "ZW", label: "Zimbabwe" },
];

/**
 * Which countries spend which currency. Neither Intl nor the engine knows this,
 * so it is a table, and a test holds it to COUNTRIES. Antarctica has none.
 */
const SPENT_IN = {
    AED: "AE", AFN: "AF", ALL: "AL", AMD: "AM", AOA: "AO", ARS: "AR", AUD: "AU CC CX HM KI NF NR TV", AWG: "AW",
    AZN: "AZ", BAM: "BA", BBD: "BB", BDT: "BD", BHD: "BH", BIF: "BI", BMD: "BM", BND: "BN", BOB: "BO", BRL: "BR",
    BSD: "BS", BTN: "BT", BWP: "BW", BYN: "BY", BZD: "BZ", CAD: "CA", CDF: "CD", CHF: "CH LI", CLP: "CL", CNY: "CN",
    COP: "CO", CRC: "CR", CUP: "CU", CVE: "CV", CZK: "CZ", DJF: "DJ", DKK: "DK FO GL", DOP: "DO", DZD: "DZ",
    EGP: "EG", ERN: "ER", ETB: "ET",
    EUR: "AD AT AX BE BG BL CY DE EE ES FI FR GF GP GR HR IE IT LT LU LV MC ME MF MQ MT NL PM PT RE SI SK SM TF VA XK YT",
    FJD: "FJ", FKP: "FK", GBP: "CQ GB GG GS IM JE", GEL: "GE", GHS: "GH", GIP: "GI", GMD: "GM", GNF: "GN", GTQ: "GT",
    GYD: "GY", HKD: "HK", HNL: "HN", HTG: "HT", HUF: "HU", IDR: "ID", ILS: "IL PS", INR: "IN", IQD: "IQ", IRR: "IR",
    ISK: "IS", JMD: "JM", JOD: "JO", JPY: "JP", KES: "KE", KGS: "KG", KHR: "KH", KMF: "KM", KPW: "KP", KRW: "KR",
    KWD: "KW", KYD: "KY", KZT: "KZ", LAK: "LA", LBP: "LB", LKR: "LK", LRD: "LR", LSL: "LS", LYD: "LY", MAD: "EH MA",
    MDL: "MD", MGA: "MG", MKD: "MK", MMK: "MM", MNT: "MN", MOP: "MO", MRU: "MR", MUR: "MU", MVR: "MV", MWK: "MW",
    MXN: "MX", MYR: "MY", MZN: "MZ", NAD: "NA", NGN: "NG", NIO: "NI", NOK: "BV NO SJ", NPR: "NP",
    NZD: "CK NU NZ PN TK", OMR: "OM", PAB: "PA", PEN: "PE", PGK: "PG", PHP: "PH", PKR: "PK", PLN: "PL", PYG: "PY",
    QAR: "QA", RON: "RO", RSD: "RS", RUB: "RU", RWF: "RW", SAR: "SA", SBD: "SB", SCR: "SC", SDG: "SD", SEK: "SE",
    SGD: "SG", SHP: "SH", SLE: "SL", SOS: "SO", SRD: "SR", SSP: "SS", STN: "ST", SYP: "SY", SZL: "SZ", THB: "TH",
    TJS: "TJ", TMT: "TM", TND: "TN", TOP: "TO", TRY: "TR", TTD: "TT", TWD: "TW", TZS: "TZ", UAH: "UA", UGX: "UG",
    USD: "AS BQ EC FM GU IO MH MP PR PW SV TC TL UM US VG VI", UYU: "UY", UZS: "UZ", VES: "VE", VND: "VN", VUV: "VU",
    WST: "WS", XAF: "CF CG CM GA GQ TD", XCD: "AG AI DM GD KN LC MS VC", XCG: "CW SX",
    XOF: "BF BJ CI GW ML NE SN TG", XPF: "NC PF WF", YER: "YE", ZAR: "ZA", ZMW: "ZM", ZWG: "ZW",
};

/** currencyOf is what a country spends, or "" when the table has no answer. */
export function currencyOf(country) {
    for (const [code, countries] of Object.entries(SPENT_IN)) {
        if (countries.split(" ").includes(country)) return code;
    }
    return "";
}

/**
 * The main zone of a country with several, where the browser's list comes in
 * alphabetical order and would open a store in New York on Adak time; and of
 * two places the list leaves out. Uninhabited places have no answer.
 */
const MAIN_ZONE = {
    AR: "America/Argentina/Buenos_Aires", AU: "Australia/Sydney", BR: "America/Sao_Paulo", CA: "America/Toronto",
    CD: "Africa/Kinshasa", CL: "America/Santiago", CN: "Asia/Shanghai", CQ: "Europe/Guernsey", CY: "Asia/Nicosia",
    DE: "Europe/Berlin", EC: "America/Guayaquil", ES: "Europe/Madrid", FM: "Pacific/Pohnpei", GL: "America/Nuuk",
    ID: "Asia/Jakarta", KI: "Pacific/Tarawa", KZ: "Asia/Almaty", MH: "Pacific/Majuro", MN: "Asia/Ulaanbaatar",
    MX: "America/Mexico_City", MY: "Asia/Kuala_Lumpur", NZ: "Pacific/Auckland", PF: "Pacific/Tahiti", PG: "Pacific/Port_Moresby",
    PS: "Asia/Hebron", PT: "Europe/Lisbon", RU: "Europe/Moscow", UA: "Europe/Kyiv", UM: "Pacific/Wake",
    US: "America/New_York", UZ: "Asia/Tashkent", XK: "Europe/Belgrade",
};

/**
 * timezoneOf is the zone to open a store in that country on: the browser's own
 * when it is in that country, since a Pune shop and a Los Angeles one know their
 * clock, else the country's only or main zone, else "".
 *
 * ponytail: Intl.Locale's zone list is missing from some browsers, where a
 * one-zone country has no answer and the form shows UTC to change by hand. Carry
 * a full table if that turns up among the people opening stores.
 */
export function timezoneOf(country) {
    let zones = [];
    try {
        const locale = new Intl.Locale(`und-${country}`);
        zones = locale.getTimeZones?.() ?? locale.timeZones ?? [];
    } catch {
        // No such region, or no country chosen: no zones.
    }
    try {
        const here = new Intl.DateTimeFormat().resolvedOptions().timeZone;
        if (zones.includes(here)) return here;
        const zone = MAIN_ZONE[country] ?? (zones.length === 1 ? zones[0] : "");
        // Spelled as this browser's own list spells it (Asia/Calcutta, Asia/Kolkata).
        return zone && new Intl.DateTimeFormat("en", { timeZone: zone }).resolvedOptions().timeZone;
    } catch {
        return "";
    }
}

/** zoneOffset is a zone's distance from UTC at that moment: "+5:30", "-5:00", "+0:00". */
export function zoneOffset(timeZone, at = new Date()) {
    // en-US because other locales write the sign and the "GMT" their own way.
    const gmt = new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "shortOffset" })
        .formatToParts(at)
        .find((part) => part.type === "timeZoneName")?.value ?? "";
    const [, sign = "+", hours = "0", minutes = "00"] = gmt.match(/GMT([+-])(\d+)(?::(\d+))?/) ?? [];
    return `${sign}${hours}:${minutes}`;
}

let zoneList = null;

/**
 * The browser's zones, labelled with their country, because nobody looking for
 * India's time would search "Asia/Calcutta". Sorted by that label, so a country's
 * zones sit together. Built once: it asks about every country.
 */
function labelledZones() {
    if (zoneList) return zoneList;
    const countryOf = new Map();
    for (const { value, label } of COUNTRIES) {
        try {
            const locale = new Intl.Locale(`und-${value}`);
            for (const zone of locale.getTimeZones?.() ?? locale.timeZones ?? []) countryOf.set(zone, label);
        } catch {
            // Not a region this browser knows.
        }
    }
    let zones = [];
    try {
        zones = Intl.supportedValuesOf("timeZone");
    } catch {
        // Older browsers: only what is saved.
    }
    zoneList = zones
        .map((zone) => {
            // Browsers still spell the city the old way, and people search the new one.
            const name = zone === "Asia/Calcutta" ? "Asia/Calcutta (Kolkata)" : zone;
            const country = countryOf.get(zone);
            return { value: zone, label: country ? `${country}: ${name}` : name };
        })
        .sort((a, b) => a.label.localeCompare(b.label));
    return zoneList;
}

/** timezoneOptions is the zones to choose from, keeping a saved one not listed. */
export function timezoneOptions(saved) {
    const zones = labelledZones();
    return saved && !zones.some((z) => z.value === saved) ? [{ value: saved, label: saved }, ...zones] : zones;
}

/**
 * countryOptions is the list for a select whose saved value may predate it: a
 * value that is not a code on the list (a name typed into the old text box,
 * an import's "India") stays a choice, first, rather than the select showing
 * blank and the next save erasing it.
 */
export function countryOptions(saved) {
    if (!saved || COUNTRIES.some((c) => c.value === saved)) return COUNTRIES;
    return [{ value: saved, label: saved }, ...COUNTRIES];
}
