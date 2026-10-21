# Brand Guidelines v1.0

GoCommerce is the engine and its admin panel; [kitcommerce.store](https://kitcommerce.store/)
is the project's home page, and admin.kitcommerce.store is the hosted panel.
This file says how all of them look and speak. Most of it gathers rules that
already exist in [`DESIGN.md`](../DESIGN.md) (the panel's design system),
[`PLAN.md`](../PLAN.md) §3 (positioning) and the docs' own prose rules, and it
defers to them: when this file and one of those disagree, the other file is
right and this one is fixed. The logo's clear space and minimum size are new
here.

The colour values live once, as tokens in `admin/src/lib/styles/design.css`.
The hex values below are those tokens converted for reference. Code uses the
tokens (`bg-background`, `text-muted-foreground`), never a hex.

## Quick Reference

| | |
|---|---|
| Name | GoCommerce, one word, capital G and C |
| One line | A small, composable commerce engine for Go |
| Mark | The K mark: a white K on a dark rounded square, `admin/static/favicon.svg` |
| Look | Near-monochrome. Colour only for status and for destructive actions |
| Type | Inter, with the system sans-serif as fallback |
| Voice | Plain, specific, honest. Say what happens and why |

## 1. Color Palette

### Primary Colors

| Name | Token | Light | Dark | Use |
|---|---|---|---|---|
| Primary | `--primary` | `#171717` | `#e5e5e5` | Primary buttons; it inverts in dark mode |
| Foreground | `--foreground` | `#0a0a0a` | `#fafafa` | Body text |

### Secondary Colors

| Name | Token | Light | Dark | Use |
|---|---|---|---|---|
| Secondary | `--secondary` | `#f5f5f5` | `#262626` | Secondary buttons |
| Accent | `--accent` | `#f5f5f5` | `#262626` | Hover and the active navigation item |
| Mark ground | (the logo file) | `#0f1720` | `#0f1720` | The K mark's square only, never the UI |

### Neutral Palette

All greys have zero chroma: no blue-grey and no warm grey.

| Name | Token | Light | Dark |
|---|---|---|---|
| Background | `--background` | `#ffffff` | `#0a0a0a` |
| Card | `--card` | `#ffffff` | `#171717` |
| Muted | `--muted` | `#f5f5f5` | `#262626` |
| Muted text | `--muted-foreground` | `#737373` | `#a1a1a1` |
| Border | `--border` | `#e5e5e5` | white at 10% |
| Ring | `--ring` | `#a1a1a1` | `#737373` |
| Sidebar | `--sidebar` | `#fafafa` | |

In dark mode, cards are lighter than the page, so a card is painted
`bg-card`, not `bg-background` (DESIGN.md §0).

### Semantic Colors

Status is a small dot beside a word, never a filled block.

| State | Colour | Hex |
|---|---|---|
| Running, done, paid | emerald-500 | `#00bc7d` |
| Failed, errored | red-500 | `#fb2c36` |
| Pending, idle | muted text | `#737373` |
| In progress | amber-500 | `#fe9a00` |
| Destructive action (light) | `--destructive` | `#e7000b` |
| Destructive action (dark) | `--destructive` | `#ff6467` |

### Accessibility

- Body text is about 19:1 against the page in both themes.
- Muted text (`#737373`) passes WCAG AA on white (4.74:1) and on the sidebar
  (4.54:1), but not on the muted fill `#f5f5f5` (4.35:1), so muted text never
  sits on a muted fill in light mode. In dark mode muted text (`#a1a1a1`)
  passes on the page, cards and fills (5.9:1 or more).
- A new colour pairing has its contrast checked before it ships.
- Status is never colour alone: the dot always sits beside its word.
- Focus is always visible, on every control.

## 2. Typography

### Font Stack

```css
--font-heading: 'Inter';
--font-body: 'Inter';
--font-mono: 'ui-monospace';
```

The full stack is `Inter, "Inter Fallback", ui-sans-serif, system-ui,
sans-serif`, served from the panel's own files (`fonts-inter.css`); no font is
fetched from a third party.

### Type Scale

| Role | Size | Weight | Notes |
|---|---|---|---|
| Page title | 30px | 600 | Tight tracking. The only large text on a screen |
| Section heading | 14px | 600 | Hierarchy comes from weight, not size |
| Big number | 30–36px | 700 | Tight tracking |
| Card label | 12px | 500 | Uppercase, wide tracking, muted |
| Body, table cell | 14px | 400 | |
| Caption, meta | 12px | 400 | Muted |

On the screens before a store (sign in, sign up, reset), fields use 16px text
on phones, so a phone does not zoom into them (D93).

## 3. Logo Usage

### Variants

| File | Use |
|---|---|
| `admin/static/favicon.svg` | Browser tab, app icon |
| `admin/static/images/logo.svg` | The same mark at 26px, for the panel |

The mark is kitcommerce.store's K: a white K on a `#0f1720` square with
rounded corners, drawn on a 24-unit grid.

### Clear Space

Keep at least a quarter of the mark's width clear on every side.

### Minimum Size

16px, its size in a browser tab.

### Don'ts

- Do not recolour the square or the K, or put the K on its own without the
  square.
- Do not stretch it, rotate it, outline it or add a shadow.
- Do not invent a new mark for a sub-product. The engine, the panel and the
  site share this one.

## 4. Voice & Tone

### Core Attributes

| Attribute | Description |
|---|---|
| **Plain** | Short words and short sentences. Explain it the way you would on the phone |
| **Specific** | Facts instead of adjectives: what it does, what it costs, which right it needs |
| **Honest** | Nothing nobody verified. Say what is not built yet as plainly as what is |
| **Reasoned** | A decision comes with its reason, so the reader can disagree with the reason |

### Brand Personality

| Trait | Description |
|---|---|
| **Plain** | Writes for a developer or a shop owner reading quickly, not for a committee |
| **Exact** | Names the field, the right, the route or the amount instead of gesturing at it |
| **Calm** | Errors say what happened and what to do next, without alarm or apology theatre |
| **Owning** | Assumes the reader owns their stack and their data, and treats them that way |

### Voice Chart

| Trait | We Are | We Are Not |
|---|---|---|
| Plain | Direct, concrete | Breezy, cute, full of exclamation marks |
| Exact | Precise about limits | Vague ("powerful", "flexible") |
| Calm | Matter-of-fact about failure | Alarmed, or falsely cheerful |
| Owning | Open about how it works | Mysterious about what happens to data |

### Tone by Context

| Context | Tone | Example |
|---|---|---|
| Home page, release notes | What changed for a store, in a sentence or a short table | "Stores can now switch off cash on delivery." |
| Docs and PLAN.md | Instructional, then the reason | "Money is integer minor units, because JPY has no decimals and KWD has three." |
| Panel labels and buttons | A verb saying what happens | "Erase sample products", not "Submit" |
| Error messages | What went wrong, and what to do | "Your role (staff) does not carry orders.refund." |
| Commit subjects | Public, plain, written for the updates page | "Carriers show their own icons on Shipping providers" |

Money in copy is formatted for the store's currency by the client
(`Intl.NumberFormat`); the API only ever returns `{amount_minor, currency}`
(D14).

### Prohibited Terms

| Avoid |
|---|
| premium |
| world-class |
| best |
| seamless |
| blazing fast |
| the fastest ecommerce engine |
| Shopify killer |
| zero infrastructure |
| infinitely scalable |
| microservices without the complexity |
| 10x cheaper |

The first five are adjectives standing in for facts: a reader cannot check
them, so they say nothing. The next five are the claims PLAN.md §3 rules out
as hard to prove and a distraction from the real advantage; GoCommerce also
runs on PostgreSQL and is one binary on purpose. A saving is published only
as a number measured on identical tasks, with the method shown, never as a
multiplier.

### Forbidden Phrases

- "Something went wrong" with nothing after it
- "Click here"
- "Submit" as a button label
- "Welcome to" as an opening line
- "Coming soon", "to be added", or any placeholder: a missing thing is simply absent
- "Lorem ipsum"

## 5. Imagery Guidelines

### Photography Style

- Product screenshots are real screens of the panel or a storefront, captured
  at phone width (390px) and desktop width (1440px), in light and dark.
- The sample shop's product photos are licensed (Unsplash License) and credited
  in `docs/admin-panel.md` (D96); they exist to be erased before a store opens.
  Any other photo is the owner's own, or licensed and credited. No stock
  photos.
- No reviews, ratings, customer counts or logos of customers anywhere until
  they are real and verified.

### Illustrations

None. A diagram that explains something (a state machine, a data flow) is
drawn in plain lines in the monochrome palette, or as text.

### Icons

- Remix Icon, the panel's icon font, at 16px inline and 20px in headers.
- Icons are muted unless they sit in an active or primary context.
- Every navigation item has one; buttons usually do not, except a trailing →.
- A payment or carrier is shown by its own published mark, shipped inside the
  binary, never hotlinked (D101).

## 6. Design Components

### Buttons

| Variant | Use |
|---|---|
| Primary | The one main action on a screen |
| Secondary | A second, safe action |
| Outline | Actions in a toolbar or a card |
| Ghost | Low-weight actions in lists |
| Destructive | Erasing or revoking, always behind a confirmation |

Secondary actions inside a card are 12px muted text links ending in →, not
buttons. The panel keeps PocketBase's `.btn` controls (AGENTS.md rule 12).

### Spacing Scale

A 4px unit. Card padding 20px for stat tiles and 24px for panels; 16px between
cards; content in a centred column with 24px page padding. At 360px wide
nothing is cut off and nothing scrolls sideways.

### Border Radius

| Token | Value | For |
|---|---|---|
| `rounded-sm` | 6px | Checkboxes, tiny tiles |
| `rounded-md` | 8px | Navigation items |
| `rounded-lg` | 10px | Buttons, inputs, selects |
| `rounded-xl` | 14px | Cards and panels |
| `rounded-full` | pill | Status chips, avatars |

### Elevation

No shadows on cards: a border and a step in background do the separating.
Popovers, menus and dialogs are the only places a shadow appears.

### Motion

- Every control answers hover, press and focus within 150–300ms, and every
  state change (loading, saved, failed, opened, closed) is visible.
- Hover is a change of background. No bounce, no scale on hover, no
  decorative animation.
- Pages and views change with a view transition.
- Animate opacity and transform only, and keep a `prefers-reduced-motion`
  path that drops the movement and keeps the feedback.

## AI Image Generation

GoCommerce does not use AI-generated images. If the owner asks for one, it is
made with Google Gemini, and it is never presented as a photograph of a real
product, place or person.

## Known inconsistencies

These are differences between what ships and what this file describes. Each
needs a decision before it is changed.

- **The olive accent.** `admin/src/lib/styles/gocommerce.css` still defines
  `--accentColor: #89a24c` with `#1e2416` text for PocketBase's accent
  surfaces. The design system says the UI has no hue except status and
  destructive actions. No component class uses the accent surfaces any more;
  whether any PocketBase control still draws with the accent has to be checked
  screen by screen before the variables are removed.
- **The mark's near-black.** The K mark's square is `#0f1720`, a faintly blue
  near-black from kitcommerce.store, while the UI's darkest neutral is
  `#0a0a0a`. The mark keeps its own colour; it is a logo, not a surface.
- **An old wordmark.** `admin/static/images/logo_header.svg` is a shopping-bag
  wordmark in `#1e2416` from before the K mark. Nothing in the panel
  references it.

## Changelog

| Version | Date | Change |
|---|---|---|
| 1.0 | 21 October 2026 | First version, gathered from DESIGN.md, PLAN.md §3 and the docs' prose rules. DESIGN.md §8 corrected to name Remix Icon |
