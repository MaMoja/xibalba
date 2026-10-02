---
name: challenge-page
description: Use when creating or changing Xibalba's challenge page, block page or any page a website visitor sees.
---

# Visitor-facing pages

This page is shown to real people who did nothing wrong, on the websites of
authorities and firms. It must be calm, clear and usable by everyone.

## Rules

- **Say what is happening in one plain sentence**, then what the visitor has to
  do (usually nothing) and how long it takes. No jargon, no jokes, no mascot.
- **Works without JavaScript.** The no-script path is a first-class route, not
  an error message telling people to enable JavaScript.
- **Accessible (WCAG 2.1 AA / BITV 2.0):**
  - correct `lang`, one `h1`, landmarks;
  - progress announced with a polite live region, not only shown visually;
  - full keyboard operation with visible focus;
  - contrast at least 4.5:1 in light and dark mode;
  - respects `prefers-reduced-motion`; no flashing, no auto-playing animation;
  - no time limit the visitor cannot extend.
- **No third-party requests.** Styles, scripts and images are inline or served
  by Xibalba. No external fonts.
- **Small.** The page should load fast on a slow phone connection.
- **Branding:** customer logo, name and one accent colour come from config.
  Check the accent colour's contrast and fall back to the default if it fails.
- **Languages:** German and English from the start, chosen from the request's
  `Accept-Language`, with a visible switch. All strings in translation files.
- **Blocked page:** says the request was blocked, gives a reference ID (no IP
  shown), and how to contact the site owner if the block is a mistake.

## Before finishing

1. Run an automated accessibility check and fix every finding.
2. Go through the page with the keyboard only.
3. Load it with JavaScript disabled and confirm a visitor can still get through.
4. Look at it at phone width, in light and dark mode, in both languages.
5. Confirm in the browser's network view that nothing loads from another host.
