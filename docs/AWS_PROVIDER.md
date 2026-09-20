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
| Provisioning model | kubeadm on EC2 instances (default); EKS via `useManagedK8s: true` |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| DNS | Route 53, or any zone you point at the load balancer |

## 0. DNS: delegate the platform host (do this first)

`globalSettings.defaultHost` drives every URL and the wildcard certificate, so it
must be a subdomain you delegate to a Route 53 hosted zone. The platform never
creates the zone.

```bash
# 1. a PUBLIC hosted zone for exactly your defaultHost
aws route53 create-hosted-zone \
  --name platform.example.com \
  --caller-reference "adhar-$(date +%s)" \
  --query 'HostedZone.Id' --output text            # → /hostedzone/Z0123456789ABC

# 2. read the four nameservers Route 53 assigned to THIS zone
aws route53 get-hosted-zone --id Z0123456789ABC \
  --query 'DelegationSet.NameServers' --output text
```

3. At your registrar, in the `example.com` zone, add **four NS records named
   `platform`** with those four values. The apex stays where it is.

Route 53 DNS-01 needs **static** credentials (`accessKeyId` + `secretAccessKey`):
the cert-manager solver reads them from a Secret, so an instance profile or SSO
session is not enough even though provisioning works from one. The IAM policy needs
`route53:ChangeResourceRecordSets`, `route53:ListHostedZonesByName` and
`route53:GetChange` on that zone.

**Verify before `adhar up`** (an empty CAA answer is correct; SERVFAIL is not):

```bash
dig +short NS platform.example.com      # → the nameservers from step 2
dig +short NS example.com               # → your registrar's, unchanged
dig +noall +comment CAA example.com     # → status: NOERROR
```

**Do not delegate the whole registered domain here unless you also host its apex
zone here.** Let's Encrypt reads CAA up the tree, so issuance for
`*.platform.example.com` also queries CAA for `example.com`; if that name is
delegated to nameservers holding no zone, every lookup SERVFAILs and the order fails
with a message naming CAA rather than delegation. Full explanation and the
capability table: [Provider guide §7](PROVIDER_GUIDE.md#7-dns-delegate-the-platform-host-before-adhar-up).

Skipping this is supported: the platform comes up on its self-signed certificate,
`adhar up` says so in its closing summary and `adhar get status` warns, and
cert-manager issues a trusted certificate on its own once the DNS is fixed.

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


### Managed mode (EKS)

The default is kubeadm on compute. One switch on the provider hands the control
plane to EKS instead; every other operation (`adhar up`, node groups,
`adhar cluster scale`, `adhar upgrade`, `adhar down`) works the same way:

```yaml
providers:
  aws:
    type: aws
    useManagedK8s: true      # or clusterMode: eks
```

What it creates: the same VPC/subnets/security group as compute mode, two IAM
roles (`adhar-<cluster>-eks-cluster`, `adhar-<cluster>-eks-node`), the EKS
control plane, one managed node group per `nodeGroups` entry and the
`aws-ebs-csi-driver` addon. The kubeconfig authenticates through
`aws eks get-token`, so the **`aws` CLI must be on PATH** on the machine
running `adhar up` and wherever the kubeconfig is used. Teardown deletes the
node groups, the cluster and the roles, then the shared tag-based network
cleanup runs.

What compute mode installs on the control plane after the first joins
(`aws/cloud_integration.go`): the `aws-cloud-controller-manager` chart, the
`aws-ebs-csi-driver` chart (with `kube-system/aws-secret` when static keys are
configured; nothing when `useInstanceProfile: true`), the default `adhar-block`
gp3 StorageClass and the CSI startup-taint toleration.

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

# Also delete unattached CSI volumes that carry no cluster tag at all
./adhar down -f config.yaml --env dev --purge-orphaned-volumes
```

### What teardown removes, and what it deliberately leaves

A cluster's AWS footprint is created by two parties. This provider creates the VPC,
subnets, security groups, instances and gateways, and tags them `Cluster=<name>`.
The controllers running **inside** the cluster create more, tagged
`kubernetes.io/cluster/<name>` instead:

| Created by | Resource | Removed by teardown |
| --- | --- | --- |
| provider | instances, ENIs, EIPs, NAT gateways, route tables, security groups, subnets, IGW, VPC, key pair | yes, in dependency order |
| cloud-controller-manager | one load balancer per `LoadBalancer` Service — **classic ELB or NLB**, both flavours are swept | yes |
| load-balancer controller | target groups, and the `k8s-elb-*` security group | yes |
| EBS CSI driver | one EBS volume per PersistentVolume, tagged for this cluster | yes |
| EBS CSI driver | unattached volumes with **no** cluster tag (a previous cluster's) | only with `--purge-orphaned-volumes` |
| anything | a resource tagged for **two** clusters (`shared`) | never — it belongs to the other one too |

The load-balancer sweep runs **before** the security-group, subnet and VPC steps,
because an ELB and its security group each hold a reference to the VPC: leaving one
behind makes the VPC deletion fail with `DependencyViolation`, and then the network,
its subnets and its internet gateway all survive into the next run.

`--purge-orphaned-volumes` is opt-in for one reason: an unattached CSI volume looks
identical whether its cluster was torn down an hour ago or is being rebuilt right
now. Without the flag the count is reported and the volumes are left alone.

### Quota preflight

`adhar up` reads the account's EC2 quota (`L-1216C47A`, *Running On-Demand Standard
instances*, measured in vCPU), adds up what is already running in the region, and
refuses before creating anything if the cluster will not fit — naming the limit, the
usage and the shortfall. A new AWS account defaults to **5 vCPU**, which is one
node, and the raw failure (`VcpuLimitExceeded`) arrives only after a VPC, subnets,
security groups and some instances exist. A credential without `servicequotas`
access logs a warning and proceeds.

## 4. What to watch on a first run

- **EBS volume attachments per instance.** This is the AWS analogue of the limit
  that actually bounds the DigitalOcean cluster. Nitro instances allow far more
  than DigitalOcean's 7, so the wall should arrive much later — but it is the
  first thing to check if StatefulSet pods sit `Pending` with
  `exceed max volume count`. The autoscaler treats that message as a scale-up
  signal on every cloud.
- **Service quotas.** vCPU is checked before the create starts (see the quota
  preflight above), so a `VcpuLimitExceeded` halfway through should no longer
  happen. Elastic IPs, NAT gateways and load balancers are *not* pre-checked and
  are all quota-limited per region; a `422`-style rejection during create is
  usually one of those, not a bug.
- **Cost.** NAT gateways bill per hour *and* per GB. Verify the teardown removed
  them: `./adhar cluster list -f config.yaml` and then check the console.

## 5. Related

[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Production guide](PRODUCTION.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
