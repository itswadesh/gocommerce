# DESIGN.md — Dokploy-style Admin Panel Design System

A neutral, zero-chroma, shadcn/ui + Tailwind v4 design language. Copy this file into your admin panel repo and follow it for all new UI.

---

## 1. Foundation

- **Framework**: Tailwind CSS v4 + shadcn/ui (Radix primitives)
- **Color space**: `oklch()` — all greys are **pure neutral (chroma = 0)**. No blue-grey, no warm grey.
- **Font**: `Inter` with system fallback stack
  ```css
  font-family: Inter, "Inter Fallback", ui-sans-serif, system-ui, sans-serif;
  ```
- **Base radius**: `--radius: 0.625rem` (10px)
- **Philosophy**: near-monochrome UI. Color appears *only* in status indicators and destructive actions. Hierarchy comes from weight, size and border — not from color.

---

## 2. Color tokens

Put these in `globals.css`.

```css
:root {
  --background: oklch(100% 0 0);
  --foreground: oklch(14.5% 0 0);

  --card: oklch(100% 0 0);
  --card-foreground: oklch(14.5% 0 0);
  --popover: oklch(100% 0 0);
  --popover-foreground: oklch(14.5% 0 0);

  --primary: oklch(20.5% 0 0);            /* near-black buttons */
  --primary-foreground: oklch(98.5% 0 0);

  --secondary: oklch(97% 0 0);
  --secondary-foreground: oklch(20.5% 0 0);
  --muted: oklch(97% 0 0);
  --muted-foreground: oklch(55.6% 0 0);   /* all secondary/meta text */
  --accent: oklch(97% 0 0);
  --accent-foreground: oklch(20.5% 0 0);

  --destructive: oklch(57.7% 0.245 27.325);
  --border: oklch(92.2% 0 0);
  --input: oklch(92.2% 0 0);
  --ring: oklch(70.8% 0 0);

  --radius: 0.625rem;

  /* Sidebar gets its own scale — 1.5% darker than the page */
  --sidebar: oklch(98.5% 0 0);
  --sidebar-foreground: oklch(14.5% 0 0);
  --sidebar-primary: oklch(20.5% 0 0);
  --sidebar-primary-foreground: oklch(98.5% 0 0);
  --sidebar-accent: oklch(97% 0 0);       /* active nav item bg */
  --sidebar-accent-foreground: oklch(20.5% 0 0);
  --sidebar-border: oklch(92.2% 0 0);
  --sidebar-ring: oklch(70.8% 0 0);
}

.dark {
  --background: oklch(14.5% 0 0);
  --foreground: oklch(98.5% 0 0);

  --card: oklch(20.5% 0 0);               /* cards lift above the page */
  --card-foreground: oklch(98.5% 0 0);
  --popover: oklch(20.5% 0 0);
  --popover-foreground: oklch(98.5% 0 0);

  --primary: oklch(92.2% 0 0);            /* inverts to near-white */
  --primary-foreground: oklch(20.5% 0 0);

  --secondary: oklch(26.9% 0 0);
  --secondary-foreground: oklch(98.5% 0 0);
  --muted: oklch(26.9% 0 0);
  --muted-foreground: oklch(70.8% 0 0);
  --accent: oklch(26.9% 0 0);
  --accent-foreground: oklch(98.5% 0 0);

  --destructive: oklch(70.4% 0.191 22.216);
  --border: oklch(100% 0 0 / 0.1);        /* alpha borders, not solid greys */
  --input: oklch(100% 0 0 / 0.15);
  --ring: oklch(55.6% 0 0);
}
```

**Rule:** never hardcode a hex. Always `bg-background`, `text-muted-foreground`, `border-border`, etc.

### Status colors (the only hue in the product)
Used as small dots + text, never as fills:

| State | Dot |
|---|---|
| running / done / success | `bg-emerald-500` |
| errored / failed | `bg-red-500` |
| idle / pending | `bg-muted-foreground` (grey) |
| building / in progress | `bg-amber-500` |

