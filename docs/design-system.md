# MicroAgent — Design System

> Single source of truth for all UI decisions. Applies to the dashboard and the future landing page.

---

## Core Aesthetic

**Stripe / Linear** — engineered precision. Clean whites, strong typographic hierarchy, one sharp accent color. Feels like a product a senior engineer built and is proud of. Not flashy, not generic. Every element earns its space.

---

## Color Palette

### Neutrals (base)

| Token | Hex | Usage |
|-------|-----|-------|
| `background` | `#FFFFFF` | Page background |
| `surface` | `#F8F9FA` | Cards, sidebar, input backgrounds |
| `border` | `#E5E7EB` | Card borders, input borders, dividers |
| `border-strong` | `#D1D5DB` | Focused containers, hover borders |
| `text-primary` | `#111827` | Headings, body copy, labels |
| `text-secondary` | `#6B7280` | Subtitles, hints, muted metadata |
| `text-disabled` | `#9CA3AF` | Placeholder text, disabled states |

### Accent — Indigo

| Token | Hex | Usage |
|-------|-----|-------|
| `accent` | `#6366F1` | Primary buttons, active nav, chart lines, links |
| `accent-hover` | `#4F46E5` | Button hover state |
| `accent-light` | `#EEF2FF` | Active nav item background, selected row tint, badge fill |
| `accent-light-border` | `#C7D2FE` | Active nav item left border, selected card border |
| `accent-muted` | `#A5B4FC` | Secondary chart series, secondary badges |

### Semantic

| Token | Hex | Usage |
|-------|-----|-------|
| `success` | `#10B981` | Running status dot, positive trend |
| `success-light` | `#ECFDF5` | Success badge background |
| `warning` | `#F59E0B` | Quota warning (>80%) |
| `warning-light` | `#FFFBEB` | Warning badge background |
| `error` | `#EF4444` | Error state, stopped status |
| `error-light` | `#FEF2F2` | Error badge background |

---

## Typography

