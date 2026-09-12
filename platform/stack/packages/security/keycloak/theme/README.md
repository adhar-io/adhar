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
        ├── theme.properties                 # parent=keycloak.v2, layers our CSS on top
        └── resources/
            ├── css/
            │   └── adhar.css                 # the entire restyle (the heart of the theme)
            └── img/
                ├── adhar-symbol.svg          # gradient hexagon symbol
                └── adhar-logo.svg            # symbol + ADHAR wordmark (login header)
```

Mounted into the pod it becomes `/opt/keycloak/themes/adhar/…`.

## What it styles

- **Background** — the Adhar Console page surface: `--color-surface-app` plus
  the console's two radial texture gradients, so the login page and the console
  are visibly the same product. Overrides the parent theme's low-poly artwork,
  which is set on `.login-pf body` (the class is on `<html>`, not `<body>`).
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
- **Responsive** (≤480px) and a **dark-mode** variant via `prefers-color-scheme`.

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
inside the `prefers-color-scheme: dark` block, mirroring the console's `.dark`
rules. The console toggles dark mode with a class it cannot share with Keycloak,
so this theme follows the OS preference instead.

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
```

`styles` **overrides** (does not merge with) the parent value, so we re-declare
the parent's `css/styles.css` (the keycloak.v2 PatternFly v5 bundle, resolved via
the parent chain) **first**, then append `css/adhar.css` **last** so our rules win
the cascade.

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

Generate it straight from this directory (kustomize):

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
- The current repo `install.yaml` pins **Keycloak 22.0.3**; that older theme uses
  PatternFly v4 (`.pf-c-*`) — which is why the CSS targets **both** `.pf-c-*` and
  `.pf-v5-c-*`. The theme therefore degrades gracefully on 22, but is designed and
  intended for **26.7.1**.
- CSS-only logo injection depends on the header IDs `#kc-header` /
  `#kc-header-wrapper`, which have been stable across v4/v5 themes. If a future
  release changes them, only the logo/tagline placement would need a tweak — the
  form styling is unaffected.
- `subPath` ConfigMap mounts are not auto-refreshed by the kubelet; a pod restart
  is required after theme changes (documented above).
