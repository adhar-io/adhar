# GCP provider

Adhar on Google Cloud: Compute Engine instances bootstrapped with kubeadm, the
same code path that is live-verified on DigitalOcean.

> **Status: render-verified, not live-verified.** Provider registration, config
> validation and `adhar up --dry-run` pass, and the provisioning code is shared
> with the DigitalOcean path that has run end to end. **No full run on GCP has
> been completed.** Treat your first use as a bring-up exercise. See
> [PROVIDER_GUIDE.md](PROVIDER_GUIDE.md#2-verification-status-what-is-proven-where)
> for the matrix, and the [DigitalOcean provider guide](DIGITALOCEAN_PROVIDER.md)
> for everything that is not cloud-specific.

| | |
|---|---|
| Provisioning model | kubeadm on Compute Engine instances (default); GKE via `useManagedK8s: true` |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| DNS | Cloud DNS, or any zone you point at the load balancer |

## 1. One-time Google Cloud setup

Do this once per project, before the first `adhar up`. A brand-new Google Cloud
project has every API switched off and a service account with no permissions, and
the failures that follow are indirect enough to be worth walking through in order.

### 1.1 Project and billing

```bash
PROJECT=adhar-cloud           # your project id
REGION=asia-southeast1        # pick one close to your users
ZONE=$REGION-a
```

Create the project and **attach a billing account** to it. Billing is not
optional and not only about cost: the Cloud DNS and Compute APIs refuse to serve
a project without it, and the error names billing only sometimes, so a
provisioning run can fail in a way that looks like a permissions problem.

Verify, as a user with access to the billing account:

```bash
gcloud billing projects describe $PROJECT --format='value(billingEnabled)'
# must print: True
```

### 1.2 Service account and key

Adhar needs a **service account key file**, not only Application Default
Credentials. Provisioning works from ADC alone, but the edge DNS step mounts a
key into external-dns and the cert-manager DNS-01 solver, so a deployment with a
real `defaultHost` fails at `configuring edge DNS (gcp)` without one.

```bash
SA=adhar@$PROJECT.iam.gserviceaccount.com

gcloud iam service-accounts create adhar \
  --project $PROJECT --display-name "Adhar platform provisioner"

gcloud iam service-accounts keys create ~/.config/gcloud/$PROJECT-adhar.json \
  --iam-account $SA --project $PROJECT
```

Keep the key out of Git. Point `serviceAccountKeyFile` at its path; `~` is
expanded.

### 1.3 IAM roles

Grant all four. The last one is the one that is easy to miss and the one whose
absence is hardest to read, because the resulting error blames the API rather
than the role:

```bash
for role in \
  roles/compute.admin \
  roles/dns.admin \
  roles/iam.serviceAccountUser \
  roles/serviceusage.serviceUsageAdmin
do
  gcloud projects add-iam-policy-binding $PROJECT \
    --member="serviceAccount:$SA" --role="$role" --quiet
done
```

| Role | Why |
| --- | --- |
| `roles/compute.admin` | Instances, disks, networks, subnets, firewall rules, addresses, forwarding rules, health checks, backend services |
| `roles/dns.admin` | external-dns writes one record per app; cert-manager writes the ACME TXT records |
| `roles/iam.serviceAccountUser` | Needed when the nodes themselves run as a service account |
| `roles/serviceusage.serviceUsageAdmin` | Lets the account enable and read the APIs below. Without it, `gcloud services enable` fails with `PERMISSION_DENIED: serviceusage.services.enable` while `gcloud services list` fails with `AUTH_PERMISSION_DENIED` — both of which read like the API is off rather than the role being absent |

**How to tell the two apart.** The distinction matters because the remedy is
different, and the message is not obvious:

```bash
gcloud services list --project $PROJECT        # AUTH_PERMISSION_DENIED -> role missing
gcloud compute zones list --project $PROJECT   # SERVICE_DISABLED       -> API off
```

### 1.4 Enable the APIs

```bash
gcloud services enable \
  serviceusage.googleapis.com \
  cloudresourcemanager.googleapis.com \
  compute.googleapis.com \
  container.googleapis.com \
  dns.googleapis.com \
  iam.googleapis.com \
  --project $PROJECT
```

Two traps here.

**`container.googleapis.com` is required even in kubeadm mode.** The provider
constructs its GKE client unconditionally, so a disabled Container API fails
provider construction before a single instance is created. It is not only for
`useManagedK8s: true`.

**Service Usage cannot bootstrap itself.** On a project where it has never been
used, nothing can enable it, including the command above — the API needed to turn
APIs on is itself off. Break the cycle once from the console, with a user account:

```
https://console.developers.google.com/apis/api/serviceusage.googleapis.com/overview?project=<PROJECT_NUMBER>
```

After that, everything else can be enabled from the CLI.

### 1.5 Quota

A new project commonly caps CPUs per region between 8 and 24. The default cluster
in this guide asks for 16 vCPU at rest and the autoscaler may push it past 32, so
a low cap shows up as a create that fails partway with instances already running.

```bash
gcloud compute regions describe $REGION --project $PROJECT \
  --format='table(quotas.metric,quotas.limit,quotas.usage)' | grep -E 'CPUS|DISKS|ADDRESSES'
```

Raise `CPUS` for the region before the first run if it is below 24.

### 1.6 Cloud DNS zone and registrar delegation

Nothing in Adhar creates the zone. Create it, then delegate the subdomain at your
registrar — this is the only step that happens outside Google Cloud.

```bash
HOST=cloud.example.com        # must equal globalSettings.defaultHost

gcloud dns managed-zones create adhar-platform \
  --project $PROJECT --dns-name "$HOST." \
  --description "Adhar platform zone for $HOST"

gcloud dns managed-zones describe adhar-platform \
  --project $PROJECT --format='value(nameServers)'
```

That prints four nameservers of the form `ns-cloud-XX.googledomains.com.`. At your
registrar, on the **parent** zone, add one `NS` record per nameserver for the
subdomain label only:

| Field | Value |
| --- | --- |
| Type | `NS` |
| Name / host | the subdomain label alone, e.g. `cloud` for `cloud.example.com` |
| Value | each of the four nameservers, one record each |
| TTL | 1 hour |

Nothing else at the registrar changes: the parent apex and its existing records
are untouched. Delegating a subdomain rather than moving the whole domain means a
mistake here cannot take your main site down.

Confirm delegation before deploying. It can take up to the parent zone's TTL:

```bash
dig +short NS $HOST      # must return the four googledomains.com nameservers
```

If you would rather not delegate, you can skip this section, set
`dnsProvider: none`, and add a wildcard `A` record for `*.<host>` by hand once the
Gateway has its external IP. You then give up automatic records and wildcard
certificates, and the platform stays on self-signed TLS.

### 1.7 Verify readiness

All five must pass before `adhar up`:

```bash
gcloud auth activate-service-account --key-file ~/.config/gcloud/$PROJECT-adhar.json
gcloud config set project $PROJECT

gcloud billing projects describe $PROJECT --format='value(billingEnabled)'  # True
gcloud services list --enabled --project $PROJECT | grep -E 'compute|container|dns'
gcloud compute zones list --project $PROJECT --limit 1                      # no SERVICE_DISABLED
gcloud dns managed-zones list --project $PROJECT                            # your zone
dig +short NS $HOST                                                          # googledomains
```

## 2. Credentials

```yaml
providers:
  gcp:
    type: gcp
    region: us-central1
    primary: true
    useApplicationDefault: true          # gcloud auth application-default login
    # serviceAccountKeyPath: /path/to/key.json
    # useComputeMetadata: true           # when Adhar runs on a GCE instance
    # useWorkloadIdentity: true
    # impersonateServiceAccount: adhar@project.iam.gserviceaccount.com
```

With a key file you may instead set the standard environment variable, which the
provider reads:

```bash
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/key.json
```

The service account needs Compute Admin (instances, disks, networks, subnets,
firewall rules, routers and Cloud NAT, addresses, forwarding rules, health
checks, backend services) and, for DNS, DNS Administrator on the zone. Add
Service Account User if the nodes themselves run as a service account.

**Application Default Credentials are not enough when `dnsProvider` is set.**
Provisioning works from ADC alone, but the edge DNS step needs a key file it can
mount into external-dns and the cert-manager DNS-01 solver, so a deployment with
a real `defaultHost` fails at `configuring edge DNS (gcp)` unless you give it
`serviceAccountKeyFile` (or `GOOGLE_APPLICATION_CREDENTIALS`) together with
`projectId`.

**Enable these APIs on the project before the first run**, because a brand-new
project has all of them off and the one that turns the others on is itself off:

```bash
# Service Usage first, from the console if the CLI cannot reach it:
#   https://console.developers.google.com/apis/api/serviceusage.googleapis.com/overview?project=<PROJECT>
gcloud services enable \
  serviceusage.googleapis.com cloudresourcemanager.googleapis.com \
  compute.googleapis.com container.googleapis.com \
  dns.googleapis.com iam.googleapis.com --project <PROJECT>
```

`container.googleapis.com` is required **even in kubeadm mode**: the provider
constructs its GKE client unconditionally, so a disabled Container API fails
provider construction before any instance is created.

A service account cannot enable Service Usage on a project where it has never
been used. Do that one from the console, or with a user account.

## 3. Configuration

```yaml
globalSettings:
  adharContext: adhar-mgmt
  defaultHost: platform.example.com
  defaultHttpPort: 80
  defaultHttpsPort: 443
  enableHAMode: true
  email: admin@example.com

providers:
  gcp:
    type: gcp
    region: us-central1
    primary: true
    useApplicationDefault: true
    config:
      project_id: my-gcp-project        # required
      zone: us-central1-a
      vpc_name: adhar-vpc
      subnet_name: adhar-subnet
      subnet_cidr: 10.6.0.0/16          # firewall rules are built from this
      machine_type: n2-standard-8       # 8 vCPU / 32 GiB
      disk_type: pd-balanced
      disk_size_gb: 100
      image_family: ubuntu-2404-lts-amd64
      image_project: ubuntu-os-cloud
      # Who may reach SSH (22) and the API server (6443). Omitting it opens both
      # to the internet and logs a warning at provisioning time.
      # admin_source_ranges: ["203.0.113.4/32"]

Three things about this block used to behave differently from how it reads, and
were fixed on 2026-09-19:

- **`subnet_cidr` is now actually used by the firewall rules.** The internal rule
  hardcoded `10.0.0.0/24` as its source range, so any cluster on a different
  subnet had node-to-node traffic dropped and never finished joining. The
  recommended `10.6.0.0/16` above was one such value.
- **`disk_type` and `disk_size_gb` are now honoured.** Both were parsed, defaulted
  and then ignored: every node got 50 GB of `pd-standard` whatever you asked for.
- **`clusterConfig` keys are matched ignoring case and separators.** `node_count`
  and `nodeCount` are the same key. Only camelCase matched before, so the
  snake_case spellings used in this document and in the shipped test fixtures
  were silently dropped and the worker count fell back to its default.

Firewall rules are also scoped to a per-cluster network tag now, and the rule set
includes HTTP/HTTPS, the NodePort range, and Google's load balancer health-check
ranges — without the last of those the Gateway's LoadBalancer has backends that
never turn healthy, so the platform is unreachable even with every pod running.

environmentTemplates:
  nonprod-defaults:
    clusterConfig:
      - key: autoScale
        value: "true"
    coreServices:
      cilium:
        chart:
          repoURL: https://helm.cilium.io/
          name: cilium
          version: 1.15.7

environments:
  dev:
    type: non-production
    provider: gcp
    template: nonprod-defaults
    clusterConfig:
      - key: name
        value: adhar-mgmt
      - key: machineType
        value: n2-standard-8
      - key: nodeCount
        value: "3"
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10
```

`project_id` has no sensible default — set it. A missing or placeholder project
id is the most common first-run failure, and the shipped sample configs
historically carried `YOUR_PROJECT_ID` placeholders that create an orphan
instance before failing.


### Managed mode (GKE)

The default is kubeadm on compute. One switch on the provider hands the control
plane to GKE instead; every other operation (`adhar up`, node groups,
`adhar cluster scale`, `adhar upgrade`, `adhar down`) works the same way:

```yaml
providers:
  gcp:
    type: gcp
    useManagedK8s: true      # or clusterMode: gke
```

What it creates: the VPC network and subnet (same helpers as compute mode),
then a zonal GKE cluster in the provider's `zone` with one node pool per
`nodeGroups` entry (VPC-native; REGULAR release channel unless a version is
pinned). The kubeconfig authenticates through `gke-gcloud-auth-plugin`, so it
**must be on PATH** (`gcloud components install gke-gcloud-auth-plugin`) on the
machine running `adhar up` and wherever the kubeconfig is used. Teardown
deletes the cluster, the `gke-<cluster>-*` firewall rules GKE adds, then the
subnet and network.

What compute mode installs on the control plane after the first joins
(`gcp/cloud_integration.go`): the `cloud-provider-gcp` manifest, the
Persistent Disk CSI driver (remote kustomization; `gce-pd-csi-driver/cloud-sa`
from the service-account key, or application-default credentials), the default
`adhar-block` pd-balanced StorageClass and the CSI startup-taint toleration.

## 4. Run it

Section 1 must be complete first. `examples/gcp-config.yaml` is a working starting
point for a kubeadm cluster with autoscaling.

```bash
# The key file, not `gcloud auth application-default login`: the edge DNS step
# needs a key it can mount. Activating it also makes the CLI act as the same
# identity the provisioner will use, so a permissions problem surfaces here
# rather than halfway through a create.
gcloud auth activate-service-account --key-file ~/.config/gcloud/$PROJECT-adhar.json
gcloud config set project $PROJECT

# Validates the config and prints the resolved cluster shape. Creates nothing.
./adhar up -f config.yaml --env production --dry-run

./adhar up -f config.yaml --env production
```

`--env` is worth passing every time. Without it, `adhar up` provisions **every**
environment in the file, and a leftover sample block sized for another cloud will
create an orphan instance and then fail.

Then:

```bash
export KUBECONFIG=~/.adhar/clusters/production/kubeconfig
./adhar get status                     # platform conditions + per-package health
./adhar get apps                       # application sync/health
./adhar get secrets                    # service credentials
```

Teardown:

```bash
./adhar down -f config.yaml --env production
```

Read the teardown output rather than assuming it was complete — see
[section 5](#5-what-to-watch-on-a-first-run) for the two classes of resource it
reports but does not delete.

## 5. What to watch on a first run

- **Persistent-disk attachments per instance.** The GCP analogue of the limit
  that bounds the DigitalOcean cluster. The cap is generous on modern machine
  types, so the wall should arrive late — but `exceed max volume count` on
  Pending StatefulSet pods is the signal, and the autoscaler acts on it.
- **Quotas: CPUs per region, in-use IP addresses, SSD total GB.** These are the
  usual cause of a create that fails partway.
- **Cloud NAT and forwarding rules bill continuously.** Confirm the teardown
  removed them rather than assuming. Two classes of resource are created by
  controllers *inside* the cluster and are therefore not in Adhar's own state
  file: the load balancer the cloud controller manager provisions for the Gateway
  Service, and one persistent disk per PersistentVolume from the CSI driver.
  `adhar down` now lists whatever of these is left, with the commands to inspect
  them, instead of leaving them silently billing — but it does not delete them,
  because a disk may still hold data you want:

  ```bash
  gcloud compute forwarding-rules list --project <PROJECT>
  gcloud compute disks list --project <PROJECT> --filter='-users:*'
  ```

- **Teardown no longer depends on local state.** A missing
  `~/.adhar/state/gcp/clusters.json` used to abort `adhar down` entirely, which
  left live instances with no supported way to remove them. The provider now
  rediscovers the cluster from the project using its network tag and naming
  convention.
- **No Cloud NAT is created.** Every node gets an ephemeral external IP instead.
  That works, but it means nodes are internet-facing, so set
  `admin_source_ranges` unless you are deliberately leaving SSH and the API
  server open.

## 6. Related

[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Production guide](PRODUCTION.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
