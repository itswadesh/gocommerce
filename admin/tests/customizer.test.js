import { test } from "node:test";
import assert from "node:assert/strict";

import {
    areaFromDrag,
    aspectGap,
    clampArea,
    designSummary,
    fitToSize,
    fontName,
    formFromSetup,
    formProblem,
    moveArea,
    printPixels,
    resizeArea,
    setupBody,
    startingArea,
    toMockup,
} from "../src/lib/customizer.js";

const mockup = { url: "/media/m.png", width: 1000, height: 2000 };
const size = { width_mm: 75, height_mm: 150, dpi: 300 };

test("the print file is the print size at its resolution", () => {
    assert.deepEqual(printPixels(size), { width: 886, height: 1772 });
    assert.equal(printPixels({ width_mm: "", height_mm: 150, dpi: 300 }), null);
});

test("an area is kept inside the mockup, in whole pixels, its radius at most half its shorter side", () => {
    assert.deepEqual(clampArea({ x: -10, y: 1900.4, width: 400.6, height: 300, radius: 500 }, mockup), {
        x: 0,
        y: 1700,
        width: 401,
        height: 300,
        radius: 150,
    });
    assert.deepEqual(clampArea({ x: 0, y: 0, width: 5000, height: 0, radius: -3 }, mockup), {
        x: 0,
        y: 0,
        width: 1000,
        height: 1,
        radius: 0,
    });
});

test("a drag in any direction marks the rectangle between its ends", () => {
    assert.deepEqual(areaFromDrag({ x: 700, y: 900 }, { x: 100, y: 100 }, mockup, 20), {
        x: 100,
        y: 100,
        width: 600,
        height: 800,
        radius: 20,
    });
    assert.deepEqual(areaFromDrag({ x: 900, y: 1900 }, { x: 1300, y: 2400 }, mockup).width, 100);
});

test("moving stops at the edges and resizing never crosses them", () => {
    const area = { x: 100, y: 100, width: 800, height: 1600, radius: 40 };
    assert.deepEqual(moveArea(area, 500, -500, mockup), { ...area, x: 200, y: 0 });
    assert.deepEqual(resizeArea(area, 400, 50, mockup), { ...area, width: 900, height: 1650 });
    assert.deepEqual(resizeArea(area, -2000, 0, mockup), { ...area, width: 1, radius: 0 });
});

test("a pointer on the drawn mockup is read in the mockup's pixels", () => {
    const box = { left: 20, top: 10, width: 250, height: 500 };
    assert.deepEqual(toMockup(145, 260, box, mockup), { x: 500, y: 1000 });
});

test("the shape check and the fix agree", () => {
    const wide = { x: 100, y: 100, width: 800, height: 800, radius: 0 };
    assert.equal(aspectGap(wide, size), 1);
    const fitted = fitToSize(wide, size, mockup);
    assert.deepEqual(fitted, { x: 100, y: 0, width: 800, height: 1600, radius: 0 });
    assert.ok(aspectGap(fitted, size) <= 0.02);
    const tall = fitToSize({ x: 0, y: 0, width: 1000, height: 100, radius: 0 }, { width_mm: 1, height_mm: 4, dpi: 300 }, mockup);
    assert.deepEqual(tall, { x: 250, y: 0, width: 500, height: 2000, radius: 0 });
});

test("a new mockup starts with an area in the print's shape", () => {
    const area = startingArea(mockup, size);
    assert.equal(area.width, 800);
    assert.equal(area.height, 1600);
    assert.ok(aspectGap(area, size) < 0.001);
});

test("each problem the engine would refuse is caught first, in words", () => {
    const ok = formFromSetup({
        enabled: true,
        mockup,
        print_area: { x: 100, y: 200, width: 800, height: 1600, radius: 60 },
        print_size: size,
        tools: { upload: true, text: true, shapes: false, background: true, stickers: false },
        fonts: ["inter"],
        sticker_media_ids: [],
        min_photo_px: 1000,
    });
    assert.equal(formProblem(ok), "");
    const broken = (edit) => {
        const f = structuredClone(ok);
        edit(f);
        return formProblem(f);
    };
    assert.match(broken((f) => (f.print_size.dpi = 1000)), /72 to 600 dpi/);
    assert.match(broken((f) => (f.print_size.width_mm = 1200)), /at most 1000 mm/);
    assert.match(
        broken((f) => (f.print_size = { width_mm: 75, height_mm: 150, dpi: 600 })),
        /1772 × 3543 pixels, over 3.8 megapixels/,
    );
    assert.match(
        broken((f) => (f.print_size = { width_mm: 1000, height_mm: 5, dpi: 600 })),
        /23622 × 118 pixels, over 16384 pixels a side/,
    );
    assert.match(broken((f) => (f.print_size.width_mm = 100)), /different shape/);
    assert.match(broken((f) => (f.min_photo_px = 1.5)), /smallest photo/);
    assert.match(broken((f) => (f.tools.stickers = true)), /Choose a sticker/);
    assert.match(broken((f) => (f.fonts = [])), /Choose a font/);
    assert.match(broken((f) => (f.mockup = null)), /Choose the mockup/);
    assert.match(broken((f) => (f.print_area = null)), /Mark the print area/);
    assert.match(broken((f) => (f.print_size.width_mm = "")), /Give the print size/);
    assert.match(
        broken((f) => (f.tools = { upload: false, text: false, shapes: false, background: false, stickers: false })),
        /at least one tool/,
    );
    assert.equal(broken((f) => ((f.enabled = false), (f.mockup = null), (f.print_area = null))), "");
    assert.equal(broken((f) => ((f.enabled = false), (f.print_size.width_mm = null))), "");
});

test("the body carries numbers and nulls, never the form's empty strings", () => {
    const f = formFromSetup({ print_size: { dpi: 300 }, fonts: ["inter"], min_photo_px: 1000 });
    assert.deepEqual(setupBody(f), {
        enabled: false,
        mockup: null,
        overlay_url: null,
        print_area: null,
        print_size: { width_mm: 0, height_mm: 0, dpi: 300 },
        tools: { upload: true, text: true, shapes: true, background: true, stickers: false },
        fonts: ["inter"],
        sticker_media_ids: [],
        min_photo_px: 1000,
    });
});

test("a font key reads as words when the design does not name its family", () => {
    assert.equal(fontName("playfair-display"), "Playfair Display");
    const named = designSummary({
        fonts: [{ key: "dm-sans", family: "DM Sans" }],
        document: { layers: [{ type: "text", text: "Hi", font: "dm-sans", color: "#000000" }] },
    });
    assert.deepEqual(named.fonts, ["DM Sans"]);
});

test("an order line's design is summed up for the person printing it", () => {
    const s = designSummary({
        document: {
            version: 1,
            background: "#112233",
            layers: [
                { type: "image", upload_id: "a" },
                { type: "text", text: "Asha", font: "poppins", color: "#ffffff" },
                { type: "text", text: "2026", font: "poppins", color: "#000000" },
                { type: "shape", shape: "heart" },
            ],
        },
        print_size: { width_mm: 75, height_mm: 150, dpi: 300 },
    });
    assert.deepEqual(s, {
        words: [
            { text: "Asha", font: "Poppins", color: "#ffffff" },
            { text: "2026", font: "Poppins", color: "#000000" },
        ],
        fonts: ["Poppins"],
        photos: 1,
        shapes: 1,
        stickers: 0,
        background: "#112233",
        print: "75 × 150 mm at 300 dpi",
    });
});
