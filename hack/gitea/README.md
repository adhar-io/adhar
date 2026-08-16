# Gitea Configuration

This directory contains Helm values files for deploying Gitea (Git hosting platform) in different environments, optimized for both local development and production use cases.

## 🔼 Version: Gitea v1.27.0 (gitea chart 12.7.0)

Bumped from v1.26.1 (chart 12.6.0). Regenerate with `./generate-manifests.sh`
(optional arg overrides the chart version). Minor bump, same chart major — the
values key surface is unchanged and a manifest diff confirmed the resource
(`kind`) set is identical, only image/chart version labels moved.

**Manual patches folded into `values.yaml`/`values-ha.yaml`** so the generator is
now the single source of truth (they used to be hand-edits on the rendered
manifest and were silently lost on any regen):

- **Keycloak SSO auto-onboarding** — `gitea.config.oauth2_client`
  (`ENABLE_AUTO_REGISTRATION`, `USERNAME=preferred_username`, `ACCOUNT_LINKING=auto`).
- **Platform CA trust** — `extraVolumes` + `extraContainerVolumeMounts` drop
  `adhar-cert` into the main container's `/etc/ssl/certs` so OIDC discovery against
  the self-signed issuer works (both variants now carry it — HA had been missing it).
- **HA external database** — `values-ha.yaml` now disables the chart-bundled
  `postgresql-ha` subchart and points `gitea.config.database` at the CNPG
  `gitea-db-rw` service (roadmap P1.2b), matching `resources/cnpg/gitea-db.yaml`.
  Asserted by `ha_test.go`.

**New in chart 12.7.0, available but not enabled:** `gatewayAPI.*` renders a
native Gateway API `HTTPRoute`/`BackendTLSPolicy`. Left **disabled** — Adhar
provides its own Gitea `HTTPRoute` in `resources/gitea/post-install.yaml` that
strips the `/gitea` sub-path (a reverse-proxy detail the generic chart route would
not replicate); enabling the chart's would create a conflicting duplicate.

## 📁 Files Overview

- `values.yaml` - **Non-HA configuration** for local development
- `values-ha.yaml` - **HA configuration** for production environments
- `generate-manifests.sh` - Script to generate Kubernetes manifests

## 🏠 Non-HA Configuration (`values.yaml`) - Local Development

**✅ Optimized for:**
- Resource efficiency for local development environments
- Minimal resource consumption suitable for development machines
- Simple single-instance deployment with basic database setup

**📋 Key Features:**
- **Gitea Application**: 1 replica
- **Database**: Single PostgreSQL instance
- **Cache**: Single Redis/Valkey instance
- **Resource Allocation**: CPU 50m-200m, Memory 128Mi-512Mi
- **Storage**: PostgreSQL 5Gi (minimal for development)
- **Metrics**: Disabled (reduces overhead)
- **Pod Disruption Budget**: Disabled (not needed for single instances)

## 🏭 HA Configuration (`values-ha.yaml`) - Production

**✅ Optimized for:**
- High availability with multiple application instances
- Production-grade database and cache clustering
- Enhanced monitoring and observability
- Resilient storage and backup strategies

**📋 Key Features:**
- **Gitea Application**: 2 replicas (HA enabled)
- **Database**: PostgreSQL HA cluster with pgpool
- **Cache**: Redis/Valkey cluster (3 nodes + 1 replica each)
- **Resource Allocation**: CPU 200m-1000m, Memory 512Mi-2048Mi
- **Storage**: PostgreSQL HA 20Gi (production-grade)
- **Metrics**: Enabled with ServiceMonitor for Prometheus
- **Pod Disruption Budget**: Enabled (minAvailable: 1)

## 🔍 Detailed Comparison

