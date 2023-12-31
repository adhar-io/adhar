# ADHAR.IO - Helm Chart Repository :tada:

## Getting started :sparkles:

### Helm :boat:

To install Adhar platform, make sure to have a kubernetes cluster running with at least:

- Version `1.26`, `1.27` or `1.28`
- A node pool with at least **8 vCPU** and **16GB+ RAM** (more resources might be required based on the activated capabilities)
- Calico CNI installed (or any other CNI that supports K8s network policies)
- A default storage class configured
- When using the `custom` provider, make sure the K8s LoadBalancer Service created by `Adhar` can obtain an external IP (using a cloud load balancer or MetalLB)

> **_NOTE:_** Install Adhar with DNS to unlock it's full potential. Check [adhar.io](https://adhar.io) for more info.

Add the Helm repository:

```bash
helm repo add adhar https://chart.adhar.io
helm repo update
```

and then install the Helm chart:

```bash
helm install adhar adhar/adhar \
--set cluster.name=adhar-dev \
--set cluster.provider=digitalocean # use 'azure', 'aws', 'google', 'digitalocean', 'civo', or 'custom' for any other cloud or onprem K8s
```
