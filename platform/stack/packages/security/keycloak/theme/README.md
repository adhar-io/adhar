# Adhar Keycloak Login Theme

A production-quality, self-contained login theme with Adhar branding for
**Keycloak 26.x** (tested target: 26.7.1). It restyles the sign-in, register,
reset-password, OTP, and update-profile pages with the Adhar design system — a
centered card on the Adhar Console page surface, the gradient hexagon logo,
refined inputs, and the console's solid brand primary button.

> All files are **text** (SVG for images — no binary PNG) so the whole theme can
> be delivered as a Kubernetes `ConfigMap` and runs fully **offline / air-gapped**
> (no web-font or CDN fetches).

## Directory layout

```
theme/
└── adhar/
    └── login/
        ├── theme.properties                 # parent=keycloak.v2, layers our CSS + JS on top
        └── resources/
            ├── css/
            │   └── adhar.css                 # the entire restyle (the heart of the theme)
            ├── js/
            │   └── adhar-theme.js            # light/dark resolution (shared cookie + toggle)
            └── img/
                ├── adhar-symbol.svg          # gradient hexagon symbol
                └── adhar-logo.svg            # symbol + ADHAR wordmark (login header)
```

`theme/test/adhar-theme.test.js` exercises the resolution script against a stub
DOM; run it with `hack/test-keycloak-theme-js.sh` (skips cleanly without Node).

Mounted into the pod it becomes `/opt/keycloak/themes/adhar/…`.

## What it styles

- **Background** — the Adhar gradient: three brand-ramp radials (blue 262 /
  cyan 200 / violet 292) over a vertical wash, on a fixed layer so it cannot
  scroll away from the taller register form. The console's own two faint radials
  read as "almost white" here, where the background is most of what you see.
  Overrides the parent theme's low-poly artwork, which is set on `.login-pf body`
  (the class is on `<html>`, not `<body>`).
- **Header** — the Adhar logo (`adhar-logo.svg`) + "Open Cloud-Native Foundation"
  tagline, injected **via CSS** (see below) so no template is overridden.