**Font family:** [Geist Sans](https://vercel.com/font) — geometric, modern, legible at all sizes.  
**Fallback stack:** `'Geist', 'Inter', system-ui, -apple-system, sans-serif`  
**Monospace (code, metrics, terminal output):** `'Geist Mono', 'JetBrains Mono', monospace`

| Scale | Size | Weight | Line height | Usage |
|-------|------|--------|-------------|-------|
| `heading-xl` | 32px | 700 | 1.2 | Landing page hero headings |
| `heading-lg` | 24px | 700 | 1.25 | Page titles in dashboard |
| `heading-md` | 18px | 600 | 1.3 | Card titles, section headings |
| `heading-sm` | 15px | 600 | 1.4 | Sub-section labels |
| `body` | 14px | 400 | 1.6 | Default UI text, form values |
| `caption` | 13px | 400 | 1.5 | Form labels, metadata, timestamps |
| `mono` | 13px | 400 | 1.5 | Token counts, cost values, code |

**Rule:** Headings are near-black (`#111827`). Secondary text is `#6B7280`. Never use pure black (`#000000`) for text.

---

## Spacing

8px base grid. All spacing values are multiples of 4px.

| Token | Value | Usage |
|-------|-------|-------|
| `xs` | 4px | Icon gaps, tight internal padding |
| `sm` | 8px | Badge padding, compact rows |
| `md` | 12px | Input padding, card inner padding |
| `lg` | 16px | Card padding, nav item padding |
| `xl` | 24px | Section gaps, form field spacing |
| `2xl` | 32px | Page padding, major section breaks |
| `3xl` | 48px | Landing page section padding |

---

## Border Radius

| Token | Value | Usage |
|-------|-------|-------|
| `radius-sm` | 4px | Badges, chips, small tags |
| `radius-md` | 6px | Inputs, buttons |
| `radius-lg` | 8px | Cards, dropdowns, modals |
| `radius-xl` | 12px | Large feature cards on landing page |
| `radius-full` | 9999px | Pill buttons, status dots |

---

## Shadows

Shadows are minimal and single-layer. No blur-heavy Material Design shadows.

| Token | Value | Usage |
|-------|-------|-------|
| `shadow-xs` | `0 1px 2px rgba(0,0,0,0.05)` | Inputs on focus |
| `shadow-sm` | `0 1px 3px rgba(0,0,0,0.08)` | Cards (default) |
| `shadow-md` | `0 4px 12px rgba(0,0,0,0.08)` | Dropdowns, modals |
| `shadow-lg` | `0 8px 24px rgba(0,0,0,0.10)` | Landing page feature cards on hover |

---

## Components

### Buttons

| Variant | Background | Text | Border | Hover |
|---------|-----------|------|--------|-------|
| **Primary** | `#6366F1` | `#FFFFFF` | none | `#4F46E5` |
| **Secondary** | `#F8F9FA` | `#111827` | `#E5E7EB` | `#F3F4F6` |
| **Ghost** | transparent | `#6366F1` | none | `#EEF2FF` bg |
| **Destructive** | `#EF4444` | `#FFFFFF` | none | `#DC2626` |

All buttons: `radius-md` (6px), `14px` font, `600` weight, `8px 16px` padding (medium), `8px 12px` (small).

### Inputs

- Border: `1px solid #E5E7EB`
- Radius: `radius-md` (6px)
- Padding: `8px 12px`
- Font: 14px, `#111827`
- Placeholder: `#9CA3AF`
- Focus ring: `2px solid #6366F1` with `2px offset`
- Background: `#FFFFFF`

### Cards

- Background: `#FFFFFF`
- Border: `1px solid #E5E7EB`
- Radius: `radius-lg` (8px)
- Shadow: `shadow-sm`
- Padding: `20px 24px`

### Badges / Status chips

- Radius: `radius-sm` (4px) for rectangular, `radius-full` for pill
- Font: 12px, 500 weight
- Colors use semantic tokens (success-light bg + success text, etc.)

### Navigation sidebar

- Width: 240px
- Background: `#FFFFFF`
- Right border: `1px solid #E5E7EB`
- Nav item height: 36px, `radius-md`, `12px 16px` padding
- Inactive: `#6B7280` text, transparent background
- Hover: `#F3F4F6` background, `#111827` text
- Active: `#EEF2FF` background, `#6366F1` text, `2px solid #6366F1` left border

---

## Charts (Tremor)

| Series | Color |
|--------|-------|
| Primary | `#6366F1` (indigo) |
| Secondary | `#A5B4FC` (indigo-300) |
| Tertiary | `#94A3B8` (slate-400) |
| Quaternary | `#CBD5E1` (slate-300) |

- Grid lines: `#F3F4F6` (very subtle)
- Axis labels: 12px, `#9CA3AF`
- Tooltip background: `#111827`, white text, `radius-md`
- No chart titles inside the chart area — use card headers above

---

## Tailwind Config (`tailwind.config.ts`)

```ts
import type { Config } from 'tailwindcss'

export default {
  content: ['./src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        accent: {
          DEFAULT: '#6366F1',
          hover:   '#4F46E5',
          light:   '#EEF2FF',
          muted:   '#A5B4FC',
        },
        surface: '#F8F9FA',
        border:  '#E5E7EB',
      },
      fontFamily: {
        sans: ['Geist', 'Inter', 'system-ui', 'sans-serif'],
        mono: ['Geist Mono', 'JetBrains Mono', 'monospace'],
      },
      borderRadius: {
        sm:  '4px',
        md:  '6px',
        lg:  '8px',
        xl:  '12px',
      },
      boxShadow: {
        xs:  '0 1px 2px rgba(0,0,0,0.05)',
        sm:  '0 1px 3px rgba(0,0,0,0.08)',
        md:  '0 4px 12px rgba(0,0,0,0.08)',
        lg:  '0 8px 24px rgba(0,0,0,0.10)',
      },
    },
  },
} satisfies Config
```

---

## Do / Don't

| Do | Don't |
|----|-------|
| Use `#6366F1` for the one interactive accent | Use multiple competing accent colors |
| Use whitespace generously | Cram content to fill space |
| Use `#6B7280` for secondary text | Use light gray text on white (contrast failure) |
| Use `shadow-sm` on cards | Stack multiple shadows |
| Keep border radius consistent (6–8px) | Mix sharp and very rounded elements |
| Use Geist Mono for all numeric values | Use proportional font for token/cost numbers |

---

## Stitch Screens

Generated reference screens are in the Stitch project **MicroAgent Dashboard** (project ID: `8737414901002071007`):

| Screen | ID |
|--------|----|
| Overview | `5d1814713e2a4906a4bb8b4d831373b9` |
| Metrics & Charts | `7447b3f90127451f813f39dc9c3f8853` |
| Settings — Provider | `15fc36c616d3417a9cb7eb8764d1f153` |
