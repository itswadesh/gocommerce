# The starting home page's pictures

The starting home page (D140) names these, and the engine serves them from its
own binary at `/x/layouts/starter/1/`, to any store without naming one. They
are cached as immutable, and pages shops have saved keep naming them, so a
file here is never changed or removed: a changed picture gets a new file name,
and the old file stays.

| File | Section | Size | Source |
|---|---|---|---|
| `hero.webp` | Hero, on computers (5:4) | 1200×960 | Unsplash, siddharth vyas |
| `hero-phone.webp` | Hero, on phones (4:3) | 800×600 | Unsplash, siddharth vyas |
| `lookbook.webp` | Picture beside words (5:4) | 1000×800 | Unsplash, Subhra Jyoti Paul |
| `tile-shop-all.webp` | Tile "Shop all" | 600×600 | Google Gemini |
| `tile-collections.webp` | Tile "Collections" | 600×600 | Google Gemini |
| `tile-categories.webp` | Tile "Categories" | 600×600 | Google Gemini |
| `tile-track.webp` | Tile "Track an order" | 600×600 | Google Gemini |

Each was cropped for the storefront's slot with `sharp` and saved as WebP at
quality 80–82.

## Photographs (Unsplash License)

The owner allowed Unsplash photographs of Indian women for this theme on
2026-10-28. Both are free to use under the
[Unsplash License](https://unsplash.com/license); neither is an Unsplash+
photograph.

- **Hero**: photo by [siddharth vyas](https://unsplash.com/@siddharth_vyas1)
  on [Unsplash](https://unsplash.com/photos/_UlqNjXP158). Unsplash License.
- **Lookbook**: photo by [Subhra Jyoti Paul](https://unsplash.com/@sjpaul)
  on [Unsplash](https://unsplash.com/photos/VDekCdiPyAM). Unsplash License.

## Tiles (Google Gemini)

Generated with Google Gemini (owner's Gemini app), cropped and compressed.
Each crop drops Gemini's sparkle mark in the bottom-right corner. Each prompt
ended "No people, no text, no logos."

1. Shop all: "…1:1 square… a single plain kraft-paper shopping bag with rope
   handles, filled with a few colourful folded items peeking out, standing on
   a pink geometric plinth against a solid bright coral background…"
2. Collections: "…1:1 square… three neat stacks of folded textiles and fabrics
   in coordinated colour families (warm, cool, neutral), each stack on its own
   small geometric plinth, against a solid soft lilac background…"
3. Categories: "…1:1 square… an open modular display shelf made of four
   colourful cubes in a 2x2 grid, each cube holding one different everyday
   product — a white sneaker, a ceramic mug, a pair of headphones, a small
   potted plant — against a solid fresh lime-green background…"
4. Track an order: "…1:1 square… a neatly sealed plain cardboard parcel tied
   with twine, a second smaller parcel stacked on top at an angle, standing on
   a blue geometric plinth against a solid sky-blue background…"

The "…" marks words each shared with the first prompt of the set, which asked
for the same photographic style and lighting.