Pattern: `<span class="size-2 rounded-full bg-emerald-500" />` next to the label.

---

## 3. Typography scale

| Role | Size | Weight | Tracking |
|---|---|---|---|
| Page title (h1) | `text-3xl` (30px) | 600 | `-0.75px` / `tracking-tight` |
| Section heading (h2) | `text-sm` (14px) | 600 | normal |
| Big metric number | `text-3xl`–`text-4xl` | 700 | `tracking-tight` |
| Card label / eyebrow | `text-xs` (12px) | 500 | `uppercase tracking-wide`, `text-muted-foreground` |
| Body / table cell | `text-sm` (14px) | 400 | normal |
| Meta / caption | `text-xs` (12px) | 400 | `text-muted-foreground` |

Notes:
- Section headings are **small and bold**, not large. Hierarchy is by weight, not size.
- Only the page title is big. Everything else stays 12–14px.
- Card eyebrow labels are uppercase 12px muted — this is the signature of the style.

---

## 4. Radius scale

| Token | Value | Used for |
|---|---|---|
| `rounded-sm` | 6px | tiny icon tiles, checkboxes |
| `rounded-md` | 8px | sidebar nav items, menu buttons |
| `rounded-lg` | 10px | **buttons**, inputs, selects |
| `rounded-xl` | 14px | **cards, panels, stat tiles** |
| `rounded-full` | pill | badges, status chips, avatars |

---

## 5. Elevation

**There are no shadows.** Separation is done with `border` + a background-value step.

- Card on page: `border bg-background` (light) / `bg-card` which is lighter than page (dark)
- Popovers/dropdowns/modals: the only place a shadow is allowed — `shadow-lg`
- Never use `shadow-sm` on cards. If you feel you need it, you need a border.

---

## 6. Spacing & layout

- Spacing unit: 4px. Use `gap-2 / 3 / 4 / 6` and `p-4 / p-5 / p-6`.
- **Card padding: `p-5`** (20px) for stat tiles, `p-6` for content panels.
- **Card gap in grids: `gap-4`**.
- Stat tile minimum height: `min-h-[140px]`, content `flex flex-col justify-between`.
- Main content: centered column, `max-w-5xl mx-auto`, page padding `p-6`.
- Sidebar width: ~256px (`w-64`), collapsible to icon rail.

### App shell
```
┌────────────┬──────────────────────────────────┐
│ sidebar    │ topbar (h-14, border-b)          │
│ bg-sidebar ├──────────────────────────────────┤
│ border-r   │ content (bg-muted/30, p-6)       │
│            │   max-w-5xl mx-auto              │
└────────────┴──────────────────────────────────┘
```
Topbar holds: sidebar toggle → breadcrumb → right-aligned status pill.

---

## 7. Components

### Card / stat tile
```html
<div class="rounded-xl border bg-background p-5 min-h-[140px] flex flex-col justify-between">
  <span class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Projects</span>
  <div>
    <div class="text-3xl font-bold tracking-tight">14</div>
    <p class="text-xs text-muted-foreground mt-1">14 environments</p>
  </div>
</div>
```
Anatomy: **uppercase muted label → big number → small muted subline**. Never add a card title bar.

### Panel with header
```html
<div class="rounded-xl border bg-background">
  <div class="flex items-center justify-between px-5 py-4 border-b">
    <h2 class="text-sm font-semibold flex items-center gap-2">Recent deployments</h2>
    <a class="text-xs text-muted-foreground hover:text-foreground">view all →</a>
  </div>
  <div class="divide-y"> ...rows... </div>
</div>
```
Secondary actions are **text links with a `→` arrow**, 12px muted — not buttons.

