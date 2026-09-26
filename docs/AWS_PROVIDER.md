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
>
> Preparing for that first run (2026-09-26) found two defects by reading the
> provider, before spending anything:
>
> - **No default route to the internet gateway.** `CreateVPC` created and attached
>   an internet gateway and stopped there, so a fresh VPC's main route table still
>   had only its local route. Nodes came up holding public IPs with no egress at
>   all, which would have failed kubeadm node prep at the first package download
>   while every instance looked healthy. Now routes `0.0.0.0/0` through the
>   gateway, idempotently. Guarded by `TestCreateVPCRoutesEgressToTheInternetGateway`.
> - **The node image was pinned to Ubuntu 22.04 and unconfigurable** while GCP
>   booted 24.04. See [Node image](#node-image).
>
> Both are fixed, and neither has been exercised against a live account yet.

| | |
|---|---|
| Provisioning model | kubeadm on EC2 instances (default); EKS via `useManagedK8s: true` |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| DNS | Route 53, or any zone you point at the load balancer |

## 0. Before you start: four prerequisites, in this order

`adhar up` creates nothing until all four are in place. They are listed in the
order in which a failure INVALIDATES the ones after it — checking DNS first and
the organization last is how a bring-up gets abandoned twice before anyone looks
at the thing that was actually blocking it.

| # | Prerequisite | Who can do it | Fails as |
|---|---|---|---|
| 0.1 | No Service Control Policy denying EC2 | The **management** account only | `UnauthorizedOperation … explicit deny in a service control policy` |
| 0.2 | Provisioning permissions on the key | The account's IAM admin | `… no identity-based policy allows …` |
| 0.3 | EC2 vCPU quota for the whole cluster | A support-case quota increase | A create that stops partway with `VcpuLimitExceeded` |
| 0.4 | A delegated Route 53 zone for `defaultHost` | You plus your registrar | Self-signed certificates; the platform still runs |

Only 0.4 is optional, and only in the sense that the platform comes up without it.

**Run [the preflight](#05-verify-all-four-at-once) before `adhar up`.** It checks
all four and, importantly, tells the two kinds of "denied" apart.

### 0.1 Clear the Service Control Policies first

An identity policy is a ceiling, not a guarantee. If the account belongs to an AWS
Organization, a **Service Control Policy** on the organization or its OU can deny
an action that `AdministratorAccess` allows, and **nothing inside the member
account can grant it back**. Adding IAM policies changes nothing.

This one is first because of how it reads. The user shows `AdministratorAccess`
attached, so the natural conclusion is that permissions are fine — while every
call fails. The cause is named only at the very end of a long error:

```
An error occurred (UnauthorizedOperation) when calling the DescribeVpcs operation:
You are not authorized to perform this operation. User: arn:aws:iam::…:user/adhar
is not authorized to perform: ec2:DescribeVpcs with an explicit deny in a service
control policy: arn:aws:organizations::…:policy/o-…/service_control_policy/p-…
```

Read which phrase you got, because they need different people:

| Phrase in the error | Means | Fixed by |
|---|---|---|
| `explicit deny in a service control policy` | An org-level block above IAM | The management account |
| `no identity-based policy allows` | A missing grant | This account's IAM admin |

From the **management** account, inspect and retarget the policy:

```bash
aws organizations describe-policy          --policy-id p-EXAMPLE
aws organizations list-targets-for-policy  --policy-id p-EXAMPLE
```

Then either drop `ec2:*`, `elasticloadbalancing:*` and
`servicequotas:GetServiceQuota` from its `Deny`, add a condition excluding the
member account, or detach it from that OU. Which is appropriate depends on why the
guardrail exists.

**Re-test with a repeated sample, never a single call.** While an SCP is being
edited, calls succeed briefly and then go back to denied. A real bring-up attempt
measured a clean batch, then 12 consecutive denials, then another clean batch, then
30 consecutive denials — and a single passing check at the wrong moment is how you
start a create that dies half-built:

```bash
for i in $(seq 1 30); do
  aws ec2 describe-availability-zones >/dev/null 2>&1 && printf . || printf D
  sleep 5
done; echo
# 30 dots = stable. Any D = not ready, whatever a single call just told you.
```

### 0.2 Grant the provisioning permissions

See [section 1](#1-credentials) for the auth methods and the exact policy. In
short: EC2 (VPC, subnets, gateways, route tables, security groups, key pairs,
instances, volumes), ELB describe + delete, `servicequotas:GetServiceQuota`, and
`sts:GetCallerIdentity`. `AmazonEC2FullAccess` plus the ELB and quota entries also
works.

### 0.3 Raise the EC2 vCPU quota to cover the WHOLE cluster

The limit is **L-1216C47A**, "Running On-Demand Standard (A, C, D, H, I, M, R, T,
Z) instances", and it counts vCPUs, not instances. Size it for `maxWorkers`, not
for the number of nodes you start with, or the autoscaler hits a wall it cannot
report usefully:

```bash
aws service-quotas get-service-quota --service-code ec2 --quota-code L-1216C47A \
  --query 'Quota.Value' --output text
```

For the shipped `config.aws.yaml` — one control plane plus up to 6 workers of
`m6i.2xlarge` (8 vCPU each) — that is **56 vCPU**. New accounts often start at 5.
Request the increase before the first run; it is not instant.

### 0.4 DNS: create and delegate the platform host

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

### 0.5 Verify all four at once

Paste this before `adhar up`. It exercises the calls the provider actually makes,
separates an org-level deny from a missing IAM grant, and checks the zone is really
delegated on the public internet rather than merely present in Route 53.

```bash
#!/usr/bin/env bash
# Adhar AWS preflight. Read-only plus dry-runs: creates nothing.
REGION=${REGION:-ap-south-1}
HOST=${HOST:-dev.adhar.io}
NEED_VCPU=${NEED_VCPU:-56}

scp_deny() { grep -q "explicit deny in a service control policy" <<<"$1"; }
iam_deny() { grep -qE "no identity-based policy allows|AccessDenied|UnauthorizedOperation" <<<"$1"; }

check() { # check <label> <command...>
  local label=$1; shift
  local out; out=$("$@" 2>&1)
  if   scp_deny "$out"; then printf '  %-34s SCP-DENY  (management account must fix)\n' "$label"; return 1
  elif grep -q DryRunOperation <<<"$out"; then printf '  %-34s ok\n' "$label"
  elif iam_deny "$out"; then printf '  %-34s IAM-DENY  (add the permission)\n' "$label"; return 1
  else printf '  %-34s ok\n' "$label"; fi
}

echo "identity: $(aws sts get-caller-identity --query Arn --output text 2>&1)"
echo
echo "reads:"
check "DescribeAvailabilityZones" aws ec2 describe-availability-zones --region "$REGION"
check "DescribeImages"            aws ec2 describe-images --owners 099720109477 --max-items 1 --region "$REGION"
check "DescribeInstanceTypes"     aws ec2 describe-instance-types --instance-types m6i.2xlarge --region "$REGION"
check "DescribeVpcs"              aws ec2 describe-vpcs --max-items 1 --region "$REGION"
check "DescribeKeyPairs"          aws ec2 describe-key-pairs --region "$REGION"
check "DescribeRouteTables"       aws ec2 describe-route-tables --max-items 1 --region "$REGION"
check "elbv2 DescribeLBs"         aws elbv2 describe-load-balancers --max-items 1 --region "$REGION"
echo "writes (dry-run):"
check "CreateVpc"                 aws ec2 create-vpc --cidr-block 10.99.0.0/16 --dry-run --region "$REGION"
check "CreateSecurityGroup"       aws ec2 create-security-group --group-name adhar-preflight --description p --vpc-id vpc-00000000 --dry-run --region "$REGION"
check "RunInstances"              aws ec2 run-instances --image-id ami-0abcdef1234567890 --instance-type m6i.2xlarge --dry-run --region "$REGION"

echo "quota:"
q=$(aws service-quotas get-service-quota --service-code ec2 --quota-code L-1216C47A \
      --query 'Quota.Value' --output text --region "$REGION" 2>&1)
if [[ $q =~ ^[0-9.]+$ ]]; then
  awk -v q="$q" -v n="$NEED_VCPU" 'BEGIN{
    printf "  L-1216C47A vCPU limit %s (need %s) %s\n", q, n, (q+0>=n+0 ? "ok" : "TOO LOW")}'
else
  # Flattened: an AWS error is several lines, and printing it raw derails the
  # report right where the reader is scanning a column of ok/DENY.
  reason=$(tr '\n' ' ' <<<"$q" | sed 's/  */ /g')
  if scp_deny "$q"; then reason="SCP-DENY (management account must fix)"; fi
  echo "  L-1216C47A vCPU limit            unreadable: ${reason:0:70}"
fi

echo "dns:"
zone=$(aws route53 list-hosted-zones-by-name --dns-name "$HOST" \
         --query "HostedZones[?Name=='$HOST.'].Id" --output text 2>&1)
if [[ $zone == /hostedzone/* ]]; then
  echo "  zone $zone"
  want=$(aws route53 get-hosted-zone --id "${zone##*/}" \
           --query 'DelegationSet.NameServers' --output text | tr '\t' '\n' | sort)
  live=$(dig +short NS "$HOST" @8.8.8.8 | sed 's/\.$//' | sort)
  if [[ -n $live && $want == "$live" ]]; then echo "  delegation                       ok"
  else echo "  delegation                       NOT LIVE (add 4 NS records at the registrar)"; fi
else
  echo "  no public hosted zone for $HOST — certificates will be self-signed"
fi
```

Every line `ok`, the quota at or above what you need, and delegation live: you are
ready. Any `SCP-DENY` means go back to [0.1](#01-clear-the-service-control-policies-first) —
and re-run the 30-sample loop there, because one clean preflight during a policy
edit is not stability.

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

### Exactly which permissions, and why

The list below is derived from the AWS API calls the provider actually makes, not
from a generic template. `AmazonEC2FullAccess` also works and is simpler; this
exists for accounts where that is not acceptable.

A key with less than this fails in a way that is easy to misread: the first denied
call is usually `ec2:DescribeImages`, which surfaces as "no AMI found" rather than
as a permissions problem.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "NetworkAndCompute",
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeRegions", "ec2:DescribeAvailabilityZones",
        "ec2:DescribeImages", "ec2:DescribeInstanceTypes",
        "ec2:CreateVpc", "ec2:DeleteVpc", "ec2:DescribeVpcs", "ec2:ModifyVpcAttribute",
        "ec2:CreateSubnet", "ec2:DeleteSubnet", "ec2:DescribeSubnets", "ec2:ModifySubnetAttribute",
        "ec2:CreateInternetGateway", "ec2:DeleteInternetGateway", "ec2:DescribeInternetGateways",
        "ec2:AttachInternetGateway", "ec2:DetachInternetGateway",
        "ec2:CreateRoute", "ec2:DescribeRouteTables", "ec2:DeleteRouteTable",
        "ec2:CreateSecurityGroup", "ec2:DeleteSecurityGroup", "ec2:DescribeSecurityGroups",
        "ec2:AuthorizeSecurityGroupIngress", "ec2:RevokeSecurityGroupIngress",
        "ec2:RevokeSecurityGroupEgress",
        "ec2:ImportKeyPair", "ec2:DeleteKeyPair", "ec2:DescribeKeyPairs",
        "ec2:RunInstances", "ec2:TerminateInstances", "ec2:DescribeInstances",
        "ec2:CreateVolume", "ec2:DeleteVolume", "ec2:DescribeVolumes",
        "ec2:CreateTags",
        "ec2:DescribeAddresses", "ec2:DisassociateAddress", "ec2:ReleaseAddress",
        "ec2:DescribeNetworkInterfaces", "ec2:DeleteNetworkInterface",
        "ec2:DescribeNatGateways", "ec2:DeleteNatGateway"
      ],
      "Resource": "*"
    },
    {
      "Sid": "LoadBalancerCleanup",
      "Effect": "Allow",
      "Action": [
        "elasticloadbalancing:DescribeLoadBalancers", "elasticloadbalancing:DescribeTags",
        "elasticloadbalancing:DeleteLoadBalancer",
        "elasticloadbalancing:DescribeTargetGroups", "elasticloadbalancing:DeleteTargetGroup"
      ],
      "Resource": "*"
    },
    {
      "Sid": "QuotaPreflight",
      "Effect": "Allow",
      "Action": ["servicequotas:GetServiceQuota"],
      "Resource": "*"
    },
    {
      "Sid": "WhoAmI",
      "Effect": "Allow",
      "Action": ["sts:GetCallerIdentity"],
      "Resource": "*"
    }
  ]
}
```

Two things the provider does **not** need, contrary to what an earlier version of
this page said:

- **NAT gateways and elastic IPs are never created.** Nodes sit in the public
  subnet with `MapPublicIpOnLaunch`, so there is no NAT path to pay for. The
  `Delete`/`Describe` entries above exist only so teardown can clean up a NAT
  gateway that an in-cluster controller created.
- **Route 53 is not called by the provider at all.** DNS records are managed from
  inside the cluster by external-dns, and the DNS-01 certificate challenge by
  cert-manager. Those need their own credentials with
  `route53:ChangeResourceRecordSets`, `route53:ListHostedZones*` and
  `route53:GetChange` on your zone — separate from the provisioning key above.

### If the account is in an AWS Organization, check the SCPs first

An identity policy is a ceiling, not a guarantee. A **Service Control Policy** on
the organization or OU can deny an action that `AdministratorAccess` allows, and
nothing in the member account can override it.

This is worth checking before anything else because of how it reads. The user
shows `AdministratorAccess` attached, so the natural conclusion is that
permissions are fine — while every call fails. The error does name the cause, at
the very end of a long line:

```
An error occurred (UnauthorizedOperation) when calling the DescribeVpcs operation:
You are not authorized to perform this operation. User: arn:aws:iam::…:user/adhar
is not authorized to perform: ec2:DescribeVpcs with an explicit deny in a service
control policy: arn:aws:organizations::…:policy/o-…/service_control_policy/p-…
```

The two phrases that matter are **"explicit deny in a service control policy"**
(an org-level block, fixable only from the management account) versus **"no
identity-based policy allows"** (a missing grant, fixable in the account). They
need completely different people, so read which one you got before escalating.

Only the management account can amend an SCP. Denials can also appear to come and
go while a policy is being edited, so re-test rather than trusting one result.

Add these only for the modes you use:

| Mode | Extra permissions |
|---|---|
| `useManagedK8s: true` (EKS) | `eks:*` on the cluster and node groups, plus `iam:CreateRole`, `iam:GetRole`, `iam:DeleteRole`, `iam:AttachRolePolicy`, `iam:DetachRolePolicy`, `iam:ListAttachedRolePolicies`, `iam:PassRole` |
| `roleArn` (assume-role) | `sts:AssumeRole` on that role |

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


### Node image

Nodes boot the newest **Ubuntu 24.04 LTS (noble)** AMI published by Canonical for
`x86_64`, discovered per region at provision time. Every part of that is
configurable on the provider:

```yaml
providers:
  aws:
    type: aws
    region: ap-south-1
    # ami: ami-0123456789abcdef0          # pin exactly; skips discovery entirely
    # imageNameFilter: "my/hardened-*"    # a private or hardened base image
    # imageOwner: "111122223333"          # whose images to search
    # imageArchitecture: arm64            # pair with a Graviton machineType
