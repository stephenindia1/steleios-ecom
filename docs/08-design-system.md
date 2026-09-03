# Steleios — Design System

Engraved visual and interaction standards for the storefront and admin. **Normative: MUST / MUST NOT.**
Companions: [07-seo-and-ai-discoverability.md](07-seo-and-ai-discoverability.md) · [04-go-and-typescript-standards.md](04-go-and-typescript-standards.md) §12.

Status: draft · 3 September 2026

---

## 0. Direction

**Pastel, multi-colour.** Not one accent hue with tints, but a family of soft hues used systematically — a warm, approachable, food-and-home retail feel rather than a monochrome tech look. Icons are themed multi-colour too, so the illustration language matches the surface language.

**The constraint that shapes everything below:** pastels are, by definition, low-contrast. They are excellent as *surfaces, fills, and identity*, and unusable as *text colour, as the only carrier of meaning, or as a focus indicator*. This document keeps the pastel character while meeting WCAG 2.2 AA, by pairing every pastel surface with a dark "ink" of the same hue.

| ID | Rule |
|---|---|
| DS-001 | Pastels are surfaces and accents. Text, icons carrying meaning, borders that convey state, and focus rings take their colour from the paired **ink** token, never from the pastel itself. |
| DS-002 | `[LEGAL]` Body text meets **4.5:1** against its background; large text and UI components meet **3:1**. Every token pair in §2 is chosen to satisfy this and is verified in CI. |
| DS-003 | Colour is never the only signal. State is carried by an icon, a label, or a shape as well as a hue — for colour-blind users and for anyone on a washed-out screen. |
| DS-004 | Decorative hue (§2.1) and semantic colour (§2.2) are separate scales. A category's pastel MUST NOT be reused to mean "success", and green MUST NOT be assigned to a category. |

---

## 1. Tokens are the source of truth

| ID | Rule |
|---|---|
| DS-010 | All colour, spacing, radius, type and motion values live in one token file, `web/shared/styles/tokens.css`, as CSS custom properties. A hex value in a component is a defect. |
| DS-011 | Tokens are defined on bare `:root` for light, redefined under `@media (prefers-color-scheme: dark)` guarded by `:root:not([data-theme="light"])`, and again under `:root[data-theme="dark"]` so an explicit toggle wins in both directions. A colour whose only definition sits inside a media query never applies to viewers on the default "system" setting. |
| DS-012 | Components reference tokens only. Tailwind, if used, is configured **from** the token file rather than carrying its own palette. |
| DS-013 | Every token has a semantic name (`--surface-category-produce`), never a literal one (`--mint-100`). Renaming a hue must not require touching components. |

---

## 2. Palette

### 2.1 The pastel family — decorative and categorical

Six hues, each with **four** roles. The four exist because a pastel alone cannot do every job: it is a surface, and it needs a dark ink for text and a mid-tone "strong" for any boundary that carries meaning.

| Role | Purpose | Contrast requirement |
|---|---|---|
| `surface` | The tinted fill of a card, chip or panel | Perceivably off-white (≥1.10 vs `--bg`); no WCAG requirement |
| `border` | Decorative hairline on a tinted surface | **None.** See DS-023 |
| `strong` | Focus ring, input boundary, selected state, chart mark | **3:1** vs `--bg` (WCAG 1.4.11) |
| `ink` | Text and meaningful icons on the surface or the page | **4.5:1** (WCAG 1.4.3) |

**Light theme** — every value below is verified, with its measured ratio.

| Hue | `surface` | `border` | `strong` (vs bg) | `ink` (on surface / on bg) | Assigned to |
|---|---|---|---|---|---|
| Blush | `#F7DDE2` | `#EDC2CB` | `#BC6576` — 3.78:1 | `#8C3A4A` — 5.82 / 7.05 | Bakery, confectionery |
| Apricot | `#FBE4CE` | `#F2CBA6` | `#B06A2E` — 4.02:1 | `#8A4A1C` — 5.55 / 6.46 | Fruit, preserves |
| Butter | `#F7EBB4` | `#E9D88C` | `#8F7815` — 4.09:1 | `#6B5810` — 5.78 / 6.57 | Grains, oils, staples |
| Mint | `#D8EFE0` | `#B0DCC1` | `#2F8A60` — 4.04:1 | `#1E6B47` — 5.34 / 6.12 | Fresh produce, dairy |
| Sky | `#D7E8F7` | `#AFCFEB` | `#2E6F9E` — 5.12:1 | `#1B5680` — 6.24 / 7.39 | Beverages, frozen |
| Lilac | `#E4DCF4` | `#C8BAE6` | `#6B57A8` — 5.60:1 | `#4B3A8C` — 6.94 / 8.71 | Household, personal care |

**Dark theme** — hue identity is kept and the roles invert: the ink becomes the light tone, the surface a deep tinted neutral.

| Hue | `surface` | `border` | `strong` (vs bg) | `ink` (on surface / on bg) |
|---|---|---|---|---|
| Blush | `#332226` | `#4E343A` | `#D9899A` — 7.07:1 | `#F0B9C4` — 8.90 / 11.00 |
| Apricot | `#33261B` | `#4E3A28` | `#D69A62` — 7.65:1 | `#EFC091` — 8.80 / 11.16 |
| Butter | `#332F1B` | `#4A4327` | `#C4AD5E` — 8.39:1 | `#E3D189` — 9.17 / 12.15 |
| Mint | `#1C2B23` | `#2C4436` | `#63B98D` — 7.83:1 | `#96D9B3` — 9.07 / 11.38 |
| Sky | `#1B2733` | `#2A3E4F` | `#6FA3CC` — 6.88:1 | `#9CC8E8` — 8.55 / 10.47 |
| Lilac | `#272238` | `#3C3454` | `#9B87C9` — 5.93:1 | `#C3B2E8` — 7.91 / 9.60 |

