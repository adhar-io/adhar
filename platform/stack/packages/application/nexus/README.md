# Nexus Repository (Community Edition)

The platform's repository for language packages (Maven, npm, PyPI). Harbor
stays OCI-only. Image `sonatype/nexus3:3.79.1`, one replica, data on the
`nexus-data` PVC (`adhar-block`; see the comment in `manifests/install.yaml`
for why it is not node-local). Routed at `https://nexus.<host>`.

| File | What it does |
| --- | --- |
| `manifests/install.yaml` | PVC, Deployment, Service |
| `manifests/secrets-key.yaml` | Generates the secrets encryption key once (Sync hook, wave 0) |
| `manifests/config-job.yaml` | `bootstrap.sh`: admin password, service account, EULA, repositories, realms, SSO header capability, re-encryption, Keycloak users. Runs as a PostSync hook and every 10 minutes from the `nexus-reconcile` CronJob |
| `manifests/dashboard-sso.yaml` | Keycloak oauth2-proxy in front of the UI |
| `manifests/httproute.yaml` | Gateway route to the proxy |
| `manifests/servicemonitor.yaml` | Prometheus scrape, authenticated with `nexus-credentials` |

## How you log in

- **Browser:** Keycloak SSO. The oauth2-proxy authenticates you and passes
  `X-Forwarded-Preferred-Username`, which Nexus's Rut Auth realm trusts.
  `bootstrap.sh` creates a Nexus account for every Keycloak user. Members of
  `platform-admin` get `nx-admin`, viewers get `adhar-viewer`, and everyone
  else gets `adhar-developer`.
- **Break-glass `admin`:** uses the **platform admin password**, which is the
  same password as Keycloak's bootstrap admin:

  ```bash
  adhar get secrets -p keycloak   # "Keycloak (admin)" row: adhar-admin / <password>
  # Nexus: user `admin`, that same password
  ```

  It comes from Secret `keycloak-config`, key `KEYCLOAK_ADMIN_PASSWORD`.
- **Automation:** Secret `nexus-credentials` holds the `adhar-automation`
  account (`nx-admin`, random password) plus the repository URLs. The console,
  Tekton builds (`.npmrc` / Maven `settings.xml`), the repository composition
  and the ServiceMonitor all read it.

## Admin password

Nexus first boots as `admin`/`admin123` (`NEXUS_SECURITY_RANDOMPASSWORD=false`).
On every run, `bootstrap.sh` step 1b works out which password Nexus has right
now:

| Nexus currently accepts | Action |
| --- | --- |
| `admin` + platform admin password | none |
| `admin` + `admin123` | change it to the platform admin password (first run) |
| only `adhar-automation` from `nexus-credentials` | the platform password was rotated, or someone changed `admin` by hand: reset `admin` to the current platform password |
| none of these | fail loudly (see below) |

This clears **"Default Admin Credentials"** on the status page. Because the
CronJob runs every 10 minutes, rotating `KEYCLOAK_ADMIN_PASSWORD` is picked up
within 10 minutes with nothing else to do. `adhar-credential-rotation` does not
need to know about Nexus. If Keycloak is not installed, the password is left
alone and the run logs a warning.

`nexus-credentials` deliberately does **not** carry the platform admin
password. Its contents reach build workspaces that run developer code, so
automation uses its own Nexus-only account. If that account's password stops
working, the next run generates a new one, writes the Secret and restarts
`adhar-console`, which reads the Secret as env.

**Recovery.** If the job logs "no known credential opens the Nexus admin API",
neither `admin` nor `adhar-automation` opens Nexus any more. To fix it:

1. Reset `admin` to `admin123` with Sonatype's admin-password reset procedure
   for the embedded H2 database. Nexus 3.79 uses H2, not OrientDB.
2. Wait for the next CronJob run, or trigger one with
   `kubectl -n adhar-system create job --from=cronjob/nexus-reconcile nexus-reconcile-now`.

That run puts `admin` back on the platform password and re-issues
`adhar-automation`.

## Secrets encryption key

Nexus encrypts the secrets it stores, such as remote/proxy credentials and
capability passwords. Unless configured otherwise, it uses a default key that
ships inside every Nexus build. That default is what the **"Default Secret
Encryption Key"** status warning is about. Nexus has supported a custom key
since 3.73. The setup here:

1. `secrets-key.yaml` generates a 32-byte random key **once**, in-cluster,
   into Secret `nexus-secrets-key`. The Secret has two keys:
   `nexus.secrets.json` (`{"active":"adhar-YYYYMMDD","keys":[{"id":…,"key":…}]}`)
   and `active-key-id`. The Job only ever `create`s. Its RBAC has no
   update, patch or delete, so it cannot replace an existing key. No key
   material is in Git.
2. `install.yaml` mounts the file read-only and passes
   `-Dnexus.secrets.file=/etc/nexus-secrets/nexus.secrets.json`. New secrets
   are encrypted with it, and the warning clears.
3. Secrets that already exist stay under the default key. Nexus reports this as
   the "Re-encryption required" check. While that check is unhealthy,
   `bootstrap.sh` step 4b submits
   `PUT /service/rest/v1/secrets/encryption/re-encrypt {"secretKeyId": …}`. The
   check, not a marker file, decides whether to submit, so an interrupted run is
   resubmitted on the next one.

### ⚠️ Losing `nexus-secrets-key` is data loss

Once Nexus has re-encrypted with this key, it cannot decrypt its stored secrets
without it, and re-encryption cannot be undone.

- **Back it up together with the `nexus-data` volume.** Restoring the volume
  without the key gives you a Nexus that cannot read its own secrets:

  ```bash
  kubectl -n adhar-system get secret nexus-secrets-key -o yaml > nexus-secrets-key.backup.yaml
  ```

- Argo CD will not prune or delete it. It is created by a Job, so it has no
  tracking label and no ownerReference. It also carries
  `argocd.argoproj.io/sync-options: Prune=false,Delete=false` in case it is ever
  adopted into an Application.
- **Do not replace it with an ExternalSecret / Password generator.** ESO
  regenerates when the ExternalSecret is recreated, which would swap the key
  under a running Nexus.
- The Deployment mounts it **non-optionally**. Without it, the pod stays in
  `ContainerCreating` rather than starting on the wrong key.
- If it is gone for good: restore the backup. Failing that, re-enter every
  stored credential (proxy remote auth, etc.) after a fresh key is generated.

Rotating the key means adding a second key to the file, making it `active`,
restarting Nexus and re-encrypting to the new ID. That is a manual procedure;
nothing here rotates the key automatically.

## Rollout notes (2026-10-24)

On a cluster that already has Nexus, one sync does all of the following:

1. Creates the key (wave 0).
2. Restarts Nexus with the key (wave 1, `Recreate`, a few minutes of downtime).
3. Runs the PostSync bootstrap, which changes `admin`, creates
   `adhar-automation`, rewrites `nexus-credentials`, restarts the console and
   re-encrypts.

The static `nexus-metrics-auth` Secret (`admin/admin123`) was removed. The
ServiceMonitor now reads `nexus-credentials`.
