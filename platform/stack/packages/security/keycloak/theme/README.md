# Adhar Keycloak Login Theme

A production-quality, self-contained login theme with Adhar branding for
**Keycloak 26.x** (tested target: 26.7.1). It restyles the sign-in, register,
reset-password, OTP, and update-profile pages with the Adhar design system — a
centered card on a branded ink background, the gradient hexagon logo, refined
inputs, and a gradient primary button.

> All files are **text** (SVG for images — no binary PNG) so the whole theme can
> be delivered as a Kubernetes `ConfigMap` and runs fully **offline / air-gapped**
> (no web-font or CDN fetches).

## Directory layout

```
theme/
└── adhar/
    └── login/
        ├── theme.properties                 # parent=keycloak, layers our CSS on top
        └── resources/
            ├── css/
            │   └── adhar.css                 # the entire restyle (the heart of the theme)
            └── img/
                ├── adhar-symbol.svg          # gradient hexagon symbol
                └── adhar-logo.svg            # symbol + ADHAR wordmark (login header)
```

Mounted into the pod it becomes `/opt/keycloak/themes/adhar/…`.

## What it styles

- **Background** — Adhar ink (`#0F172A`) with soft blue/indigo/violet radial
  glows; a 4px brand-gradient hairline across the top of the page.
- **Header** — the Adhar logo (`adhar-logo.svg`) + "Open Cloud-Native Foundation"
  tagline, injected **via CSS** (see below) so no template is overridden.
- **Card** (`.card-pf`) — white surface, 16px radius, soft layered shadow.
- **Inputs** (`.pf-v5-c-form-control` / native `input`) — 12px radius, brand-blue
  focus ring, on-brand autofill, invalid state.
- **Primary button** (`#kc-login`, `.pf-m-primary`) — the Blue→Indigo→Violet
  gradient with hover/active/focus states.
- **Secondary / social-provider buttons**, links, "remember me" checkbox,
  form-options row, info/registration area, alerts (danger/warning/success/info),
  per-field validation, locale switcher.
- **Footer** — "Adhar • Built with ❤️ for developers!" injected via CSS.
- **Responsive** (≤480px) and a **dark-mode** variant via `prefers-color-scheme`.

## Design tokens

| Token | Value | Use |
|-------|-------|-----|
| Brand gradient | `linear-gradient(135deg, #3B82F6 0%, #6366F1 50%, #8B5CF6 100%)` | Signature element: button, top hairline, logo |
| Brand Blue | `#3B82F6` | Links, focus ring, input focus border |
| Brand Indigo | `#6366F1` | Gradient midpoint, link hover |
| Brand Violet | `#8B5CF6` | Gradient end |
| Ink | `#0F172A` | Page background base |
| Text strong / body / muted | `#0F172A` / `#334155` / `#64748B` | Titles / labels / captions |
| Text on dark | `#E2E8F0` / `#94A3B8` | Header + footer text on the ink background |
| Radii | card 16px · input 12px · button 12px | Rounded, modern feel |
| Font | `'Inter', system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif` | No web-font fetch; Inter if installed, else system |

All tokens live as CSS custom properties at the top of `adhar.css` and are
overridden inside the `prefers-color-scheme: dark` block.

## How the logo/branding is injected — CSS-only, no FTL override

**We deliberately ship NO `login.ftl` / `template.ftl` override.** The theme sets
`parent=keycloak`, so it inherits every base template and all form logic
(username/password, social providers, errors, "remember me", registration,
reset-credentials) unchanged — there is zero risk of breaking form rendering.

The logo and tagline are painted with CSS on the header element Keycloak already
renders (`#kc-header` / `#kc-header-wrapper`): the realm-name text is hidden
(`font-size:0`) and replaced with `background-image: url("../img/adhar-logo.svg")`;
the tagline and footer slogan are `::after` content. This is the most robust
approach across Keycloak point releases.

### `theme.properties`

```properties
parent=keycloak
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
  inherit it via `parent=keycloak` and its `styles=css/styles.css` bundle. If a
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