### Buttons
```html
<!-- primary -->
<button class="inline-flex h-9 items-center justify-center gap-2 rounded-lg bg-primary px-4
               text-sm font-medium text-primary-foreground transition-colors
               hover:bg-primary/90 focus-visible:ring-2 focus-visible:ring-ring
               disabled:pointer-events-none disabled:opacity-50">
  Go to projects <span aria-hidden>→</span>
</button>
```
| Variant | Classes |
|---|---|
| primary | `bg-primary text-primary-foreground hover:bg-primary/90` |
| secondary | `bg-secondary text-secondary-foreground hover:bg-secondary/80` |
| outline | `border bg-background hover:bg-accent` |
| ghost | `hover:bg-accent hover:text-accent-foreground` |
| destructive | `bg-destructive text-white hover:bg-destructive/90` |

Sizes: `h-8` (sm) · `h-9` (default) · `h-10` (lg) · `size-9` (icon). All `rounded-lg`, `text-sm font-medium`.

### Sidebar nav item
```html
<a class="flex w-full items-center gap-2 rounded-md p-2 text-sm
          hover:bg-sidebar-accent
          data-[active=true]:bg-sidebar-accent data-[active=true]:font-medium">
  <Icon class="size-4 shrink-0" /> Home
</a>
```
- Active state = **background tint only**, no left bar, no color change.
- Group labels: `px-2 text-xs font-medium text-muted-foreground` ("Home", "Settings").
- Footer: account row with avatar + email + chevron, then a muted version string.

### Badge / status pill
```html
<span class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs whitespace-nowrap">
  <span class="size-1.5 rounded-full bg-emerald-500"></span> running
</span>
```

### Table / list rows
- Prefer `divide-y` lists over real `<table>` for dashboards.
- Row: `flex items-center gap-4 px-5 py-3`, `hover:bg-muted/50`.
- Left: status dot + **bold 14px name** over 12px muted subtitle.
- Right: muted meta (status, relative time) then a `logs →` text link.
- Relative times everywhere ("3 minutes ago"), never raw timestamps in lists.

### Inputs
`h-9 w-full rounded-lg border border-input bg-background px-3 text-sm placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2`

---

## 8. Icons

- **Lucide React**, stroke width 2.
- Sizes: `size-4` (16px) inline/nav, `size-5` in headers.
- Icons are `text-muted-foreground` unless in an active/primary context.
- Every sidebar item has an icon; content headings may have one, buttons usually do not (except a trailing `→`).

---

## 9. Motion

- Transitions: `transition-colors duration-150` on all interactive elements.
- Sidebar collapse: `transition-[width] duration-200 ease-linear`.
- No bounce, no scale-on-hover, no decorative animation. Hover = background change only.

---

## 10. States

| State | Treatment |
|---|---|
| Loading | skeleton `bg-muted animate-pulse rounded-md` matching final shape |
| Empty | centered, `text-sm text-muted-foreground` + one primary CTA |
| Error | `text-destructive` text, `border-destructive/50` container |
| Disabled | `opacity-50 pointer-events-none` |
| Focus | `outline-none ring-2 ring-ring ring-offset-2 ring-offset-background` |

---

## 11. Accessibility

- Never convey status by dot color alone — always pair with a text label.
- All icon-only buttons need `aria-label`.
- Focus rings are mandatory; never `outline-none` without a replacement ring.
- `muted-foreground` at 12px is the smallest permitted text — don't go lower or lighter.

---

## 12. Do / Don't

**Do**
- Keep the UI monochrome; let data provide the color.
- Use borders, not shadows.
- Use uppercase 12px muted labels above metrics.
- Use `→` text links for secondary navigation.
- Keep the density high — 14px body, 12px meta, compact rows.

**Don't**
- Add brand color to buttons, nav, or headers.
- Mix radii (pick from the scale in §4).
- Use gradients, glassmorphism, or drop shadows on cards.
- Use headings larger than `text-3xl` anywhere except the page title.
- Hardcode colors outside the token set.
