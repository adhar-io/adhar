# strapi — the platform's headless CMS

**Status: enabled in the production profile, off locally.**

## Why there is no "strapi" image here

Upstream **retired `strapi/strapi`** (404 on Docker Hub), and `naskio/strapi` —
the only maintained general-purpose replacement — ships a globally installed CLI
that cannot resolve its own dependencies:

```
$ docker run --rm --platform linux/amd64 naskio/strapi:5.30.1-alpine strapi version
Error: Cannot find module ...
  requireStack:
    .../@strapi/strapi/dist/src/node/staticFiles.js
    .../@strapi/strapi/dist/src/node/build.js
    .../@strapi/strapi/dist/cli.js
  code: 'MODULE_NOT_FOUND'
```

`strapi version` alone reproduces it, so no platform wiring is involved.
Reproduced on **5.30.1, 5.30.1-alpine, 5.28.0-alpine and 5.26.0-alpine**; the
newest published tag is still 5.30.1 (2025-11-08). That image can neither
scaffold a project nor start one.

## What this package does instead

It runs the **official Node image** (`node:22-bookworm-slim`) and performs the
two steps that image was meant to, using Strapi's own supported tooling:

1. **Scaffold, once per volume** — `npx create-strapi@latest` non-interactively
   (`--no-run --skip-cloud --use-npm --javascript --no-example --no-git-init`),
   with `--dbclient postgres` so the `pg` driver is installed. The generated
   `config/database.js` switches on `DATABASE_CLIENT`, so the real credentials
   come from the environment at runtime, not from the scaffold.
2. **Build the admin panel, then serve** — `strapi build` guarded by a version
   sentinel (`/srv/.admin-built`), then `strapi start`.

`node:*-alpine` is deliberately avoided: Strapi pulls native modules and the musl
build of `@swc/core` dies with **SIGBUS** during its postinstall.

## Verified end to end

On this cluster's amd64 nodes, before the package was switched on (2026-09-25):

| Step | Result |
|---|---|
| `create-strapi` scaffold | rc=0 |
| `strapi build` | rc=0, admin panel in 52 s |
| `strapi start` | "Strapi started successfully" |
| `GET /admin` | HTTP 200 |
| `GET /_health` | HTTP 204 |
| CNPG tables created | 41 in `public` |
| Scaffolded project size | 731 MiB (718 MiB node_modules) |
| npm cache size | ~1 GiB — kept OFF the PVC on purpose |

Also verified earlier and unchanged: `strapi-db` (CNPG, WAL archiving with gzip
compression, daily base backup), `strapi-db-credentials` and `strapi-secrets`
(six distinct generated values), the PVC binding, `strapi-oauth2-proxy` Keycloak
SSO and the `strapi.<host>` HTTPRoute.

## Operating notes

- **First boot takes four to six minutes** — scaffold, ~700 MiB of npm install,
  and an admin build, on a cold cache. The `startupProbe` budget is 20 minutes.
  Every later start finds the project on the volume and comes up in seconds.
- **Runs in development mode on purpose.** `NODE_ENV=production` disables
  Strapi's Content-Type Builder, which would leave an operator unable to define a
  single content type through the UI. `strapi start` is still used rather than
  `develop`, so there is no file watcher rebuilding under a running site.
- **The first administrator is created in the browser** on first visit, at
  `/admin`. Strapi has no unattended admin bootstrap that does not involve
  writing a password into the cluster.
- **`config/server.js` is managed** and rewritten on every start so the public
  origin follows the platform domain. The scaffolded default reads no URL
  variable at all, which is why absolute admin/media links were wrong before.
- A production CMS carrying **your own** content types is still better served by
  the golden path: scaffold a Strapi repo and let `app-ci` build your image.
  This package gives you a working CMS without that step.

## The console's API token

The Adhar Console's Develop → Content page uses Strapi's Content API, which
accepts only API tokens, and this Strapi's admin is Keycloak SSO only — so no
human or script can mint one through the admin. The package provisions it:

1. `manifests/console-token.yaml` generates a key once into Secret
   `strapi-api-token` (`STRAPI_TOKEN`, plus the in-cluster `STRAPI_URL`).
2. The boot script (step 3c) registers `HMAC-SHA512(API_TOKEN_SALT, key)` —
   exactly how Strapi stores tokens — as a full-access, non-expiring token
   named **Adhar Console**, and repairs it if it is edited or deleted. Look for
   `console token: registered` / `already registered` in the Strapi log.
3. The console Deployment loads the Secret (`envFrom`, optional), and the
   `strapi-console-reload` PostSync Job restarts the console once if it started
   before the Secret existed.

Rotate by deleting the `strapi-api-token` Secret and re-syncing: ESO mints a
new key, and the next Strapi start re-registers it.
