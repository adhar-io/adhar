# AWS provider

Adhar on AWS: EC2 instances bootstrapped with kubeadm, the same code path that
is live-verified on DigitalOcean.

> **Status: render-verified, not live-verified.** Provider registration, config
> validation and `adhar up --dry-run` pass, and the provisioning code is shared
> with the DigitalOcean path that has run end to end. **No full run on AWS has
> been completed.** Treat your first use as a bring-up exercise, budget time for
> it, and check [PROVIDER_GUIDE.md](PROVIDER_GUIDE.md#2-verification-status-what-is-proven-where)
> for the current matrix. The [DigitalOcean provider guide](DIGITALOCEAN_PROVIDER.md)
> is the reference for everything that is not cloud-specific.

| | |
|---|---|
| Provisioning model | kubeadm on EC2 instances (EKS is not the default path) |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| DNS | Route 53, or any zone you point at the load balancer |

## 1. Credentials

The provider accepts five authentication methods. Pick one.

```yaml
providers:
  aws:
    type: aws
    region: us-east-1
    primary: true
    useEnvironment: true        # standard AWS_* environment variables
    # profile: my-profile       # a named profile from ~/.aws/credentials
    # credentialsFile: /path/to/credentials
    # roleArn: arn:aws:iam::123456789012:role/AdharProvisioner
    # externalId: …             # paired with roleArn
    # useInstanceProfile: true  # EC2 instance profile / IRSA
```

With `useEnvironment: true`:

```bash
export AWS_ACCESS_KEY_ID="…"
export AWS_SECRET_ACCESS_KEY="…"
export AWS_SESSION_TOKEN="…"     # only for temporary credentials
```

The identity needs EC2 (instances, VPC, subnets, security groups, internet and
NAT gateways, route tables, elastic IPs, key pairs), ELB (load balancers, target
groups) and, if you use Route 53 for DNS, hosted-zone record management.

## 2. Configuration

```yaml
globalSettings:
  adharContext: adhar-mgmt
  defaultHost: platform.example.com   # a zone you control
  defaultHttpPort: 80
  defaultHttpsPort: 443
  enableHAMode: true
  email: admin@example.com

providers:
  aws:
    type: aws
    region: us-east-1
    primary: true
    useEnvironment: true
    config:
      cidr: 10.4.0.0/16                 # VPC range
      # subnetCidrs: [10.4.1.0/24, 10.4.2.0/24]

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
    provider: aws
    template: nonprod-defaults
    clusterConfig:
      - key: name
        value: adhar-mgmt
      - key: nodeSize
        value: m6i.2xlarge          # 8 vCPU / 32 GiB
      - key: nodeCount
        value: "3"
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10
```

`clusterConfig.nodeSize` is an EC2 instance type. The full catalogue wants
roughly 8 vCPU and 16–32 GiB per worker; a curated profile is comfortable on
half that.

## 3. Run it

```bash
export AWS_ACCESS_KEY_ID="…" AWS_SECRET_ACCESS_KEY="…"
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

- **EBS volume attachments per instance.** This is the AWS analogue of the limit
  that actually bounds the DigitalOcean cluster. Nitro instances allow far more
  than DigitalOcean's 7, so the wall should arrive much later — but it is the
  first thing to check if StatefulSet pods sit `Pending` with
  `exceed max volume count`. The autoscaler treats that message as a scale-up
  signal on every cloud.
- **Service quotas.** Instances, elastic IPs, NAT gateways and load balancers
  are all quota-limited per region; a `422`-style rejection during create is
  usually a quota, not a bug.
- **Cost.** NAT gateways bill per hour *and* per GB. Verify the teardown removed
  them: `./adhar cluster list -f config.yaml` and then check the console.

## 5. Related

[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Production guide](PRODUCTION.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