- **Card** (`.pf-v5-c-login__main`) — `surface-raised`, 16px radius, `edge-default`
  border, soft layered shadow. (NOT `.card-pf` — that is Keycloak's old v1 theme
  and does not exist in Keycloak 26's PatternFly v5 markup.)
- **Inputs** (`.pf-v5-c-form-control`) — 8px radius, console focus ring, on-brand
  autofill, invalid state. Keycloak wraps each input in a `<span>` that carries
  the *same* class, so the **wrapper** draws the field and the inner `input` is
  flattened to transparent; styling both is what produced the "double box" bug.
- **Primary button** (`.pf-m-primary`) — the console's solid `brand-600` with
  hover/active/focus states (the console uses a solid fill, not a gradient).
- **Secondary / social-provider buttons**, links, "remember me" checkbox,
  form-options row, info/registration area, alerts (danger/warning/success/info),
  per-field validation, locale switcher.
- **Footer** — the registration / "back to sign-in" band, with a single divider.
  (An earlier revision documented a CSS-injected "Built with ❤️" slogan. It was
  attached to `.login-pf-page::after`, an element Keycloak 26 does not render,
  so it never appeared on screen; it has been dropped rather than reinstated,
  since the console carries no equivalent strapline.)
- **Card width** — capped by `--ad-card-w` (448px for sign-in, 560px for the
  field-heavy register / update-profile pages, 100% below 600px). See the
  container note under *Caveats* for why this needs `grid-template-areas`.
- **Theme toggle** — a fixed pill in the top-right corner, injected by
  `adhar-theme.js`, writing the same shared cookie the console reads.
- **Responsive** (≤480px, plus a 600px step for the wider cards) and a
  **dark mode** driven by the resolved theme, not by `prefers-color-scheme`.

## Design tokens

| Token | Value | Use |
|-------|-------|-----|
| Brand 600 / 700 / 800 | `oklch(0.51 0.19 262)` / `oklch(0.44 0.18 262)` / `oklch(0.36 0.15 262)` | Primary button, links, hover/active |
| Surface app / raised / sunken | `oklch(0.985 0.003 260)` / `oklch(1 0 0)` / `oklch(0.975 0.005 260)` | Page / card / inset |
| Content / muted / subtle | `oklch(0.21 0.02 260)` / `oklch(0.48 0.015 260)` / `oklch(0.64 0.01 260)` | Titles+labels / captions / placeholders |
| Edge subtle / default / strong | `oklch(0.95 0.005 260)` / `oklch(0.91 0.008 260)` / `oklch(0.84 0.012 260)` | Dividers / borders / hover+focus borders |
| Radii | card 16px · input 8px · button 12px | Matches the console's `--radius-*` scale |
| Font | `'Inter', system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif` | No web-font fetch; Inter if installed, else system |

Every value is copied verbatim from `apps/console/app/styles.css` in the
adhar-console repo, so the two surfaces cannot drift apart by accident. Tokens
live as `--ad-*` custom properties at the top of `adhar.css` and are overridden
in the dark block, mirroring the console's `.dark` rules.

## How the logo/branding is injected — CSS-only, no FTL override

**We deliberately ship NO `login.ftl` / `template.ftl` override.** The theme sets
`parent=keycloak.v2`, so it inherits every base template and all form logic
(username/password, social providers, errors, "remember me", registration,
reset-credentials) unchanged — there is zero risk of breaking form rendering.

The logo and tagline are painted with CSS on the header element Keycloak already
renders (`#kc-header` / `#kc-header-wrapper`): the realm-name text is hidden
(`font-size:0`) and replaced with `background-image: url("../img/adhar-logo.svg")`;
the tagline and footer slogan are `::after` content. This is the most robust
approach across Keycloak point releases.

### `theme.properties`

```properties
parent=keycloak.v2
import=common/keycloak
styles=css/styles.css css/adhar.css
scripts=js/adhar-theme.js
```

`styles` **overrides** (does not merge with) the parent value, so we re-declare
the parent's `css/styles.css` (the keycloak.v2 PatternFly v5 bundle, resolved via
the parent chain) **first**, then append `css/adhar.css` **last** so our rules win
the cascade.

## Light / dark mode

The page follows **the Adhar Console's choice**, not the operating system.

Keycloak 26 has its own dark mode (`darkMode=true` + `kcDarkModeClass=pf-v5-theme-dark`
in keycloak.v2, with an inline `blocking="render"` script in `template.ftl`), but it
keys off `prefers-color-scheme` — as this stylesheet used to. Both therefore tracked
the OS, which is why picking dark in the console on a light-mode laptop left this page
white.

`resources/js/adhar-theme.js` resolves the mode instead:

1. `?theme=light|dark|system` on the URL, if present (and persists it) — this is the
   hand-off hook for the console when it redirects to login;
2. otherwise the **`adhar-theme` cookie**;
3. otherwise `system`, which defers to the OS.

The cookie is set on the **parent domain** (`keycloak.platform.adhar.io` →
`.platform.adhar.io`), because the console and Keycloak are different origins and
cannot share `localStorage`. It is `path=/`, `samesite=lax`, `secure` on HTTPS, and
host-only when there is no parent to share with (a bare host, a two-label domain, or
an IP).

The script stamps `data-adhar-theme` on `<html>` and keeps Keycloak's own
`pf-v5-theme-dark` class in step, so the inherited PatternFly component styles can
never disagree with our tokens. Every dark rule in `adhar.css` is therefore keyed on:

```css
:root[data-adhar-theme='dark'],
:root.pf-v5-theme-dark:not([data-adhar-theme='light']) { … }
```

which is correct in all four cases — explicit dark, explicit light on a dark OS,
system mode, and **no JavaScript at all** (Keycloak's own script still sets the class
from the OS, and the attribute is simply absent). There is deliberately no
`prefers-color-scheme` rule left in the file; keeping one meant the link colours
stayed OS-keyed and an explicit light choice put pale `brand-300` links on a white
card.