```

`ami` and `imageId`, `imageNameFilter` and `image_name_filter`, and the other
aliases are all accepted, in camelCase or snake_case.

**Pin `ami` for anything reproducible.** Without it, two runs a week apart boot
different images, because the lookup takes whatever Canonical published most
recently.

This was hardcoded to Ubuntu 22.04 (jammy) with no configuration path, while the
GCP provider booted 24.04 — so the same platform ran on two distro releases
depending on the cloud, with different kernels and container runtimes underneath
it, and a hardened image could not be used at all. Fixed 2026-09-26.

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

Do [the preflight](#05-verify-all-four-at-once) first. `--dry-run` validates the
config but does NOT check permissions, quota or DNS, so it passes cleanly on an
account that cannot create anything.

```bash
export AWS_ACCESS_KEY_ID="…" AWS_SECRET_ACCESS_KEY="…"

./adhar up -f config.aws.yaml --env dev --dry-run   # config only, no spend
./adhar up -f config.aws.yaml --env dev             # ~25-40 min on a first run

export KUBECONFIG=~/.adhar/clusters/dev/kubeconfig
./adhar get status
```

Teardown:

```bash
./adhar down -f config.aws.yaml --env dev

# Also delete unattached CSI volumes that carry no cluster tag at all.
# Works whether or not the cluster still exists.
./adhar down -f config.aws.yaml --env dev --purge-orphaned-volumes
```

**Point `down` at the same file you brought it up with.** A teardown can only see
the providers its config file defines: aimed at a file that configures a different
provider it finds nothing, and until this was fixed it reported success anyway
while the whole cluster kept running. It now says "Nothing was deleted" and names
the providers it searched.

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
