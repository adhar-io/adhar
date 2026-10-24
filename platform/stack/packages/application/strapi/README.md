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
(every tag through 5.30.1). Before 5.57.0-2 this package therefore ran the plain
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

- **Starts in seconds.** The one exception is the first start on a volume from
  the old layout, which spends a minute or two deleting ~700 MiB of
  `node_modules` (startupProbe budget: 5 minutes).
- **The Content-Type Builder is editable.** It only accepts schema changes when
  `autoReload` is on, which only `strapi develop` sets. The previous
  `NODE_ENV=development` + `strapi start` therefore left it read-only.
  `STRAPI_ADHAR_MODE=develop` runs `develop` against the prebuilt admin, with
  no Vite and no rebuild, and restarts the server after a schema save.
- **Admin sign-in is Keycloak.** The `strapi-app` client (`sso.yaml`) is
  Strapi's own login, separate from the edge proxy's `strapi` client. New
  users are auto-registered as `strapi-editor`, and a Super Admin promotes
  people from Settings → Users.
- **The first administrator is still created in the browser,** at `/admin` on
  first visit. Strapi has no unattended admin bootstrap that doesn't involve
  writing a password into the cluster.
- **Upgrading** means a new fork release plus a new `image:` tag here (both
  containers, one tag; `TestStrapiRunsTheForksImageWithTheKeycloakAdminLogin`
  enforces that), and an `appVersion` bump in `adhar-package.yaml`.
- A production CMS with **your own** content types in Git is still better served
  by the golden path. That project can consume the fork's `@strapi/core` tarball
  from the same release (see the release notes) to keep admin SSO.

## The console's API token

The Adhar Console's Develop → Content page uses Strapi's Content API, which
accepts only API tokens. This Strapi's admin is Keycloak SSO only, so no
human or script can mint one through the admin. The package provisions it:

1. `manifests/console-token.yaml` generates a key once into Secret
   `strapi-api-token` (`STRAPI_TOKEN`, plus the in-cluster `STRAPI_URL`).
2. The image's bootstrap reads it as `ADHAR_CONSOLE_TOKEN` and registers it
   through Strapi's own api-token hash and query layer, as a full-access,
   non-expiring token named **Adhar Console**. It repairs the token if it is
   edited or deleted. Look for `[adhar] console token "Adhar Console"
   registered` / `repaired` in the Strapi log. (The boot script it replaces
   wrote the row with raw SQL, which left out `kind`, whose default lives in
   Strapi's query layer.)
3. The console Deployment loads the Secret (`envFrom`, optional), and the
   `strapi-console-reload` PostSync Job restarts the console once if it started
   before the Secret existed.

Rotate by deleting the `strapi-api-token` Secret and re-syncing: ESO mints a
new key, and the next Strapi start re-registers it.
