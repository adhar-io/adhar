# tolgee — where every frontend application's text lives

**Status: enabled in the production profile, off in the local development core.**

[Tolgee](https://tolgee.io) is the platform's localisation server. A frontend
application does not keep its UI strings as the source of truth in its own
repository: it declares that it is localised, the platform gives it a Tolgee
project, CI keeps the project and the image in step, and the running app reads
the current translations at runtime — falling back to the ones bundled at build
time when Tolgee cannot be reached.

## The three halves

| Where | What happens |
|---|---|
| **Control plane** (Application compositions, step `localisation`) | For an `Application` whose `spec.parameters.localisation.provider` is `tolgee`, an idempotent Job (named by a hash of the declaration) ensures the Tolgee organisation (the Adhar organisation's slug, `adhar` by default), a project named after the application, the declared languages, and two project API keys, published as Secrets. |
| **CI** (`app-ci`, adhar-supply-chain) | When the repository has a `.tolgeerc` and `<app>-tolgee-ci` exists, a build of `main` runs `tolgee push --force-mode KEEP` (new keys from the source-language file; existing translations are never overwritten) and then `tolgee pull` into the path `.tolgeerc` declares, BEFORE the build — so the image bundles fresh translations. A repository without Tolgee config skips the stage without a pod. |
| **Runtime** (the app's own Deployment) | Reads `TOLGEE_API_URL`, `TOLGEE_PROJECT_ID`, `TOLGEE_API_KEY` from Secret `<app>-tolgee` with `optional: true`, so an app without localisation (or on a cluster without Tolgee) still starts and serves its bundled translations. |

### Opting in

In the application's `.adhar/app.yaml`:

```yaml
spec:
  parameters:
    localisation:
      provider: tolgee
      baseLanguage: en
      languages: [en, de, fr]
```

### The Secrets (the contract)

| Secret | Namespace | Keys | API key scopes |
|---|---|---|---|
| `<app>-tolgee-ci` | `adhar-system` | `api-url`, `project-id`, `api-key` | `keys.view`, `keys.create`, `keys.edit`, `translations.view`, `translations.edit`, `translations.state-edit` |
| `<app>-tolgee` | every environment namespace of the app (and a canonical copy in `adhar-system`) | `api-url`, `project-id`, `api-key` | `translations.view` |

`api-url` is the adhar-controlplane address, `http://tolgee.adhar-system.svc.cluster.local:8080`.
A browser cannot reach it: a single-page app fetches its translations through
its own server (or an nginx location that proxies to it), never by shipping the
key to the browser.

Keys are created ONCE. A re-run keeps every key that Tolgee still accepts and
mints a new one only when the Secret is missing or Tolgee refuses the stored
key (a rebuilt database). Nothing rotates them on a schedule.

## Sign-in

Keycloak, natively (generic OAuth2, `keycloak-client.yaml`). A person signed in
to the platform clicks **Log in with OAuth2** once and is back in Tolgee without
a password prompt; a first sign-in creates their Tolgee account. That click
cannot be removed at the edge: Tolgee's sign-in page generates the OAuth2
`state`, keeps it in localStorage, and rejects a callback carrying any other.

Password sign-UP is closed at the edge (`/api/public/sign_up` and `/sign_up`
redirect to `/login`). Password sign-IN stays on for the initial administrator
(`admin`, password in Secret `tolgee-admin-credentials`): the bootstrap Job
needs it, because Tolgee mints project API keys only for a password-authenticated
super-token, and it is the break-glass login.

People see an application's project once they are members of the Tolgee
organisation it lives in. Invite them from Tolgee (Organisation → Members);
Keycloak groups are not yet mapped to Tolgee organisations.

## Platform wiring

* Postgres through a `Database` XR (`tolgee-db`, CNPG, nightly backup).
* Files (screenshots, imports) in RustFS bucket `tolgee` (created by the rustfs package).
* Secret `tolgee-admin` {`api-url`, `username`, `password`, `pat`} — written by the
  PostSync Job in `admin-bootstrap.yaml`; the PAT is the platform's admin token.
* Telemetry to app.tolgee.io, the embedded Postgres and the demo project are off.
* `/actuator` is not routed to the edge; Prometheus scrapes it adhar-controlplane.

## Rolling out

The package is a stack push. The per-application provisioning lives in the
Application compositions and the extended Application XRD, which reach a cluster
with a **controller release**; until then an Application carrying
`localisation` is rejected by the old (strict) XRD, so release first, then add
`localisation` to descriptors.
