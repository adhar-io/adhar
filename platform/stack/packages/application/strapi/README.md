# strapi — wired, correct, and blocked on a broken upstream image

**Status: present but DISABLED in every profile.** Everything in this package is
built and verified except the one thing the platform does not control: a working
Strapi server image.

## What works

Verified live on the GCP cluster (2026-09-25), in this order:

- `strapi-db` — a CNPG Postgres cluster, healthy, with WAL archiving to the
  platform object store, `wal.compression: gzip` and a daily base backup.
- `strapi-db-credentials` and `strapi-secrets` — generated once by the ESO
  `Password` generator; the six secrets Strapi v5 refuses to boot without
  (`APP_KEYS` ×4, `API_TOKEN_SALT`, `ADMIN_JWT_SECRET`, `TRANSFER_TOKEN_SALT`,
  `JWT_SECRET`, `ENCRYPTION_KEY`) come out as **six distinct values**.
- the PVC binds, the `wait-for-db` init container passes, the pod schedules.
- `strapi-oauth2-proxy` (Keycloak SSO) and the `strapi.<host>` HTTPRoute.

## What is broken, and where

The container then crash-loops, and the fault is inside the image:

```
$ docker run --rm --platform linux/amd64 naskio/strapi:5.30.1-alpine strapi version
Error: Cannot find module ...
  requireStack:
    .../@strapi/strapi/dist/src/node/staticFiles.js
    .../@strapi/strapi/dist/src/node/build.js
    .../@strapi/strapi/dist/cli.js
    .../@strapi/strapi/bin/strapi.js
  code: 'MODULE_NOT_FOUND'
```

The globally installed CLI cannot resolve its own dependencies, so it can neither
scaffold a project nor start one. Reproduced on **5.30.1, 5.30.1-alpine, 5.28.0-alpine
and 5.26.0-alpine** — `strapi version` is enough; the platform's wiring is not involved.

Upstream retired `strapi/strapi` (404 on Docker Hub), and `naskio/strapi` is the
only maintained general-purpose image, so there is currently nothing to point at.

## Why a package is the wrong shape anyway

Strapi is a framework: its deployable artifact is **an application you scaffold and
build**, which is why no canonical server image exists. That is exactly what this
platform's paved road does — scaffold a repo, `app-ci` builds it with buildpacks,
scans, signs, attests and deploys it through Argo CD and a canary Rollout. A CMS
with your own content types belongs there, with this package's database, secrets
and SSO wiring reused.

## To enable

1. Point `manifests/install.yaml` at an image whose `strapi version` works
   (a first-party image built from your own Strapi project is the reliable one).
2. If it is a pre-built project image, drop `NODE_ENV=development` — `strapi start`
   is correct for an image that already contains a built admin panel.
3. Flip `enabled: "true"` for `strapi` in `adhar-appset-<profile>.yaml` and the
   mirrored `environments/<profile>/config.yaml`.
