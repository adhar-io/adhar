# Adhar Brand & Press Kit

Official brand assets for the Adhar project. All assets are SVG (infinitely scalable); export PNGs at the noted sizes where a platform requires raster uploads.

## Assets

### Logos (symbol + wordmark)

| File | Use |
|------|-----|
| `adhar-logo.svg` | Full logo, light backgrounds |
| `adhar-logo-dark.svg` | Full logo, dark backgrounds |
| `adhar-logo.png` | Raster fallback (used by in-repo templates) |

### Symbol only

| File | Use |
|------|-----|
| `symbol-color.svg` | Primary — gradient symbol, any background with enough contrast |
| `symbol-light.svg` | Monochrome slate (#0F172A), for light backgrounds / single-color print |
| `symbol-dark.svg` | Monochrome white, for dark backgrounds / single-color print |

### Wordmark only (no symbol)

| File | Use |
|------|-----|
| `wordmark-color.svg` | Primary — gradient "ADHAR" |
| `wordmark-light.svg` | Slate text for light backgrounds |
| `wordmark-dark.svg` | White text for dark backgrounds |

### Social & press banners

| File | Size | Platform |
|------|------|----------|
| `banner-github-social.svg` | 1280×640 | GitHub repository social preview |
| `banner-og.svg` | 1200×630 | Open Graph / generic link previews |
| `banner-x-header.svg` | 1500×500 | X (Twitter) profile header |
| `banner-linkedin.svg` | 1584×396 | LinkedIn page banner |

Export to PNG (platforms require raster):

```bash
# with librsvg (brew install librsvg)
rsvg-convert -w 1280 -h 640 banner-github-social.svg -o banner-github-social.png

# or with Inkscape
inkscape banner-github-social.svg -w 1280 -h 640 -o banner-github-social.png
```

> Banners use the Inter font with a `system-ui` fallback. For pixel-identical exports install [Inter](https://rsms.me/inter/) locally before converting.

## Color Palette

| Token | Hex | Use |
|-------|-----|-----|
| Brand Blue | `#3B82F6` | Gradient start, links, accents |
| Brand Indigo | `#6366F1` | Gradient midpoint |
| Brand Violet | `#8B5CF6` | Gradient end |
| Ink | `#0F172A` | Dark backgrounds, monochrome-light assets |
| Slate | `#1E293B` | Dark background gradient end |
| Text primary (dark bg) | `#E2E8F0` | Headlines on dark |
| Text secondary (dark bg) | `#94A3B8` | Supporting text on dark |
| Text muted | `#64748B` | Taglines, captions |

The brand gradient runs **Blue → Indigo → Violet**, left-to-right (or top-left → bottom-right on the symbol).

## Typography

- **Typeface**: [Inter](https://rsms.me/inter/) (open source, SIL OFL)
- Wordmark: weight 800, letter-spacing −2
- Headlines: 600 · Body/captions: 500

## Usage Guidelines

**Do**

- Prefer the full logo; use the symbol alone when space is tight (favicons, avatars)
- Keep clear space around the logo of at least the height of one hexagon
- Use the dark variants on dark backgrounds and light variants on light backgrounds
- Scale proportionally; minimum symbol size 24px

**Don't**

- Recolor, rotate, outline, or add effects to the logo
- Place the gradient logo on mid-tone or clashing gradient backgrounds
- Alter the wordmark's font, weight, or spacing
- Use the logo to imply endorsement of third-party products

## Naming

- The project is written **Adhar** (Sanskrit: अधार, *Adhāra* — foundation); all-caps **ADHAR** appears only in the wordmark
- First mention in press: "Adhar, the open foundation for cloud-native platform engineering"
- Slogan: **"Adhar • Built with ❤️ for developers!"**

## Boilerplate (for press)

> Adhar is a 100% open-source Internal Developer Platform that provisions a complete, production-grade cloud-native platform with a single command. It integrates 50+ CNCF and open-source services — GitOps, security, observability, and self-service infrastructure — across AWS, Azure, GCP, DigitalOcean, Civo, or local clusters. Adhar is licensed under Apache 2.0 and maintained by Adharlabs. Learn more at [www.adhar.io](https://www.adhar.io) or [github.com/adhar-io/adhar](https://github.com/adhar-io/adhar).

## License & Contact

Brand assets may be used to reference the Adhar project (articles, talks, integrations). They are not licensed for use as your own product branding. Questions: press@adhar.io · [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww)
