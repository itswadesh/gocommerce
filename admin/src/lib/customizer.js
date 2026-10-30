/**
 * The arithmetic behind a product's Customizer card (D174): where the print
 * area sits on the mockup, how big the print file comes out, and the body the
 * engine is sent. Everything here is in the mockup's own pixels, as the
 * engine stores it; the card scales only what it draws.
 */

export const MIN_DPI = 72;
export const MAX_DPI = 600;
export const MAX_PRINT_MM = 1000;
export const MAX_PRINT_PIXELS = 3_800_000;
export const MAX_PRINT_SIDE = 16384;
export const MAX_STICKERS = 30;
export const MAX_MIN_PHOTO = 10000;
export const ASPECT_TOLERANCE = 0.02;

export const TOOLS = [
    { key: "upload", label: "Photo" },
    { key: "text", label: "Words" },
    { key: "shapes", label: "Shapes" },
    { key: "background", label: "Background colour" },
    { key: "stickers", label: "Stickers" },
];

const round = Math.round;

export function printPixels(size) {
    const w = Number(size?.width_mm);
    const h = Number(size?.height_mm);
    const dpi = Number(size?.dpi);
    if (!(w > 0) || !(h > 0) || !(dpi > 0)) return null;
    return { width: round((w / 25.4) * dpi), height: round((h / 25.4) * dpi) };
}

export function aspectGap(area, size) {
    const w = Number(size?.width_mm);
    const h = Number(size?.height_mm);
    if (!area || !(area.width > 0) || !(area.height > 0) || !(w > 0) || !(h > 0)) return null;
    const want = w / h;
    return Math.abs(area.width / area.height - want) / want;
}

export function clampArea(area, mockup) {
    const W = mockup.width;
    const H = mockup.height;
    const width = Math.min(Math.max(1, round(area.width)), W);
    const height = Math.min(Math.max(1, round(area.height)), H);
    const x = Math.min(Math.max(0, round(area.x)), W - width);
    const y = Math.min(Math.max(0, round(area.y)), H - height);
    const radius = Math.min(Math.max(0, round(area.radius ?? 0)), Math.floor(Math.min(width, height) / 2));
    return { x, y, width, height, radius };
}

export function areaFromDrag(a, b, mockup, radius = 0) {
    const inside = (p) => ({
        x: Math.min(Math.max(0, p.x), mockup.width),
        y: Math.min(Math.max(0, p.y), mockup.height),
    });
    const [p, q] = [inside(a), inside(b)];
    return clampArea(
        {
            x: Math.min(p.x, q.x),
            y: Math.min(p.y, q.y),
            width: Math.abs(q.x - p.x),
            height: Math.abs(q.y - p.y),
            radius,
        },
        mockup,
    );
}

export function moveArea(area, dx, dy, mockup) {
    return clampArea({ ...area, x: area.x + dx, y: area.y + dy }, mockup);
}

export function resizeArea(area, dx, dy, mockup) {
    const width = Math.min(area.width + dx, mockup.width - area.x);
    const height = Math.min(area.height + dy, mockup.height - area.y);
    return clampArea({ ...area, width, height }, mockup);
}

export function toMockup(clientX, clientY, box, mockup) {
    return {
        x: ((clientX - box.left) / box.width) * mockup.width,
        y: ((clientY - box.top) / box.height) * mockup.height,
    };
}

export function fitToSize(area, size, mockup) {
    const w = Number(size?.width_mm);
    const h = Number(size?.height_mm);
    if (!area || !(w > 0) || !(h > 0)) return area;
    const ratio = w / h;
    let width = area.width;
    let height = width / ratio;
    if (height > mockup.height) {
        height = mockup.height;
        width = height * ratio;
    }
    const cx = area.x + area.width / 2;
    const cy = area.y + area.height / 2;
    return clampArea({ ...area, x: cx - width / 2, y: cy - height / 2, width, height }, mockup);
}

export function startingArea(mockup, size) {
    const width = mockup.width * 0.8;
    const area = { x: mockup.width * 0.1, y: mockup.height * 0.1, width, height: mockup.height * 0.8, radius: 0 };
    return clampArea(fitToSize(area, size, mockup), mockup);
}