| **Component** | **Non-HA (Local)** | **HA (Production)** |
|---------------|-------------------|-------------------|
| **Gitea Replicas** | 1 | 2 |
| **CPU Limits** | 200m | 1000m |
| **Memory Limits** | 512Mi | 2048Mi |
| **CPU Requests** | 50m | 200m |
| **Memory Requests** | 128Mi | 512Mi |
| **Database** | Single PostgreSQL | PostgreSQL HA cluster |
| **Cache** | Single Redis/Valkey | Redis/Valkey cluster (3+1) |
| **PostgreSQL Storage** | 5Gi | 20Gi |
| **Metrics Collection** | ❌ Disabled | ✅ Enabled |
| **ServiceMonitor** | ❌ Disabled | ✅ Enabled |
| **Pod Disruption Budget** | ❌ Disabled | ✅ Enabled |
| **Resource Footprint** | 🟢 Minimal | 🟡 Production-grade |
| **High Availability** | ❌ Single point of failure | ✅ Resilient to failures |

## 🚀 Getting Started

### Prerequisites

- Kubernetes cluster (v1.19+)
- Helm 3.x installed
- `kubectl` configured to access your cluster
- Storage class available for persistent volumes

### Installation

#### 1. Add Gitea Helm Repository

```bash
helm repo add gitea-charts https://dl.gitea.com/charts/
helm repo update
```

#### 2. Local Development Deployment

For local development environments (Kind, Minikube, etc.):

```bash
# Install Gitea with non-HA configuration
helm install gitea gitea-charts/gitea \
  --namespace adhar-system \
  --create-namespace \
  -f hack/gitea/values.yaml

# Wait for deployment to be ready
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=gitea -n adhar-system --timeout=300s

# Get Gitea admin credentials
kubectl get secret gitea-admin-secret -n adhar-system -o jsonpath='{.data.password}' | base64 -d
```

#### 3. Production HA Deployment

For production environments:

```bash
# Install Gitea with HA configuration
helm install gitea gitea-charts/gitea \
  --namespace adhar-system \
  --create-namespace \
  -f hack/gitea/values-ha.yaml

# Verify HA setup
kubectl get pods -n adhar-system -l app.kubernetes.io/name=gitea
kubectl get pods -n adhar-system -l app.kubernetes.io/name=postgresql-ha
kubectl get pods -n adhar-system -l app.kubernetes.io/name=valkey-cluster
```

### Post-Installation Setup

#### 1. Access Gitea Web Interface

```bash
# Port forward to access locally
kubectl port-forward -n adhar-system service/gitea-http 3000:3000

# Access at: http://localhost:3000
```

#### 2. Initial Admin Setup

```bash
# Get admin credentials
echo "Username: gitea"
echo "Password: $(kubectl get secret gitea-admin-secret -n adhar-system -o jsonpath='{.data.password}' | base64 -d)"
```

#### 3. Configure Git Access

```bash
# For HTTP clone (replace with your ingress domain)
git clone http://localhost:3000/username/repository.git

# For SSH access (if SSH service is enabled)
git clone git@gitea.example.com:username/repository.git
```

## 🏗️ Architecture Details

### Local Development Architecture

```
┌─────────────────┐
│     Gitea       │
│   (1 replica)   │
└─────────┬───────┘
          │
    ┌─────▼─────┐
    │PostgreSQL │
    │ (single)  │
    └─────┬─────┘
          │
    ┌─────▼─────┐
    │   Redis   │
    │ (single)  │
    └───────────┘
```

### Production HA Architecture

```
┌─────────────────┐    ┌─────────────────┐
│     Gitea       │    │     Gitea       │
│   (replica 1)   │    │   (replica 2)   │
└─────────┬───────┘    └─────────┬───────┘
          │                      │
          └──────────┬───────────┘
                     │
          ┌──────────▼──────────┐
          │   PostgreSQL HA     │
          │ (master + standby)  │
          │     + pgpool        │
          └──────────┬──────────┘
                     │
          ┌──────────▼──────────┐
          │  Redis/Valkey       │
          │    Cluster          │
          │ (3 nodes + replicas)│
          └─────────────────────┘
```

## 📊 Monitoring (Production HA Only)

When using the HA configuration, comprehensive monitoring is enabled:

### Metrics Endpoints