| ID | Rule |
|---|---|
| DS-020 | Hue assignment to a category is **reference data**, versioned like any other configuration (BR-VER-01), not hard-coded in a component. |
| DS-021 | A category's hue is stable. Changing it re-teaches every returning customer, so it needs the same deliberation as renaming the category. |
| DS-022 | At most **two** decorative hues appear in one viewport region. Multi-colour is a system across the site, not confetti on a page. |
| DS-023 | A decorative `border` is **not** held to 3:1. WCAG 1.4.11 applies to components whose *identification* depends on their boundary; a tinted card is identified by its fill and its content, and forcing a 3:1 hairline onto a pastel surface destroys the pastel character for no accessibility gain. Wherever a boundary genuinely carries meaning — focus, selection, an input's edge, a validation state — the `strong` tone is used instead. Using `border` for those is a defect. |
| DS-024 | `Butter` at `#F7EBB4` is deliberately a shade deeper than the other surfaces. At the original `#F9F0C6` it measured 1.09:1 against `--bg` and read as an off-white rendering artefact rather than a colour. |

### 2.2 Semantic colour — meaning, never decoration

| Role | Light | Dark | Used for |
|---|---|---|---|
| Success | `#17724A` | `#4FBE86` | Paid, delivered, in stock |
| Warning | `#8F6012` | `#D9A441` | Low stock, expiring soon, pending action |
| Critical | `#A63A31` | `#E2796E` | Payment failed, out of stock, allergen alert |
| Info | `#1B5680` | `#9CC8E8` | Neutral notices |

| ID | Rule |
|---|---|
| DS-025 | `[LEGAL]` Allergen "contains" statements use **critical** semantics with an icon and explicit text, never a pastel chip alone (BR-ATR-14). |
| DS-026 | Semantic colours are used at full strength for text and icons; their pastel tints are used only as the surrounding surface. |

### 2.3 Neutrals

Neutrals carry a faint warm-violet bias so they sit with the pastel family rather than looking like a separate grey system.

| Token | Light | Dark |
|---|---|---|
| `--bg` | `#FAF8FB` | `#14121A` |
| `--surface` | `#FFFFFF` | `#1C1A24` |
| `--surface-raised` | `#F4F1F7` | `#241F2E` |
| `--border` | `#E4DFE9` | `#332C40` |
| `--text` | `#1A1720` | `#EFEAF3` |
| `--text-muted` | `#5C5468` | `#A79CB4` |

---

## 3. Icons

| ID | Rule |
|---|---|
| DS-030 | Icons are **multi-tone**: a primary shape in the hue's ink and a secondary fill in the hue's surface. One shared drawing style across the whole set — no mixing outline and filled families. |
| DS-031 | Icons are inline SVG delivered from one sprite. Icon **fonts are prohibited** — they break with font-blocking, are read aloud as gibberish by some screen readers, and cannot be multi-coloured cleanly. |
| DS-032 | Icon colours come from `currentColor` and CSS custom properties, so one SVG serves both themes and every hue. Hard-coded fills inside the sprite are prohibited. |
| DS-033 | Sizes are a fixed scale: 16, 20, 24, 32, 48 px. Icons are drawn on a 24px grid with a 1.75px stroke and optically aligned, not scaled arbitrarily. |
| DS-034 | A decorative icon carries `aria-hidden="true"`. An icon that is the only content of a control carries an accessible name. An icon MUST NOT be the only indicator of state (DS-003). |
| DS-035 | The sprite is versioned and tree-shaken per route: the storefront MUST NOT ship the admin icon set. |
| DS-036 | Category icons use their category's assigned hue (§2.1). Status icons use semantic colour (§2.2). The two sets are visually distinguishable at 16px. |

---

## 4. Type, space, shape, motion

| ID | Rule |
|---|---|
| DS-040 | Two families: a warm humanist sans for UI and body, a slightly higher-contrast face for display. Both loaded with `font-display: swap` and a real fallback stack; system fonts on the checkout critical path if the budget demands it. |
| DS-041 | A fixed type scale with `text-wrap: balance` on headings and a ~65-character measure for running text. |
| DS-042 | Spacing is a 4px base scale. Layout uses flex/grid `gap`; per-element margins that collapse or double are prohibited. |
| DS-043 | Radius scale: 6px controls, 12px cards, 999px pills. Elevation is two shadow steps only — pastels flatten under heavy shadow. |
| DS-044 | Border, fill, radius and shadow each mean "separate object" and are spent by role. One radius and one shadow stamped on every block flattens the hierarchy. |
| DS-045 | Motion is 120–200ms with an ease-out curve, and every transition is disabled under `prefers-reduced-motion`. |
| DS-046 | Focus is a visible 2px ring in the hue's ink with a 2px offset, on every interactive element. Removing the focus outline is prohibited without a replacement that is at least as visible. |

---

## 5. Enforcement

| ID | Rule |
|---|---|
| DS-050 | A contrast test runs in CI over every token pair in §2 and fails the build on any pair below its threshold (DS-002). |
| DS-051 | A stylelint rule rejects raw colour literals outside the token file (DS-010). |
| DS-052 | Component tests assert the accessible name of icon-only controls and the presence of a non-colour state indicator (DS-003, DS-034). |
| DS-053 | Both themes are screenshot-tested on the storefront's key routes; a token change that breaks dark mode fails before review. |
