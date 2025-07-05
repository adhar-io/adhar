# Adhar Management Cluster Setup

This directory contains production-grade scripts and configurations for setting up and managing the Adhar platform's management cluster. The implementation follows industry best practices for security, reliability, and day-2 operations.

## Overview

The management cluster serves as the central control plane for the Adhar platform, hosting:
- Kubernetes control plane with HA configuration
- Cilium CNI for advanced networking and security
- Crossplane for multi-cloud infrastructure orchestration
- ArgoCD for GitOps workflows
- Monitoring and observability stack
- Backup and disaster recovery automation

## Quick Start

### 1. Prerequisites

Ensure your system meets the minimum requirements:
- RHEL/CentOS/Rocky Linux 8+
- 8GB RAM minimum (16GB recommended)
- 2 CPU cores minimum (4 cores recommended)
- 50GB disk space minimum
- Root or sudo access

### 2. Test Setup

Before deploying, run the validation test:

```bash
./test-setup.sh
```

This validates:
- System prerequisites
- Configuration files
- Script availability
- Integration components

### 3. Configuration

Edit `cluster-config.yaml` to match your environment:

```yaml
cluster:
  name: "adhar-management"
  kubernetesVersion: "v1.31.7"
  controlPlaneEndpoint: "your-lb-endpoint:7443"
  
  masters:
    - name: "master01"
      ip: "10.30.155.99"
      hostname: "adhar-master01"
    # Add additional masters for HA
```

### 4. Bootstrap Cluster

Deploy the management cluster:

```bash
sudo ./bootstrap.sh
```

The bootstrap process includes:
- System preparation and security hardening
- Container runtime (containerd) installation
- Kubernetes cluster initialization
- Cilium CNI deployment
- HAProxy load balancer setup (for HA)
- Monitoring stack installation
- Security policies and network policies
- Backup automation setup

### 5. Verify Deployment

Check cluster health:

```bash
./day2-ops.sh health --verbose
```

## Files Overview

### Core Scripts

- **`bootstrap.sh`** - Main cluster provisioning script
  - Idempotent and production-ready
  - Comprehensive error handling and logging
  - Automated security hardening
  - Support for HA configurations

- **`day2-ops.sh`** - Day-2 operations automation
  - Health monitoring and alerting
  - Automated backup and recovery
  - Performance optimization
  - Security auditing
  - System maintenance

- **`test-setup.sh`** - Validation and testing
  - Pre-deployment validation
  - Integration testing
  - System requirement checks

### Configuration Files

- **`cluster-config.yaml`** - Cluster configuration
  - YAML-driven cluster definition
  - Support for multi-master HA
  - Cilium feature configuration
  - Monitoring and backup settings

### Go Integration

- **`../platform/build/cluster.go`** - Go integration module
  - Programmatic cluster management
  - CLI integration
  - Status monitoring and validation

## CLI Integration

The management cluster integrates with the Adhar CLI through dedicated commands:

```bash
# Bootstrap new management cluster
adhar cluster bootstrap --config cluster-config.yaml

# Check cluster status
adhar cluster status --verbose

# Create backup
adhar cluster backup --output /var/lib/adhar/backups

# Clean up resources
adhar cluster cleanup --dry-run
```

## Day-2 Operations

### Health Monitoring

Comprehensive health checks covering:
- Kubernetes API server
- Node status and resource usage
- Cilium networking health
- System pods (etcd, CoreDNS)
- Certificate expiration
- Disk and memory usage
- Security policy compliance

```bash
# Basic health check
./day2-ops.sh health

# Detailed health report
./day2-ops.sh health --verbose

# JSON output for automation
./day2-ops.sh health --json
```

### Backup and Recovery

Automated backup of critical cluster components:
- etcd snapshots with verification
- Kubernetes certificates and secrets
- Cluster state and custom resources
- Crossplane configurations
- Persistent volume snapshots

```bash
# Full cluster backup
./day2-ops.sh backup

# etcd-only backup
./day2-ops.sh backup --etcd-only

# Custom output directory
./day2-ops.sh backup --output /custom/backup/path
```

### Maintenance Operations

Regular maintenance tasks:
- Resource cleanup (failed pods, completed jobs)
- Container image pruning
- Log rotation and cleanup
- Performance optimization
- Security policy updates

```bash
# System cleanup
./day2-ops.sh cleanup

# Performance optimization
./day2-ops.sh optimize

# Security audit
./day2-ops.sh security

# Full maintenance cycle
./day2-ops.sh full
```

## Security Features

### Network Security
- Default-deny network policies
- Cilium eBPF-based networking
- Encrypted pod-to-pod communication
- Ingress/egress traffic control