```bash
# Check Gitea metrics
kubectl port-forward -n adhar-system service/gitea-http 3000:3000
# Access metrics at: http://localhost:3000/metrics

# Check PostgreSQL metrics (if exporter enabled)
kubectl port-forward -n adhar-system service/postgresql-ha-metrics 9187:9187

# Check Redis metrics (if exporter enabled)
kubectl port-forward -n adhar-system service/valkey-cluster-metrics 9121:9121
```

### Prometheus Integration

The HA configuration includes ServiceMonitor for automatic Prometheus discovery:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: gitea
  namespace: adhar-system
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: gitea
  endpoints:
  - port: http
    path: /metrics
    interval: 30s
```

### Key Metrics to Monitor

- **Application Metrics**: Request rate, response time, error rate
- **Database Metrics**: Connection pool, query performance, replication lag
- **Cache Metrics**: Hit/miss ratio, memory usage, connections
- **Storage Metrics**: Disk usage, I/O performance

## 🔧 Customization

### Modify Resource Limits

Edit the values files to adjust resource allocation:

```yaml
# In values.yaml or values-ha.yaml
resources:
  limits:
    cpu: 1000m      # Adjust CPU limit
    memory: 2048Mi  # Adjust memory limit
  requests:
    cpu: 200m       # Adjust CPU request
    memory: 512Mi   # Adjust memory request
```

### Configure External Database

To use an external database instead of the built-in PostgreSQL:

```yaml
# Disable built-in databases
postgresql:
  enabled: false
postgresql-ha:
  enabled: false

# Configure external database in gitea config
gitea:
  config:
    database:
      DB_TYPE: postgres
      HOST: external-postgres.example.com:5432
      NAME: gitea
      USER: gitea
      PASSWD: your-password
```

### Enable Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: gitea.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: gitea-tls
      hosts:
        - gitea.example.com
```

### Configure LDAP/OAuth

```yaml
gitea:
  config:
    service:
      DISABLE_REGISTRATION: true
    oauth2:
      ENABLE: true
    ldap:
      ENABLED: true
```

## 🔒 Security Considerations

### Production Security Checklist

- [ ] **Strong Passwords**: Change default passwords for all services
- [ ] **TLS/SSL**: Enable HTTPS with proper certificates
- [ ] **Network Policies**: Restrict traffic between components
- [ ] **RBAC**: Configure proper Kubernetes RBAC
- [ ] **Secrets Management**: Use external secret management (Vault, etc.)
- [ ] **Database Encryption**: Enable encryption at rest
- [ ] **Backup Strategy**: Regular automated backups
- [ ] **Update Strategy**: Keep all components updated

### Secret Management

```bash
# Create strong passwords
kubectl create secret generic gitea-admin-secret \
  --from-literal=password=$(openssl rand -base64 32) \
  -n adhar-system

# Database credentials
kubectl create secret generic postgresql-credentials \
  --from-literal=postgres-password=$(openssl rand -base64 32) \
  --from-literal=repmgr-password=$(openssl rand -base64 32) \
  -n adhar-system
```

## 💾 Backup and Recovery

### Database Backup (Production)

```bash
# Create database backup
kubectl exec -n adhar-system deployment/postgresql-ha-postgresql \
  -- pg_dump -U gitea gitea > gitea-backup-$(date +%Y%m%d).sql

# Restore from backup
kubectl exec -i -n adhar-system deployment/postgresql-ha-postgresql \
  -- psql -U gitea gitea < gitea-backup-20240101.sql
```

### Git Repository Backup

```bash
# Backup all repositories
kubectl exec -n adhar-system deployment/gitea \
  -- tar czf /tmp/gitea-repos-$(date +%Y%m%d).tar.gz /data/git/repositories

# Copy backup from pod
kubectl cp adhar-system/gitea-pod:/tmp/gitea-repos-20240101.tar.gz ./gitea-repos-backup.tar.gz
```

## 🐛 Troubleshooting

### Common Issues

#### 1. Pods Not Starting

```bash
# Check pod status and events
kubectl describe pods -n adhar-system -l app.kubernetes.io/name=gitea

# Check resource constraints
kubectl top pods -n adhar-system

# Check storage issues
kubectl get pvc -n adhar-system
```

