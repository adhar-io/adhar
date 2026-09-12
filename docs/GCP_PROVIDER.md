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
| Provisioning model | kubeadm on Compute Engine instances (GKE is not the default path) |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| DNS | Cloud DNS, or any zone you point at the load balancer |

## 1. Credentials

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
checks, backend services) and, for DNS, DNS Administrator on the zone.

## 2. Configuration

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
      subnet_cidr: 10.6.0.0/16
      machine_type: n2-standard-8       # 8 vCPU / 32 GiB
      disk_type: pd-ssd
      disk_size_gb: 100
      image_family: ubuntu-2404-lts-amd64
      image_project: ubuntu-os-cloud

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

## 3. Run it

```bash
gcloud auth application-default login
./adhar up -f config.yaml --env dev --dry-run
./adhar up -f config.yaml --env dev

export KUBECONFIG=~/.adhar/clusters/dev/kubeconfig
./adhar get status
```

Teardown:

```bash
./adhar down -f config.yaml --env dev
```

## 4. What to watch on a first run

- **Persistent-disk attachments per instance.** The GCP analogue of the limit
  that bounds the DigitalOcean cluster. The cap is generous on modern machine
  types, so the wall should arrive late — but `exceed max volume count` on
  Pending StatefulSet pods is the signal, and the autoscaler acts on it.
- **Quotas: CPUs per region, in-use IP addresses, SSD total GB.** These are the
  usual cause of a create that fails partway.
- **Cloud NAT and forwarding rules bill continuously.** Confirm the teardown
  removed them rather than assuming.

## 5. Related

[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Production guide](PRODUCTION.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