export function formFromSetup(s) {
    return {
        enabled: !!s.enabled,
        mockup: s.mockup ? { ...s.mockup } : null,
        overlay_url: s.overlay_url ?? "",
        print_area: s.print_area ? { ...s.print_area } : null,
        print_size: {
            width_mm: s.print_size?.width_mm || "",
            height_mm: s.print_size?.height_mm || "",
            dpi: s.print_size?.dpi || 300,
        },
        tools: { upload: true, text: true, shapes: true, background: true, stickers: false, ...s.tools },
        fonts: [...(s.fonts ?? [])],
        sticker_media_ids: [...(s.sticker_media_ids ?? [])],
        min_photo_px: s.min_photo_px ?? 1000,
    };
}

export function formProblem(f) {
    const size = f.print_size;
    const mm = (v) => v === "" || v == null || (Number(v) > 0 && Number(v) <= MAX_PRINT_MM);
    if (!mm(size.width_mm) || !mm(size.height_mm)) return `The print size must be above 0 and at most ${MAX_PRINT_MM} mm a side.`;
    const dpi = Number(size.dpi);
    if (!Number.isInteger(dpi) || dpi < MIN_DPI || dpi > MAX_DPI) return `The resolution must be ${MIN_DPI} to ${MAX_DPI} dpi.`;
    const px = printPixels(size);
    const tooLarge = (limit) =>
        `The print file would be ${px.width} × ${px.height} pixels, over ${limit}. Larger prints are not supported yet, so lower the resolution or the print size.`;
    if (px && px.width * px.height > MAX_PRINT_PIXELS) return tooLarge(`${MAX_PRINT_PIXELS / 1e6} megapixels`);
    if (px && Math.max(px.width, px.height) > MAX_PRINT_SIDE) return tooLarge(`${MAX_PRINT_SIDE} pixels a side`);
    const min = Number(f.min_photo_px);
    if (!Number.isInteger(min) || min < 0 || min > MAX_MIN_PHOTO) return `The smallest photo must be 0 to ${MAX_MIN_PHOTO} pixels.`;
    const gap = aspectGap(f.print_area, size);
    if (gap !== null && gap > ASPECT_TOLERANCE) {
        return "The print area is a different shape from the print size. Match them, or the print comes out stretched.";
    }
    if (f.tools.stickers && !f.sticker_media_ids.length) return "Choose a sticker, or switch stickers off.";
    if (f.tools.text && !f.fonts.length) return "Choose a font, or switch words off.";
    if (f.enabled) {
        if (!f.mockup) return "Choose the mockup before switching the customizer on.";
        if (!f.print_area) return "Mark the print area before switching the customizer on.";
        if (!px) return "Give the print size before switching the customizer on.";
        if (!Object.values(f.tools).some(Boolean)) return "Switch on at least one tool.";
    }
    return "";
}

export function setupBody(f) {
    return {
        enabled: f.enabled,
        mockup: f.mockup,
        overlay_url: f.overlay_url || null,
        print_area: f.print_area,
        print_size: {
            width_mm: Number(f.print_size.width_mm) || 0,
            height_mm: Number(f.print_size.height_mm) || 0,
            dpi: Number(f.print_size.dpi),
        },
        tools: { ...f.tools },
        fonts: [...f.fonts],
        sticker_media_ids: [...f.sticker_media_ids],
        min_photo_px: Number(f.min_photo_px),
    };
}

export function fontName(key) {
    return String(key ?? "")
        .split("-")
        .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
        .join(" ");
}

export function designSummary(design) {
    const layers = design?.document?.layers ?? [];
    const family = new Map((design?.fonts ?? []).map((f) => [f.key, f.family]));
    const words = layers
        .filter((l) => l.type === "text")
        .map((l) => ({ text: l.text, font: family.get(l.font) || fontName(l.font), color: l.color }));
    const count = (type) => layers.filter((l) => l.type === type).length;
    const size = design?.print_size;
    return {
        words,
        fonts: [...new Set(words.map((w) => w.font))],
        photos: count("image"),
        shapes: count("shape"),
        stickers: count("sticker"),
        background: design?.document?.background ?? null,
        print: size?.width_mm > 0 ? `${size.width_mm} × ${size.height_mm} mm at ${size.dpi} dpi` : null,
    };
}
