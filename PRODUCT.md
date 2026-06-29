# Product

## Register

product

## Users

Small retail operators in Kenya: duka and shop owners, kiosk attendants, pharmacy and hardware counter staff, salon and eatery cashiers. Most are not technical. Many have never configured a printer or an IP address and never should have to.

Their context when using this:
- Standing at a busy counter, often serving a customer in real time.
- On low-end or aging hardware, including Windows 7 machines with little RAM.
- With unreliable internet and unreliable mains power. The tool must keep working when both are down.
- In bright daylight or poorly lit rooms, sometimes on a small or cheap touchscreen.

The job to be done: ring up a sale, take cash, and hand the customer a printed receipt, quickly and without thinking about technology.

## Product Purpose

A lean point of sale that runs as a local web app served by a tiny background agent on the shop's own computer. It auto-detects the thermal receipt printer on the network or over USB, records every sale offline, and prints a clean receipt.

Why it exists: existing POS software in this market is either heavy and expensive, requires constant internet, or is too technical for the people who actually stand at the counter. This product is deliberately small, offline-first, and self-explanatory so that the owner can set it up alone and rarely if ever needs a site visit.

What success looks like:
- A non-technical owner unboxes a machine, opens the app, and is selling within minutes without typing an IP or port.
- A full day of sales completes with no internet and no data loss.
- Site visits and support calls drop close to zero because the common path never confuses anyone.

Scope is staged on purpose:
- v1 (lean): auto-detect printer, sell items, cash payment with change, print 58mm and 80mm ESC/POS receipts, save sales offline, view and reprint the day's sales.
- v2: M-Pesa via Daraja (STK push, Till and Paybill), and inventory and reporting depth.

## Brand Personality

Three words: approachable, dependable, effortless.

Voice and tone: plain, warm, and human. Speaks like a helpful neighbor, not a manual. English first, with Swahili labels where they reduce hesitation. Never uses technical jargon in front-facing text. Celebrates the small wins (a sale rung, a receipt printed) without being noisy.

Emotional goal: the owner should feel calm and in control, and quietly proud to be running something that looks and feels modern and well made. A customer watching over the counter should trust it on sight.

Locally rooted: this is a Kenyan product for Kenyan shops. It should feel like it belongs here, not like a foreign template translated at the last minute.

## Anti-references

- Cluttered enterprise POS dashboards crammed with tabs, grids, and tiny controls.
- Technical or jargon-heavy setup screens that ask for IP addresses, ports, or driver names.
- Generic bootstrap or admin-template look that feels templated and impersonal.
- Loud, gamified, or noisy interfaces that distract from a fast counter transaction.
- Anything that stops working the moment the internet drops.

## Design Principles

1. No jargon, ever. Front-facing text uses everyday language. The user never sees an IP, a port, or a protocol. No em dashes in any front-facing text.
2. One obvious next action. Every screen makes the single most likely action large, central, and unmistakable.
3. Works when the internet doesn't. Offline is the default assumption, not a degraded mode. Nothing critical depends on a connection.
4. Confirm by the real world. Setup is verified by something the user can see and hold, like paper coming out of the printer, not by a status code.
5. Forgiving by default. Big touch targets, easy undo, clear recovery from mistakes. The tool assumes a busy, distracted human and a cheap screen.

## Accessibility & Inclusion

- Target WCAG 2.1 AA contrast, biased higher for readability in bright daylight and on low-quality displays.
- Large, well-spaced touch targets sized for finger use on resistive and budget touchscreens.
- English-first interface with Swahili labels on the highest-traffic actions to reduce hesitation for non-English-comfortable staff.
- Respect reduced-motion preferences. Motion is supportive and subtle, never required to understand state.
- Must remain usable and responsive on low-spec hardware, including Windows 7 with limited RAM and an older browser engine. Performance is an accessibility requirement here, not a nicety.
