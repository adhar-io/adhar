# airbyte — the platform's ELT / data-integration service

Chart `airbyte/airbyte` **1.9.3**, Airbyte Core (community edition) **2.0.x**
(images `airbyte/*:2.0.1`). `manifests/install.yaml` is generated: edit
`values.yaml` and run `./generate-manifests.sh`, never hand-edit it.

## Login model (2026-10-24): one Keycloak login, no Airbyte login

`https://airbyte.<domain>` → Cilium Gateway → **`airbyte-oauth2-proxy`**
(Keycloak client `airbyte`, realm `adhar`, `manifests/sso.yaml`) →
`airbyte-airbyte-server-svc:8001`. Someone already signed in to Keycloak goes
straight into the Airbyte workspace. No Airbyte password and no `/setup` page.

Why Airbyte's own auth is **off** (`global.auth.enabled: false`):

* Airbyte Core 2.x has two auth modes. **SIMPLE** is one shared instance-admin
  email and password. **NONE** has no login. OIDC, generic OIDC and Keycloak SSO
  are Enterprise-only and need a license. Core has no trusted-header (proxy
  auth) mode either, so Airbyte cannot use the identity that oauth2-proxy has
  already established.
* With SIMPLE on, every user passed Keycloak and then hit a second login with a
  shared password. Airbyte's docs name the alternative for this case: turn
  Airbyte's auth off when a proxy already authenticates users with your SSO.

What enforces access instead:

| Path | Control |
|---|---|
| Browser | The HTTPRoute's only backend is the oauth2-proxy, which needs a Keycloak session on every path. Airbyte reads no identity header, so client-sent `X-Forwarded-*` headers grant nothing. |
| In-cluster | `manifests/networkpolicy.yaml`: the server (:8001), the workload API server (:8007, also unauthenticated in mode NONE) and the connector builder (:8080, published as a NodePort by the chart) accept connections only from the oauth2-proxy, the console BFF (`app=console`, server only), Airbyte's own pods (`app.kubernetes.io/instance=airbyte`) and its job pods (`airbyte=job-pod`). Other namespaces get nothing. |
| Console | `/api/svc/airbyte` calls the server with no credential. The console Deployment no longer carries `AIRBYTE_CLIENT_ID/SECRET`, because in mode NONE there is no token endpoint to mint from. |

Trade-offs:

* Core is single-user. Every Keycloak user who passes the proxy acts as the
  one Airbyte admin, which is what the shared password meant in practice. To
  narrow who may open Airbyte, restrict the proxy (for example
  `--allowed-group=/platform-admin` in `sso.yaml`), not Airbyte.
* Anyone who can create pods in `adhar-system` can label a pod
  `app.kubernetes.io/instance=airbyte` and reach the API. That is the same
  trust level as reading the namespace's Secrets, which already includes the
  Airbyte database password.
* `instance_configuration/setup` and `applications/token` are anonymous
  endpoints upstream in every mode. The NetworkPolicy is what keeps them away
  from tenant workloads.

## First-run setup bootstrap

`manifests/setup.yaml` is a PostSync hook Job (curl 8.11.1, non-root,
read-only rootfs). It runs the same call the `/setup` page makes:

```
GET  /api/v1/instance_configuration          -> initialSetupComplete?
POST /api/v1/instance_configuration/setup    {email, organizationName: "Adhar",
     userName: "Adhar Admin", anonymousDataCollection: true,
     initialSetupComplete: true, displaySetupWizard: false}
```

* The email is `instance-admin-email` from `airbyte-config-secrets`
  (`manifests/auth.yaml`, `airbyte-admin@adhar.local`).
* `anonymousDataCollection: true` is Airbyte's "anonymize usage data" switch,
  so it is the privacy-preserving value. Note that the chart still hard-codes
  `TRACKING_STRATEGY: segment`.
* The Job is idempotent: it exits 0 once `initialSetupComplete` is true, and it
  re-checks on every sync. It never logs the email.

## Break-glass access to the Airbyte admin

1. **Keycloak is down or the proxy is broken.** Bypass the proxy with a
   port-forward. It needs only kube RBAC, and the NetworkPolicy does not block
   it:
   ```
   kubectl -n adhar-system port-forward svc/airbyte-airbyte-server-svc 8001:8001
   # open http://localhost:8001  (full admin, no login)
   ```
2. **Turning Airbyte's own login back on** (for example if the proxy must be
   removed). Set `global.auth.enabled: true` in `values.yaml`, run
   `./generate-manifests.sh`, and restore `AIRBYTE_CLIENT_ID/SECRET` in
   `core/adhar-console/manifests/install.yaml` (from `airbyte-auth-secrets`
   keys `instance-admin-client-id` / `instance-admin-client-secret`). Then
   update `TestTheConsoleCallsAirbyteWithoutClientCredentials`. The login is
   the setup email plus the stable password:
   ```
   adhar get secrets -p airbyte     # instance-admin-email / instance-admin-password
   ```

## Other notes

* Object storage is the platform RustFS/MinIO (`manifests/platform-minio.yaml`).
  `generate-manifests.sh` strips the chart's bundled MinIO.
* The bootloader is converted to a wave-1 Job, and all hook annotations on
  long-lived objects are removed. See `generate-manifests.sh` for why.