> **The console side is a one-line change and is not in this repo.** For the two
> surfaces to agree, `adhar-console` must write the same cookie when its theme
> changes:
>
> ```js
> document.cookie = `adhar-theme=${mode};domain=.${location.hostname.split('.').slice(1).join('.')};path=/;max-age=31536000;samesite=lax;secure`;
> ```
>
> Until it does, the login page honours its own toggle and otherwise follows the OS.

### Toggle

`adhar-theme.js` injects a small pill button into `#kc-header`, fixed to the
top-right of the viewport (so it cannot shift the logo, and stays reachable on the
tall register form). It writes the same cookie, so a choice made on the login page is
the choice the console will read.

## Selectors targeted

To be robust across Keycloak 24–26 the CSS targets, for each element, the stable
Keycloak **element IDs**, the **native input types**, AND **both** PatternFly
class generations:

| Element | Selectors |
|---------|-----------|
| Page / bg | `body.login-pf`, `.login-pf-page`, `#kc-container` |
| Header/logo | `#kc-header`, `#kc-header-wrapper` (`::after` = tagline) |
| Card | `.card-pf`, `#kc-content`, `#kc-content-wrapper` |
| Title | `#kc-page-title`, `.login-pf-header h1` |
| Form | `#kc-form`, `#kc-form-login` |
| Inputs | `input[type=text\|password\|email\|…]`, `.pf-v5-c-form-control`, `.pf-c-form-control` |
| Labels | `label`, `.pf-v5-c-form__label`, `.pf-c-form__label` |
| Primary button | `#kc-login`, `input[type=submit]`, `button[type=submit]`, `.pf-v5-c-button.pf-m-primary`, `.pf-c-button.pf-m-primary`, `.btn-primary` |
| Secondary btn | `.pf-v5-c-button.pf-m-secondary\|.pf-m-default`, `.btn-default` |
| Checkbox | `input[type=checkbox]`, `.pf-v5-c-check__input`, `.pf-c-check__input` |
| Options row | `#kc-form-options` |
| Social IdPs | `#kc-social-providers`, `a.zocial` |
| Info/register | `#kc-info`, `#kc-registration`, `#kc-registration-container` |
| Alerts | `.pf-v5-c-alert.pf-m-{danger,warning,success,info}`, `.pf-c-alert.*`, `.alert-*` |
| Field error | `.pf-v5-c-form__helper-text.pf-m-error`, `#input-error`, `.kc-feedback-text` |
| Footer | `.login-pf-page::after` (slogan) |

`!important` is used on key properties because the inherited `css/styles.css`
bundle carries high-specificity rules that must be overridden.

## Deploying — ConfigMap mount (for the caller)

Keycloak loads themes from `/opt/keycloak/themes/<name>`. Because ConfigMap keys
cannot contain `/`, mount each file to its exact path with a **`subPath` file
mount** (this creates parent dirs and does not mask sibling files).

> ⚠️ **Do not edit `install.yaml` here** — this snippet is for the caller to wire.

### 1. One ConfigMap holding all theme files (flat, unique keys)

In this repo the ConfigMap is generated by
`hack/gen-keycloak-theme-configmap.sh` and mounted by `install.yaml.tmpl`; the
kustomize form below is for callers wiring the theme into their own chart.

```yaml
# kustomization.yaml
configMapGenerator:
  - name: keycloak-theme-adhar
    namespace: adhar-system
    files:
      - theme/adhar/login/theme.properties
      - theme/adhar/login/resources/css/adhar.css
      - theme/adhar/login/resources/img/adhar-symbol.svg
      - theme/adhar/login/resources/img/adhar-logo.svg
generatorOptions:
  disableNameSuffixHash: true
```

### 2. Mount into the Keycloak Deployment

