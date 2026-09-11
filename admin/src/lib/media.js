/**
 * What a media record looks like to a person, and what a filter row means to
 * the engine.
 *
 * These were inside MediaZone, where only a product editor could reach them.
 * Three surfaces need them now — the zone, the library screen and the file
 * details drawer — and pulling them out is also what makes them testable: none
 * of it touches the DOM, so `tests/media.test.js` runs them under plain node.
 *
 * The one piece of real logic here is `mediaQuery`. The filter row an operator
 * sees is not the query string the engine takes: "Under 100 KB" is a pair of
 * byte bounds and "name A–Z" is a sort plus an order. Doing that translation in
 * two places is how a screen ends up sending `size=small` to a listing that has
 * never heard of it and silently showing everything.
 */

/**
 * Shopify's File size filter, as the byte bounds the engine takes.
 *
 * Named buckets rather than a pair of number boxes: nobody searching a media
 * library thinks in bytes, and "under 100 KB" is the actual question — which of
 * these is small enough to put on a page.
 *
 * `max: 0` is no upper bound, which is `MediaQuery.MaxBytes`'s own convention
 * and what makes "over 5 MB" expressible without a sentinel.
 */
export const SIZE_BUCKETS = {
    small: { min: 0, max: 100 * 1024, label: "Under 100 KB" },
    medium: { min: 100 * 1024, max: 1024 * 1024, label: "100 KB – 1 MB" },
    large: { min: 1024 * 1024, max: 5 * 1024 * 1024, label: "1 MB – 5 MB" },
    huge: { min: 5 * 1024 * 1024, max: 0, label: "Over 5 MB" },
};

/** What to call a file when something has to name it. */
export function mediaLabel(item) {
    if (!item) return "";
    return item.alt || item.filename || `media ${item.id}`;
}

export function kindIcon(kind) {
    if (kind === "video") return "ri-movie-line";
    if (kind === "model") return "ri-box-3-line";
    return "ri-image-line";
}

/**
 * The line under a thumbnail: "PNG", "MP4". The extension rather than the MIME
 * type, because that is what the operator recognises and what Shopify shows;
 * the kind is the fallback for a URL that carries no extension.
 */
export function fileType(item) {
    if (!item) return "";
    const name = item.filename || item.url || "";
    const path = name.split(/[?#]/)[0];
    const dot = path.lastIndexOf(".");
    if (dot > 0 && dot > path.lastIndexOf("/")) {
        return path.slice(dot + 1).toUpperCase();
    }
    if (item.kind === "model") return "3D model";
    return (item.kind || "").toUpperCase();
}

/** Bytes as a person reads them. Zero means unknown — linked media. */
export function fileSize(bytes) {
    if (!bytes) return "";
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** "1200 × 800", or empty for anything the engine could not measure. */
export function dimensions(item) {
    if (!item?.width || !item?.height) return "";
    return `${item.width} × ${item.height}`;
}

/**
 * The filter row as `GET /api/admin/media` takes it.
 *
 * Empty values are left empty rather than omitted, because `query()` in api.js
 * drops them on the way to the wire — so an unset filter costs nothing here and
 * never reaches the engine.
 */
export function mediaQuery(filters = {}, extra = {}) {
    const bucket = SIZE_BUCKETS[filters.size];
    // One control, two parameters. The empty value sends neither, which is what
    // keeps newest first: the thing you just uploaded is usually the thing you
    // want, and no sort/order pair can ask for that.
    const [sortField, sortOrder] = String(filters.sort ?? "").split(":");
    return {
        q: String(filters.q ?? "").trim(),
        kind: filters.kind ?? "",
        usage: filters.usage ?? "",
        sort: sortField ?? "",
        order: sortField ? (sortOrder ?? "") : "",
        product_id: filters.product_id ?? "",
        min_bytes: bucket?.min || "",
        max_bytes: bucket?.max || "",
        ...extra,
    };
}

/** True while any filter is narrowing the list — what tells an empty result
 *  "nothing matches" apart from "the library is empty". */
export function mediaFiltered(filters = {}) {
    return !!(
        String(filters.q ?? "").trim() ||
        filters.kind ||
        filters.usage ||
        filters.size ||
        filters.scope ||
        filters.product_id
    );
}
