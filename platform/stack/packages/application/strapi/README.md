# strapi: the platform's headless CMS

**Status: enabled in the production profile, off locally.**

## The image is the platform's own

The package runs `ghcr.io/adhar-io/strapi:<strapi version>-<n>`, built from
the [adhar-io/strapi](https://github.com/adhar-io/strapi) fork (`adhar/docker/`,
released by `.github/workflows/adhar-release.yml`; the runbook is the fork's
`adhar/README.md`). The image is multi-arch (amd64 and arm64) and
Cosign-keyless signed. It runs as uid 1000.

Upstream retired `strapi/strapi`, and `naskio/strapi`, the only maintained
replacement, ships a CLI that fails with `MODULE_NOT_FOUND` on `strapi version`
(every tag through 5.30.1). Before 5.57.0-3 this package therefore ran the plain
Node image and, on first boot, scaffolded a project, ran npm install (~700 MiB)
and built the admin panel inside the pod. That took four to six minutes, and
an interrupted install could poison the volume. The fork's image does all of it
at build time:

| Baked in | What it does |
| --- | --- |
| `create-strapi@<version>` project | JavaScript, `pg` driver, `config/database.js` reading `DATABASE_*` |
| Fork's `@strapi/core` (npm override) | Admin SSO without a licence. Only `sso` is in `ADHAR_BUNDLED_FEATURES`, so audit logs, review workflows and content releases stay locked. The build fails without it. |
| `config/server.js` | `url` from `PUBLIC_URL`, `proxy` on, update check off |
| `config/admin.js` | Keycloak `passport-oauth2` strategy (uid `keycloak`) from `KEYCLOAK_*` |
| `src/index.js` | Turns on SSO auto-register with `STRAPI_SSO_DEFAULT_ROLE`, and registers the console's API token (below), on every boot |
| Prebuilt admin panel | Relative backend URL, so one image serves any domain |
| One version of every `@strapi/*` package | Stock `create-strapi` hoists `@strapi/admin`/`utils`/`types` at the newest 5.x beside the pinned ones (plugins peer-depend on `^5.0.0`). Two `@strapi/utils` turn every 403 into a 500. The image pins them and fails to build on drift. |

## What lives on the volume

`/srv/app/src/{api,components,extensions}` holds what the Content-Type Builder
writes, and `/srv/app/public/uploads` holds media. The image symlinks those
paths into `/srv`. They are the same paths the self-scaffolding layout used,
so an existing PVC carries over. On its first start the image migrates that
layout once (marker: `/srv/.adhar-image-layout`). It deletes the derived files
(`node_modules`, build output, `/srv/.admin-built`) and moves the rest of the
old scaffold (`package.json`, `config/`, `.env`, `src/index.js`) to
`/srv/legacy-scaffold`. Delete that directory once you're satisfied nothing in
it was hand-edited.

## Operating notes

- **First boot takes four to six minutes** — scaffold, ~700 MiB of npm install,
  and an admin build, on a cold cache. The `startupProbe` budget is 20 minutes.
  Every later start finds the project on the volume and comes up in seconds.
- **Runs in development mode on purpose.** `NODE_ENV=production` disables
  Strapi's Content-Type Builder, which would leave an operator unable to define a
  single content type through the UI. `strapi start` is still used rather than
  `develop`, so there is no file watcher rebuilding under a running site.
- **There is no "register admin" page and no Strapi password for people.**
  The platform creates the first Super Admin itself and signs users in with
  their Keycloak session — see *Admin sign-in* below.
- **`config/server.js` is managed** and rewritten on every start so the public
  origin follows the platform domain. The scaffolded default reads no URL
  variable at all, which is why absolute admin/media links were wrong before.
- A production CMS carrying **your own** content types is still better served by
  the golden path: scaffold a Strapi repo and let `app-ci` build your image.
  This package gives you a working CMS without that step.

## The console's API token

The Adhar Console's Develop → Content page uses Strapi's Content API, which
accepts only API tokens. This Strapi's admin is Keycloak SSO only, so no
human or script can mint one through the admin. The package provisions it:

1. `manifests/console-token.yaml` generates a key once into Secret
   `strapi-api-token` (`STRAPI_TOKEN`, plus the adhar-controlplane `STRAPI_URL`).
2. Strapi's bootstrap (`src/adhar/console-token.js`, from
   `manifests/platform-extension.yaml`) registers
   `HMAC-SHA512(API_TOKEN_SALT, key)` — exactly how Strapi stores tokens — as a
   full-access, non-expiring Content API token named **Adhar Console**, through
   Strapi's own query layer, and repairs it if it is edited or deleted. Look
   for `[adhar] console token: registered` / `already registered` / `repaired`
   in the Strapi log. (The earlier raw-SQL version failed on 5.57 with an
   `expires_at` type error, and where it did insert it left `kind` empty, which
   5.57 rejects with 401 — so before 2026-10-24 the console's token never
   actually worked on this version.)
3. The console Deployment loads the Secret (`envFrom`, optional), and the
   `strapi-console-reload` PostSync Job restarts the console once if it started
   before the Secret existed.

Rotate by deleting the `strapi-api-token` Secret and re-syncing: ESO mints a
new key, and the next Strapi start re-registers it.

