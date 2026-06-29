# Design

Seed visual system. Refine with `/impeccable document` once there is UI code, so the tokens below match the real implementation.

## Theme

Clean, warm, and high-contrast, built for a busy counter on a cheap bright screen. Light theme is primary because most shops are well lit. Generous spacing and oversized controls. The interface should feel calm and modern, locally rooted, and obviously trustworthy at a glance. Nothing decorative competes with the current action.

## Color

Light theme, warm neutrals with a confident green primary (trust, growth, money) and a warm amber accent for emphasis and success moments.

- Background: warm off-white, near `#FAF8F5`
- Surface: white `#FFFFFF` with soft warm shadows
- Text strong: near-black warm `#1A1714`
- Text muted: warm gray `#6B6259`
- Primary (green): `#0E7C5A` for the main action, with a darker pressed state
- Primary text: white on primary
- Accent (amber): `#E8A317` for highlights, totals, and positive confirmation
- Success: green family, reuse primary
- Warning: amber `#E8A317`
- Danger: clear red `#C7382D`, used sparingly and only for destructive or error states

Contrast is biased high for daylight legibility. Target WCAG AA or better on all text and controls. A dark theme can come later but is not required for v1.

## Typography

Performance first. Avoid heavy webfont payloads on Windows 7 and slow connections. The UI must render instantly even before any font loads, so the base text uses a pure system stack.

- Base stack: `system-ui, -apple-system, Segoe UI, Tahoma, Arial, sans-serif`. Zero download, instant paint, works offline.
- A distinctive display face for headings and totals will be selected during the design pass, then self-hosted and embedded in the binary so it stays offline-friendly. Avoid the overused defaults (Inter, Roboto, Plus Jakarta Sans, Space Grotesk, Geist, Fraunces); choose something with personality that still reads cleanly at counter distance.
- Base size large for counter reading, around 18px, scaling up for primary numbers.
- Totals and prices set large and bold so they read across the counter. Prefer tabular figures for prices.
- Limited scale: a small set of sizes, weights 400 and 600 and 700 only.

## Layout

- Single column, one task per screen. No dense multi-pane dashboards.
- Primary action anchored where a thumb naturally rests, large and unmistakable.
- Big tiles for products, big number pad for quantities and cash.
- Comfortable spacing scale, nothing cramped. Touch targets at least 48px.
- Responsive down to small budget touchscreens and up to a desktop monitor.

## Motion

Subtle and supportive. Quick state feedback on taps, gentle confirmation when a sale completes or a receipt prints. Respect reduced-motion. No motion is ever required to understand what happened.

## Components (early)

- Big primary button (the one obvious next action per screen)
- Product tile (name, price, tap to add)
- Cart line item with quantity stepper
- Number pad for quantity and cash tendered
- Printer status chip that speaks plainly ("Printer ready", "Looking for your printer")
- Receipt preview that mirrors the printed output