#### 2. Database Connection Issues

```bash
# Check PostgreSQL status
kubectl get pods -n adhar-system -l app.kubernetes.io/name=postgresql

# Check database logs
kubectl logs -n adhar-system deployment/postgresql-ha-postgresql

# Test database connection
kubectl exec -n adhar-system deployment/gitea \
  -- pg_isready -h postgresql-ha-pgpool -p 5432
```

#### 3. Performance Issues

```bash
# Check resource usage
kubectl top pods -n adhar-system

# Check database performance
kubectl exec -n adhar-system deployment/postgresql-ha-postgresql \
  -- psql -U gitea -c "SELECT * FROM pg_stat_activity;"

# Check cache performance
kubectl exec -n adhar-system deployment/valkey-cluster \
  -- redis-cli info stats
```

#### 4. HA Configuration Issues

```bash
# Verify multiple Gitea replicas
kubectl get pods -n adhar-system -l app.kubernetes.io/name=gitea -o wide

# Check PostgreSQL HA status
kubectl exec -n adhar-system deployment/postgresql-ha-postgresql \
  -- repmgr cluster show

# Check Redis cluster status
kubectl exec -n adhar-system deployment/valkey-cluster \
  -- redis-cli cluster nodes
```

### Debugging Commands

```bash
# Get all Gitea-related resources
kubectl get all -n adhar-system -l app.kubernetes.io/name=gitea

# Check service endpoints
kubectl get endpoints -n adhar-system

# View all events in namespace
kubectl get events -n adhar-system --sort-by='.lastTimestamp'

# Check ingress configuration
kubectl describe ingress -n adhar-system

# Test internal connectivity
kubectl run debug --image=busybox --rm -it --restart=Never \
  -- nslookup gitea-http.adhar-system.svc.cluster.local
```

## 📚 Additional Resources

- [Gitea Documentation](https://docs.gitea.io/)
- [Gitea Helm Chart](https://gitea.com/gitea/helm-gitea)
- [PostgreSQL HA Documentation](https://github.com/bitnami/charts/tree/main/bitnami/postgresql-ha)
- [Redis Cluster Documentation](https://redis.io/topics/cluster-tutorial)
- [Kubernetes Storage Best Practices](https://kubernetes.io/docs/concepts/storage/)

## 🤝 Contributing

When modifying the values files:

1. **Test Thoroughly**: Test changes in both local and production-like environments
2. **Resource Planning**: Ensure resource limits are appropriate for the target environment
3. **Documentation**: Update this README if adding new configuration options
4. **Backward Compatibility**: Verify backward compatibility with existing deployments
5. **Security Review**: Review security implications of configuration changes

## ⚠️ Important Notes

- **Resource Requirements**: HA configuration requires significantly more resources
- **Database Migration**: Switching between single PostgreSQL and PostgreSQL HA requires data migration
- **Cache Switching**: Moving between single Redis and Redis cluster requires configuration updates
- **Storage Classes**: Ensure appropriate storage classes are available for persistent volumes
- **Network Policies**: HA configuration requires additional network connectivity
- **Backup Strategy**: Critical for production environments - test restore procedures regularly
- **Monitoring**: HA configuration generates more metrics and logs - plan storage accordingly

## 🔄 Migration Guide

### From Non-HA to HA

1. **Backup Data**: Create full backup of database and repositories
2. **Plan Downtime**: Schedule maintenance window for migration
3. **Deploy HA Components**: Deploy PostgreSQL HA and Redis cluster
4. **Migrate Data**: Transfer data from single instances to HA setup
5. **Update Configuration**: Switch Gitea to use HA components
6. **Verify Operation**: Test all functionality in HA mode
7. **Monitor**: Closely monitor the system post-migration

### Rolling Updates

```bash
# Update Gitea version
helm upgrade gitea gitea-charts/gitea \
  --namespace adhar-system \
  -f hack/gitea/values-ha.yaml \
  --set image.tag=1.21.0

# Monitor rollout
kubectl rollout status deployment/gitea -n adhar-system
```