## Admin sign-in (Keycloak) — 2026-10-24

Opening `https://strapi.<host>/admin` signs you in to Keycloak once (at the
`strapi-oauth2-proxy`, as for every platform UI) and lands you in the Strapi
admin panel. There is no Strapi login form and no `/admin/auth/register-admin`.

### How it works

| Piece | Where | What it does |
|---|---|---|
| Unattended first admin | `src/adhar/bootstrap-admin.js` | On every start: if no admin exists, creates `strapi-bootstrap@adhar.internal` as Super Admin via Strapi's own `createFirstAdmin`; keeps it active, Super Admin, and on the password in Secret `strapi-bootstrap-admin`. Register-admin therefore never appears. |
| Token forwarding | `sso.yaml` (`--pass-access-token`, `--cookie-refresh=5m`) | The proxy forwards the user's Keycloak access token as `X-Forwarded-Access-Token` and keeps it fresh. |
| Sign-in bridge | `src/adhar/sso.js`, `GET /admin-sso` | Verifies that token, finds/creates the Strapi admin user by email, syncs roles from groups, mints a session with Strapi's own `sessionManager('admin')` (refresh cookie + access cookie, exactly what Strapi's Enterprise SSO callback hands the panel), returns to `/admin`. |
| Login-page skip | `src/adhar/sso.js` | A full page load of `/admin/**` with no Strapi session for *this* Keycloak user is redirected to `/admin-sso`; `/admin-sso/guard.js`, injected into the admin HTML, does the same for the panel's client-side jumps to `/auth/login` (session expiry). |

The code ships as ConfigMap `strapi-platform-extension`; the boot script copies
it into the project (`src/index.js`, `src/adhar/*`) on every start. Your own
lifecycle code goes in `src/index.local.js` (a pre-existing `src/index.js` was
moved there once). After editing the ConfigMap, bump
`adhar.io/extension-revision` on the Deployment so the pod restarts.

### Role mapping (`ADHAR_SSO_ROLE_MAP` in install.yaml)

| Keycloak group | Strapi role |
|---|---|
| `platform-admin` | Super Admin |
| `platform-engineer`, `application-admin` | Editor |
| `platform-developer` | Author |
| anything else (`platform-viewer`, `base-user`, no group) | **no access** — a page says which groups grant it |

Re-applied on **every** sign-in: a group removed in Keycloak removes the role;
losing every mapped group deactivates the Strapi account (which also ends its
open sessions). Only the three role codes in the map are managed — a custom role
a Super Admin grants inside Strapi is kept. No default role on purpose: the realm
allows self-registration. Strapi sessions are capped at 8 h idle / 10 h (the
realm's SSO session), so changed groups take effect by the next sign-in.

### Trust boundary

Strapi's Service is reachable adhar-controlplane without the proxy — the console BFF and
tenant apps call the Content API there with API tokens, so a NetworkPolicy would
break them. `/admin-sso` therefore trusts **no** plain header
(`X-Forwarded-Email`/`-User`/`-Groups` are ignored). The only input is the
forwarded access token, and it must be signed by the realm's key (JWKS fetched
adhar-controlplane), issued by the realm's public issuer, issued **to the `strapi`
client** (`azp`, so another app's token is refused) and unexpired; `alg: none`
and HMAC algorithms are rejected. At the edge, oauth2-proxy v7 deletes any
client-sent `X-Forwarded-Access-Token` before injecting its own, so a signed-in
user cannot smuggle someone else's token through it either. The bootstrap
admin's email can never be signed in through Keycloak. The console's API-token
path is untouched.

### Logout

Strapi's **Log out** ends the Strapi session, then the guard ends the proxy's
session (`/oauth2/sign_out`) and shows `/admin-sso/signed-out` ("Open Strapi
again" / "Sign out of the whole platform"). It does not silently sign you back
in. Your Keycloak session — shared with the other platform apps — stays,
unless you choose the platform sign-out link.

### Break-glass

When Keycloak is down: `https://strapi.<host>/admin/auth/login?local=1` shows
Strapi's own login form for that browser session. Sign in as
`strapi-bootstrap@adhar.internal` with the password from Secret
`strapi-bootstrap-admin` (`adhar get secrets`). Rotate it by deleting the Secret
and restarting Strapi — the Secret is re-applied on every start. Visiting
`/admin-sso` returns the browser to Keycloak sign-in.

### Verified (2026-10-24)

Locally against Strapi 5.57.0 (SQLite and Postgres 16) behind the real
`oauth2-proxy:v7.7.1` with this package's flags and Keycloak 26, driven by
headless Chrome: Keycloak login → Strapi dashboard ("Hello Alice", Super Admin)
with no Strapi form; developer → Author, engineer → Editor, re-sync both ways,
no-group user refused and deactivated (old session 401); forged headers,
foreign-key / other-client / other-issuer / expired / tampered / `alg:none`
tokens all 401 (minted against a stub JWKS); a valid token smuggled through the proxy replaced by the
proxy's own; UI logout ends on the signed-out page; `?local=1` + bootstrap
password works; restarts idempotent; Secret rotation re-applied; console token
200 on the Content API.