### Cluster Security
- Pod Security Policies
- RBAC with least-privilege access
- Certificate-based authentication
- Audit logging enabled
- Regular security scanning

### System Security
- SELinux configuration (permissive for K8s)
- Firewall rules for required ports
- Automatic security updates
- File integrity monitoring

## High Availability

### Control Plane HA
- Multiple master nodes
- HAProxy load balancer
- etcd clustering
- Automatic failover

### Application HA
- Pod anti-affinity rules
- Multiple replicas for critical services
- Health checks and auto-recovery
- Rolling updates with zero downtime

## Monitoring and Observability

### Metrics Collection
- Prometheus for metrics
- Node Exporter for system metrics
- Cilium metrics for networking
- etcd metrics for cluster health

### Visualization
- Grafana dashboards
- Hubble UI for network observability
- kubectl top commands
- Custom health reporting

### Alerting
- Critical system alerts
- Resource usage thresholds
- Certificate expiration warnings
- Backup failure notifications

## Troubleshooting

### Common Issues

1. **Bootstrap fails with permission errors**
   ```bash
   # Ensure running as root or with sudo
   sudo ./bootstrap.sh
   ```

2. **Cilium installation fails**
   ```bash
   # Check kernel modules
   modprobe overlay br_netfilter
   # Verify firewall rules
   firewall-cmd --list-all
   ```

3. **etcd backup fails**
   ```bash
   # Verify certificates exist
   ls -la /etc/kubernetes/pki/etcd/
   # Check etcd pod status
   kubectl get pods -n kube-system -l component=etcd
   ```

4. **Node not joining cluster**
   ```bash
   # Check join token validity
   kubeadm token list
   # Verify network connectivity
   ping <master-ip>
   # Check firewall rules
   ```

### Log Locations

- Bootstrap logs: `/var/log/adhar-bootstrap.log`
- Day-2 operations: `/var/log/adhar/operations.log`
- Kubernetes logs: `/var/log/pods/`
- Cilium logs: `kubectl logs -n kube-system -l k8s-app=cilium`

### Recovery Procedures

1. **Restore from backup**
   ```bash
   # Extract backup
   tar -xzf backup-file.tar.gz
   # Restore etcd
   etcdctl snapshot restore ...
   # Restart cluster services
   ```

2. **Reset cluster (development only)**
   ```bash
   kubeadm reset
   ./bootstrap.sh
   ```

## Integration with Adhar Platform

The management cluster serves as the foundation for:

1. **Environment Provisioning** - Crossplane provisions workload clusters
2. **GitOps Workflows** - ArgoCD manages application deployments
3. **Multi-Cloud Operations** - Centralized control across providers
4. **Compliance and Governance** - Policy enforcement and auditing

## Contributing

When modifying the management cluster setup:

1. Test changes with `./test-setup.sh`
2. Validate scripts with `shellcheck`
3. Test on clean systems
4. Update documentation
5. Follow semantic versioning for configs

## Support

For issues and questions:
- Check troubleshooting section above
- Review logs in `/var/log/adhar/`
- Validate configuration with test script
- Consult the main Adhar documentation

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                 Management Cluster                          │
│                                                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐        │
│  │   Master    │  │   Master    │  │   Master    │        │
│  │   Node 1    │  │   Node 2    │  │   Node 3    │        │
│  │             │  │             │  │             │        │
│  │ ┌─────────┐ │  │ ┌─────────┐ │  │ ┌─────────┐ │        │
│  │ │ etcd    │ │  │ │ etcd    │ │  │ │ etcd    │ │        │
│  │ │ api-srv │ │  │ │ api-srv │ │  │ │ api-srv │ │        │
│  │ │ ctrlr   │ │  │ │ ctrlr   │ │  │ │ ctrlr   │ │        │
│  │ └─────────┘ │  │ └─────────┘ │  │ └─────────┘ │        │
│  └─────────────┘  └─────────────┘  └─────────────┘        │
│                                                             │
│  ┌─────────────┐  ┌─────────────┐                         │
│  │   Worker    │  │   Worker    │                         │
│  │   Node 1    │  │   Node 2    │                         │
│  │             │  │             │                         │
│  │ Crossplane  │  │ ArgoCD      │                         │
│  │ Controllers │  │ Applications│                         │
│  │ Cilium CNI  │  │ Monitoring  │                         │
│  └─────────────┘  └─────────────┘                         │
│                                                             │
│                    HAProxy Load Balancer                   │
│                    (API Server Frontend)                   │
└─────────────────────────────────────────────────────────────┘
```

This management cluster setup provides the foundation for scalable, secure, and maintainable multi-cloud operations with the Adhar platform.
