# Advanced Topics

**Version**: v0.3.8  
**Last Updated**: November 2025

---

## 📖 Table of Contents

1. [High Availability Mode](#high-availability-mode)
2. [Production Deployment](#production-deployment)
3. [Migration Strategies](#migration-strategies)
4. [Disaster Recovery](#disaster-recovery)
5. [Multi-Cluster Management](#multi-cluster-management)
6. [Security Hardening](#security-hardening)
7. [Performance Optimization](#performance-optimization)
8. [Troubleshooting](#troubleshooting)

---

## High Availability Mode

### Overview

HA Mode provides production-grade reliability with automatic failover, redundancy, and self-healing capabilities.

### Enabling HA Mode

**Configuration**:
```yaml
globalSettings:
  enableHAMode: true
```

**Via CLI**:
```bash
# Create cluster with HA
adhar up -f config.yaml --ha-mode

# Enable HA for environment
adhar env create prod --ha-mode
```

### HA Components

#### Control Plane (3+ Masters)
- **Etcd**: Distributed key-value store with 3-5 nodes
- **API Server**: Multiple replicas behind load balancer
- **Controller Manager**: Leader election for active-standby
- **Scheduler**: Leader election for active-standby

#### Core Services
- **ArgoCD**: 2+ replicas for server, repo-server, and controller
- **Gitea**: 2+ pods with shared database backend
- **Nginx Ingress**: 3+ replicas across nodes
- **Cilium**: DaemonSet with automatic failover

#### Data Persistence
- **PostgreSQL**: HA configuration with replication
- **Redis**: Sentinel mode with automatic failover
- **MinIO**: Distributed mode with erasure coding

### Resource Requirements

| Component | HA Disabled | HA Enabled |
|-----------|-------------|------------|
| Control Plane Nodes | 1 | 3+ |
| Worker Nodes | 1+ | 3+ |
| ArgoCD Replicas | 1 | 2-3 |
| Ingress Replicas | 1 | 3 |
| Database Replicas | 1 | 3 |

### Automatic Scaling

```yaml
environmentTemplates:
  prod-defaults:
    clusterConfig:
      - key: "autoScale"
        value: "true"
      - key: "minNodes"
        value: "3"
      - key: "maxNodes"
        value: "10"
```

---

## Production Deployment

### Pre-Deployment Checklist

#### Infrastructure
- [ ] Multi-AZ/region deployment
- [ ] Load balancer configuration
- [ ] DNS and SSL certificates
- [ ] Backup storage configured
- [ ] Monitoring endpoints accessible

#### Security
- [ ] Network policies defined
- [ ] RBAC roles configured
- [ ] Secrets encrypted at rest
- [ ] Audit logging enabled
- [ ] Security scanning automated

#### Compliance
- [ ] Policy enforcement enabled
- [ ] Compliance frameworks applied
- [ ] Audit trails configured
- [ ] Data retention policies set

### Production Configuration

```yaml
# Production config example
globalSettings:
  enableHAMode: true
  email: "platform-team@company.com"

providers:
  aws:
    type: aws
    region: us-east-1
    primary: true
    config:
      instance_types:
        control_plane: "t3.large"
        worker: "t3.xlarge"

environmentTemplates:
  prod-defaults:
    coreServices:
      cilium:
        values:
          - key: "operator.replicas"
            value: "3"
      nginx:
        values:
          - key: "controller.replicaCount"
            value: "3"
      argocd:
        values:
          - key: "server.replicas"
            value: "3"
          - key: "repoServer.replicas"
            value: "3"
```

### Deployment Steps

1. **Validate Configuration**
   ```bash
   adhar config validate -f prod-config.yaml
   ```

2. **Create Management Cluster**
   ```bash
   adhar up -f prod-config.yaml --provider aws
   ```

3. **Verify Services**
   ```bash
   adhar get status
   adhar health check
   ```

4. **Configure Monitoring**
   ```bash
   # Access Grafana
   kubectl port-forward -n monitoring svc/grafana 3000:80
   
   # Configure alerts
   kubectl apply -f alerting-rules.yaml
   ```

5. **Setup Backup**
   ```bash
   adhar backup create --schedule "0 2 * * *"
   ```

---

## Migration Strategies

### Version Migration

#### Approach 1: In-Place Upgrade
Suitable for non-critical environments with acceptable downtime.

```bash
# Backup current state
adhar backup create --name pre-upgrade-backup

# Upgrade platform
adhar upgrade --version v0.4.0

# Verify upgrade
adhar get status
```

#### Approach 2: Blue-Green Deployment
Zero-downtime migration with rollback capability.

```bash
# Create new cluster (green)
adhar cluster create prod-green \
  --provider aws \
  --config prod-config-v2.yaml

# Migrate applications gradually
adhar apps migrate --from prod-blue --to prod-green

# Switch traffic
adhar network switch --to prod-green

# Decommission old cluster after verification
adhar cluster delete prod-blue
```

### Provider Migration

Moving between cloud providers (e.g., AWS → GCP).

**Steps**:

1. **Export Configuration**
   ```bash
   adhar config export --cluster prod-aws > aws-config.yaml
   ```

2. **Adapt Configuration**
   ```bash
   # Convert AWS config to GCP format
   adhar config convert \
     --from aws \
     --to gcp \
     --input aws-config.yaml \
     --output gcp-config.yaml
   ```

3. **Create Target Cluster**
   ```bash
   adhar up -f gcp-config.yaml --provider gcp
   ```

4. **Migrate Data**
   ```bash
   # Backup from source
   adhar backup create --cluster prod-aws --all

   # Restore to target
   adhar restore apply \
     --backup prod-aws-backup \
     --cluster prod-gcp
   ```

5. **Migrate Applications**
   ```bash
   adhar apps migrate \
     --from prod-aws \
     --to prod-gcp \
     --verify
   ```

### Data Migration

#### Database Migration
```bash
# Export databases
adhar db export --all --output db-backup.sql

# Import to new cluster
adhar db import --input db-backup.sql --cluster prod-new
```

#### Volume Migration
```bash
# Snapshot volumes
adhar storage snapshot --all

# Restore in new cluster
adhar storage restore --snapshot-id snap-xxx
```

---

## Disaster Recovery

### Backup Strategy

#### Automated Backups
```yaml
backup:
  enabled: true
  schedule: "0 2 * * *"  # Daily at 2 AM
  retention: 30          # Days
  storage:
    type: s3
    bucket: adhar-backups
    region: us-east-1
```

#### Backup Components
- **Cluster State**: etcd snapshots
- **Application Manifests**: Git repository backups
- **Persistent Volumes**: Volume snapshots
- **Secrets**: Encrypted secret backups
- **Configuration**: Platform configuration

### Recovery Procedures

#### Full Cluster Recovery
```bash
# Create new cluster
adhar cluster create prod-recovery \
  --provider aws \
  --region us-west-2

# Restore from backup
adhar restore apply \
  --backup prod-backup-20251115 \
  --cluster prod-recovery

# Verify restoration
adhar health check --cluster prod-recovery
```

#### Application Recovery
```bash
# Restore specific application
adhar apps restore my-app \
  --backup app-backup-20251115 \
  --namespace production

# Verify application
adhar apps status my-app
```

### RTO/RPO Targets

| Component | RTO (Recovery Time) | RPO (Data Loss) |
|-----------|---------------------|-----------------|
| Platform Services | < 1 hour | < 24 hours |
| Applications | < 30 minutes | < 4 hours |
| Databases | < 2 hours | < 1 hour |
| Persistent Volumes | < 1 hour | < 24 hours |

---

## Multi-Cluster Management

### Federation Setup

```yaml
# Multi-cluster configuration
clusters:
  - name: prod-us-east
    provider: aws
    region: us-east-1
    role: primary
  
  - name: prod-us-west
    provider: aws
    region: us-west-2
    role: replica
  
  - name: prod-eu
    provider: gcp
    region: europe-west1
    role: replica
```

### Cross-Cluster Networking
```bash
# Setup cluster mesh
adhar network mesh enable \
  --clusters prod-us-east,prod-us-west

# Verify connectivity
adhar network test --source prod-us-east --dest prod-us-west
```

### Global Load Balancing
```bash
# Configure global LB
adhar network glb create \
  --name global-app \
  --clusters prod-us-east,prod-us-west,prod-eu \
  --algorithm latency
```

---

## Security Hardening

### Network Security

#### Network Policies
```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: default-deny
spec:
  endpointSelector: {}
  ingress:
    - fromEntities:
        - cluster
  egress:
    - toEntities:
        - cluster
        - kube-dns
```

#### Encryption
```yaml
# Enable WireGuard encryption
cilium:
  encryption:
    enabled: true
    type: wireguard
```

### Access Control

#### RBAC Configuration
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: developer
rules:
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets"]
    verbs: ["get", "list", "watch"]
```

#### Pod Security Standards
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: production
  labels:
    pod-security.kubernetes.io/enforce: restricted
```

### Secrets Management

#### External Secrets Operator
```yaml
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: vault-backend
spec:
  provider:
    vault:
      server: "https://vault.example.com"
      path: "secret"
      auth:
        kubernetes:
          mountPath: "kubernetes"
          role: "adhar-role"
```

---

## Performance Optimization

### Resource Optimization

#### Right-Sizing
```bash
# Analyze resource usage
kubectl top nodes
kubectl top pods --all-namespaces

# Get recommendations
adhar analyze resources --namespace production
```

#### Auto-Scaling
```yaml
# Horizontal Pod Autoscaler
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: app-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: my-app
  minReplicas: 2
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

### Database Optimization

#### Connection Pooling
```yaml
postgresql:
  pooler:
    enabled: true
    maxConnections: 100
    poolMode: transaction
```

#### Query Optimization
```bash
# Enable slow query logging
kubectl exec -it postgres-0 -- psql -c \
  "ALTER SYSTEM SET log_min_duration_statement = 1000"

# Analyze slow queries
kubectl logs postgres-0 | grep "duration:"
```

---

## Troubleshooting

### Common Production Issues

#### High API Server Latency
```bash
# Check API server metrics
kubectl top nodes
kubectl get --raw /metrics | grep apiserver

# Scale API servers
kubectl scale deployment/kube-apiserver --replicas=5
```

#### etcd Performance Issues
```bash
# Check etcd metrics
kubectl exec -it etcd-0 -- etcdctl endpoint status

# Defragment etcd
kubectl exec -it etcd-0 -- etcdctl defrag

# Check disk performance
kubectl exec -it etcd-0 -- fio --name=test --size=1G --rw=write
```

#### Network Connectivity Issues
```bash
# Test pod-to-pod connectivity
kubectl run test --rm -it --image=nicolaka/netshoot -- /bin/bash

# Check Cilium status
cilium status

# Debug network policies
cilium policy get
```

### Monitoring and Alerts

#### Critical Alerts
- API server down
- etcd cluster unhealthy
- High error rates
- Certificate expiration
- Disk space < 20%
- Memory pressure

#### Alert Configuration
```yaml
# Prometheus alert rules
groups:
  - name: platform-critical
    rules:
      - alert: HighErrorRate
        expr: rate(http_requests_total{status=~"5.."}[5m]) > 0.05
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "High error rate detected"
```

---

## Best Practices Summary

### ✅ Do's
- Enable HA mode for production
- Implement automated backups
- Use infrastructure as code
- Monitor everything
- Test disaster recovery procedures
- Implement security policies
- Document runbooks
- Perform regular upgrades

### ❌ Don'ts
- Run production without HA
- Store secrets in plain text
- Skip backup verification
- Ignore monitoring alerts
- Deploy without testing
- Use default passwords
- Disable security features
- Forget audit logging

---

## Additional Resources

- **[Architecture Guide](ARCHITECTURE.md)**: Platform architecture details
- **[User Guide](USER_GUIDE.md)**: Day-to-day usage
- **[Provider Guide](PROVIDER_GUIDE.md)**: Provider implementations
- **[Contributing](../CONTRIBUTING.md)**: Contribute to the project

---

**Questions?** Open an issue on [GitHub](https://github.com/adhar-io/adhar/issues)

