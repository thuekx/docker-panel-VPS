# Nostek Panel — brand kit v2

Status: approved by the owner on 2026-11-11 (BRAND-1 = Nostek PANEL, teal colourway).

![Brand board](../assets/brand/png/brand-board.png)

## Rules

- The **N mark is the same in every variant**: same shape, same proportions. Only colours change.
- The NOSTEK wordmark keeps its accent middle bar in the E.
- Suffix text (PANEL, CONTROL PANEL, DOCKER PANEL) is set in Inter Display SemiBold (SIL Open Font Licence),
  converted to outlines in the SVG files.
- Keep clear space around the logo of at least half the mark's width.
- Do not stretch, rotate, outline or recolour the mark outside the palettes below.

## Variants

| File | Use |
|---|---|
| `nostek-panel-on-dark.svg` / `nostek-panel-on-light.svg` | Main logo (teal), transparent background (README, website) |
| `nostek-panel-dark.svg` / `nostek-panel-light.svg` | Main logo on its own background |
| `nostek-panel-blue-*.svg` | Alternative in the original NOSTEK blue |
| `nostek-panel-mono-*.svg` | One-colour versions |
| `nostek-mark.svg`, `nostek-app-icon.svg`, `nostek-favicon.svg`, `png/` | Icons |

"Control Panel" may be used as a descriptor in text. Do not use other companies' trademarks (for example the
name of a container engine) as part of the product name.

## Palette

| Name | Hex |
|---|---|
| Mint | `#5EF2D0` |
| Teal light (accent on dark) | `#2EE6C5` |
| Teal | `#0FB5A6` |
| Deep teal | `#0A6E8A` |
| Teal on light backgrounds | `#0B9483` |
| Navy (background / text) | `#0B1530` |
| Blue (alternative) | `#0A6CFF` |

Rebuild: `python3 assets/brand/src/build_brand.py assets/brand` (needs `fonttools` and Inter Display).