```yaml
spec:
  template:
    spec:
      containers:
        - name: keycloak
          volumeMounts:
            - name: theme-adhar
              mountPath: /opt/keycloak/themes/adhar/login/theme.properties
              subPath: theme.properties
              readOnly: true
            - name: theme-adhar
              mountPath: /opt/keycloak/themes/adhar/login/resources/css/adhar.css
              subPath: adhar.css
              readOnly: true
            - name: theme-adhar
              mountPath: /opt/keycloak/themes/adhar/login/resources/img/adhar-symbol.svg
              subPath: adhar-symbol.svg
              readOnly: true
            - name: theme-adhar
              mountPath: /opt/keycloak/themes/adhar/login/resources/img/adhar-logo.svg
              subPath: adhar-logo.svg
              readOnly: true
      volumes:
        - name: theme-adhar
          configMap:
            name: keycloak-theme-adhar
```

> Alternative (if you prefer whole-directory mounts): make three ConfigMaps and
> mount them at `…/adhar/login`, `…/login/resources/css`, `…/login/resources/img`.
> The single-ConfigMap `subPath` form above is simpler and the recommended path.

### 3. Point the realm at the theme

Set the login theme on the realm (in the realm import / `keycloak-config`, or
Admin Console → **Realm settings → Themes → Login theme → `adhar`**):

```json
{
  "realm": "adhar",
  "loginTheme": "adhar"
}
```

## Iterating on the theme

Keycloak runs with `start-dev` here, which **disables theme caching**, so edits
appear on refresh. If you run a production build, temporarily disable caching to
iterate:

```
--spi-theme-cache-themes=false --spi-theme-cache-templates=false --spi-theme-static-max-age=-1
```

Workflow:

1. Edit `adhar.css` (or the SVGs) in this directory.
2. Re-apply the ConfigMap (`kubectl apply -k .` or push via GitOps).
3. `kubectl rollout restart deploy/keycloak -n adhar-system` (needed because
   `subPath` mounts do **not** live-update).
4. Hard-refresh the login page (bypass browser CSS cache).

## Keycloak 26 compatibility notes / caveats

- The base login theme in Keycloak 24–26 is **keycloak.v2** (PatternFly v5). We
  inherit it via `parent=keycloak.v2` and its `styles=css/styles.css` bundle. If a
  future patch renames that bundle, update the first entry in `theme.properties`.
- `install.yaml.tmpl` pins **Keycloak 26.7.1** (an earlier revision of this note
  claimed 22.0.3, which stopped being true). That is PatternFly v5, so the
  `.pf-v5-c-*` selectors are the live ones; a few `.pf-c-*` (v4) selectors remain
  for graceful degradation only.
- **`.pf-v5-c-login__container` needs `grid-template-areas`, not just
  `grid-template-columns`.** At `min-width: 1200px` — i.e. on essentially every
  desktop — PatternFly makes the container a two-column named grid:

  ```css
  grid-template-areas: "main header" "main footer" "main .";
  grid-template-columns: 34rem minmax(auto, 34rem);
  padding-inline: 6.125rem;              /* 98px EACH side */
  ```

  Overriding only `grid-template-columns` to one column does **not** collapse that:
  a two-column area template with one explicit column gets an implicit second
  column, so `header` (the 260px logo) was laid out *beside* the card rather than
  above it, and the inherited 196px of inline padding came off the width cap too.
  The card was squeezed into whatever was left — which is why it looked wrong on a
  desktop and fine on a phone. Always override the areas, the columns and
  `padding-inline` together.
- The ConfigMap in `manifests/theme-configmap.yaml` is **generated** from this
  directory by `hack/gen-keycloak-theme-configmap.sh` (`--check` verifies it is
  current). Do not hand-edit it: the two copies drifted once already, leaving
  `parent=keycloak` in the file and `parent=keycloak.v2` in the deployed
  ConfigMap. Every key the generator emits must also be listed in the volume
  `items` in `install.yaml.tmpl`, or the file will not exist in the pod.
- CSS-only logo injection depends on the header IDs `#kc-header` /
  `#kc-header-wrapper`, which have been stable across v4/v5 themes. If a future
  release changes them, only the logo/tagline placement would need a tweak — the
  form styling is unaffected.
- `subPath` ConfigMap mounts are not auto-refreshed by the kubelet; a pod restart
  is required after theme changes (documented above).
