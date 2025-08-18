# ArgoCD Configuration

This directory contains Helm values files for deploying ArgoCD (GitOps Continuous Delivery) in different environments, optimized for both local development and production use cases.

## 📁 Files Overview

- `values.yaml` - **Non-HA configuration** for local development
- `values-ha.yaml` - **HA configuration** for production environments
- `generate-manifests.sh` - Script to generate Kubernetes manifests

## 🏠 Non-HA Configuration (`values.yaml`) - Local Development

**✅ Optimized for:**
- Resource efficiency for local development environments
- Minimal resource consumption suitable for development machines
- Simple single-instance deployment with basic Redis setup

**📋 Key Features:**
- **Application Controller**: 1 replica
- **Server**: 1 replica
- **Repo Server**: 1 replica
- **ApplicationSet Controller**: 1 replica
- **Redis**: Single Redis instance (HA disabled)
- **Horizontal Pod Autoscaler**: Disabled
- **Pod Disruption Budgets**: Disabled
- **Metrics**: Basic metrics without extensive monitoring

## 🏭 HA Configuration (`values-ha.yaml`) - Production

**✅ Optimized for:**
- High availability with multiple controller instances
- Production-grade Redis clustering for state management
- Enhanced monitoring and observability
- Pod distribution across nodes with disruption protection

**📋 Key Features:**
- **Application Controller**: 2 replicas (HA enabled)
- **Server**: 2 replicas (HA enabled)
- **Repo Server**: 2 replicas (HA enabled)
- **ApplicationSet Controller**: 1 replica (component limitation)
- **Redis**: HA cluster enabled (redis-ha subchart)
- **Horizontal Pod Autoscaler**: Enabled (min: 2, max: 5)
- **Pod Disruption Budgets**: Enabled (minAvailable: 1)
- **Metrics**: Full metrics stack with ServiceMonitors

## 🔍 Detailed Comparison

| **Component** | **Non-HA (Local)** | **HA (Production)** |
|---------------|-------------------|-------------------|
| **Application Controller Replicas** | 1 | 2 |
| **Server Replicas** | 1 | 2 |
| **Repo Server Replicas** | 1 | 2 |
| **ApplicationSet Replicas** | 1 | 1* |
| **Redis Configuration** | Single instance | HA cluster |
| **Horizontal Pod Autoscaler** | ❌ Disabled | ✅ Enabled |
| **HPA Min Replicas** | N/A | 2 |
| **HPA Max Replicas** | N/A | 5 |
| **Pod Disruption Budgets** | ❌ Disabled | ✅ Enabled |
| **PDB Min Available** | N/A | 1 |
| **Resource Footprint** | 🟢 Minimal | 🟡 Production-grade |
| **High Availability** | ❌ Single point of failure | ✅ Resilient to failures |
| **Auto-Scaling** | ❌ Manual scaling | ✅ Automatic scaling |

*ApplicationSet controller doesn't support multiple replicas due to controller design

## 🚀 Getting Started

### Prerequisites

- Kubernetes cluster (v1.19+)
- Helm 3.x installed
- `kubectl` configured to access your cluster
- Storage class available for persistent volumes (if using Redis persistence)

### Installation

#### 1. Add ArgoCD Helm Repository

```bash
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update
```

#### 2. Local Development Deployment

For local development environments (Kind, Minikube, etc.):

```bash
# Install ArgoCD with non-HA configuration
helm install argocd argo/argo-cd \
  --namespace argocd \
  --create-namespace \
  -f hack/argocd/values.yaml

# Wait for ArgoCD to be ready
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=argocd-server -n argocd --timeout=300s

# Get initial admin password
kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath="{.data.password}" | base64 -d; echo
```

#### 3. Production HA Deployment

For production environments:

```bash
# Install ArgoCD with HA configuration
helm install argocd argo/argo-cd \
  --namespace argocd \
  --create-namespace \
  -f hack/argocd/values-ha.yaml

# Verify HA setup
kubectl get pods -n argocd -l app.kubernetes.io/name=argocd-application-controller
kubectl get pods -n argocd -l app.kubernetes.io/name=argocd-server
kubectl get pods -n argocd -l app.kubernetes.io/name=argocd-repo-server
kubectl get pods -n argocd -l app.kubernetes.io/name=argocd-redis-ha
```

### Post-Installation Setup

#### 1. Access ArgoCD Web UI

```bash
# Port forward to access locally
kubectl port-forward svc/argocd-server -n argocd 8080:443

# Access at: https://localhost:8080
# Username: admin
# Password: (from previous step)
```

#### 2. Configure ArgoCD CLI

```bash
# Install ArgoCD CLI (macOS)
brew install argocd

# Login to ArgoCD
argocd login localhost:8080

# Change admin password
argocd account update-password
```

#### 3. Create Your First Application

```bash
# Create a sample application
kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: sample-app
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps.git
    targetRevision: HEAD
    path: guestbook
  destination:
    server: https://kubernetes.default.svc
    namespace: default
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
EOF
```

## 🏗️ Architecture Details

### Local Development Architecture

```
┌─────────────────┐
│ Application     │
│ Controller      │
│  (1 replica)    │
└─────────┬───────┘
          │
┌─────────▼───────┐    ┌─────────────────┐
│     Server      │    │   Repo Server   │
│  (1 replica)    │    │  (1 replica)    │
└─────────┬───────┘    └─────────┬───────┘
          │                      │
          └──────────┬───────────┘
                     │
          ┌──────────▼──────────┐
          │       Redis         │
          │   (single node)     │
          └─────────────────────┘
```

### Production HA Architecture

```
┌─────────────────┐    ┌─────────────────┐
│ Application     │    │ Application     │
│ Controller      │    │ Controller      │
│  (replica 1)    │    │  (replica 2)    │
└─────────┬───────┘    └─────────┬───────┘
          │                      │
          └──────────┬───────────┘
                     │
┌─────────────────┐  │  ┌─────────────────┐
│     Server      │  │  │     Server      │
│  (replica 1)    │  │  │  (replica 2)    │
└─────────┬───────┘  │  └─────────┬───────┘
          │          │            │
┌─────────▼───────┐  │  ┌─────────▼───────┐
│   Repo Server   │  │  │   Repo Server   │
│  (replica 1)    │  │  │  (replica 2)    │
└─────────┬───────┘  │  └─────────┬───────┘
          │          │            │
          └──────────┼────────────┘
                     │
          ┌──────────▼──────────┐
          │     Redis HA        │
          │ (master + sentinel) │
          │   + HAProxy LB      │
          └─────────────────────┘
```

## 📊 Monitoring (Production HA Only)

When using the HA configuration, comprehensive monitoring is available:

### Metrics Endpoints

```bash
# ArgoCD Server metrics
kubectl port-forward -n argocd service/argocd-server-metrics 8083:8083
# Access metrics at: http://localhost:8083/metrics

# Application Controller metrics
kubectl port-forward -n argocd service/argocd-application-controller-metrics 8082:8082

# Repo Server metrics
kubectl port-forward -n argocd service/argocd-repo-server-metrics 8084:8084

# Redis HA metrics
kubectl port-forward -n argocd service/argocd-redis-ha-haproxy-metrics 8090:8404
```

### Prometheus Integration

The HA configuration can be configured with ServiceMonitors for automatic Prometheus discovery:

```yaml
# Example ServiceMonitor for ArgoCD Server
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: argocd-server
  namespace: argocd
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: argocd-server-metrics
  endpoints:
  - port: metrics
    interval: 30s
    path: /metrics
```

### Key Metrics to Monitor

- **Application Sync**: Sync status, sync frequency, sync duration
- **Controller Performance**: Reconciliation time, queue depth
- **Repository Access**: Git operations, authentication failures
- **Redis Performance**: Memory usage, connection count, command latency
- **Resource Usage**: CPU, memory, network for all components

## 🔧 Customization

### Configure External Redis

To use an external Redis instead of the built-in Redis HA:

```yaml
# In values-ha.yaml
redis-ha:
  enabled: false

# Configure external Redis
redis:
  externalRedis:
    host: redis.example.com
    port: 6379
    password: your-redis-password
```

### Enable Ingress

```yaml
server:
  ingress:
    enabled: true
    ingressClassName: nginx
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
      nginx.ingress.kubernetes.io/backend-protocol: "GRPC"
    hosts:
      - argocd.example.com
    tls:
      - secretName: argocd-server-tls
        hosts:
          - argocd.example.com
```

### Configure OIDC Authentication

```yaml
server:
  config:
    oidc.config: |
      name: OIDC
      issuer: https://your-oidc-provider.com
      clientId: argocd
      clientSecret: $oidc.clientSecret
      requestedScopes: ["openid", "profile", "email", "groups"]
      requestedIDTokenClaims: {"groups": {"essential": true}}
  rbacConfig:
    policy.default: role:readonly
    policy.csv: |
      p, role:admin, applications, *, */*, allow
      p, role:admin, clusters, *, *, allow
      p, role:admin, repositories, *, *, allow
      g, argocd-admins, role:admin
```

### Configure Repository Access

```yaml
configs:
  repositories:
    - url: https://github.com/your-org/your-repo.git
      type: git
      name: your-repo
      project: default
    - url: https://charts.your-domain.com
      type: helm
      name: your-helm-repo
```

## 🔒 Security Considerations

### Production Security Checklist

- [ ] **Change Default Password**: Update admin password immediately
- [ ] **Enable HTTPS**: Use proper TLS certificates
- [ ] **Configure RBAC**: Set up proper role-based access control
- [ ] **Repository Access**: Use SSH keys or tokens for Git access
- [ ] **Network Policies**: Restrict traffic between components
- [ ] **Secret Management**: Use external secret management
- [ ] **Audit Logging**: Enable comprehensive audit logs
- [ ] **Regular Updates**: Keep ArgoCD and dependencies updated

### RBAC Configuration

```yaml
server:
  rbacConfig:
    policy.default: role:readonly
    policy.csv: |
      # Admin permissions
      p, role:admin, applications, *, */*, allow
      p, role:admin, clusters, *, *, allow
      p, role:admin, repositories, *, *, allow
      
      # Developer permissions
      p, role:developer, applications, get, */*, allow
      p, role:developer, applications, sync, */*, allow
      
      # Group mappings
      g, argocd-admins, role:admin
      g, developers, role:developer
```

## 💾 Backup and Recovery

### Application Definitions Backup

```bash
# Export all applications
kubectl get applications -n argocd -o yaml > argocd-applications-backup.yaml

# Export ArgoCD configuration
kubectl get configmap argocd-cm -n argocd -o yaml > argocd-config-backup.yaml
kubectl get configmap argocd-rbac-cm -n argocd -o yaml > argocd-rbac-backup.yaml
```

### Redis Data Backup (HA Configuration)

```bash
# Create Redis backup
kubectl exec -n argocd argocd-redis-ha-server-0 -- redis-cli BGSAVE

# Copy backup from Redis pod
kubectl cp argocd/argocd-redis-ha-server-0:/data/dump.rdb ./redis-backup.rdb
```

### Disaster Recovery

```bash
# Restore applications
kubectl apply -f argocd-applications-backup.yaml

# Restore configuration
kubectl apply -f argocd-config-backup.yaml
kubectl apply -f argocd-rbac-backup.yaml

# Restart ArgoCD components to reload config
kubectl rollout restart deployment/argocd-server -n argocd
kubectl rollout restart deployment/argocd-repo-server -n argocd
```

## 🐛 Troubleshooting

### Common Issues

#### 1. Applications Not Syncing

```bash
# Check application status
kubectl get applications -n argocd

# Describe specific application
kubectl describe application your-app -n argocd

# Check controller logs
kubectl logs -n argocd deployment/argocd-application-controller
```

#### 2. Repository Access Issues

```bash
# Test repository connectivity
kubectl exec -n argocd deployment/argocd-repo-server \
  -- git ls-remote https://github.com/your-org/your-repo.git

# Check repository configuration
kubectl get configmap argocd-cm -n argocd -o yaml
```

#### 3. Performance Issues

```bash
# Check resource usage
kubectl top pods -n argocd

# Check Redis performance (HA setup)
kubectl exec -n argocd argocd-redis-ha-server-0 -- redis-cli info stats

# Check application controller performance
kubectl logs -n argocd deployment/argocd-application-controller | grep "level=info"
```

#### 4. HA Configuration Issues

```bash
# Verify multiple replicas
kubectl get pods -n argocd -l app.kubernetes.io/name=argocd-server -o wide

# Check Redis HA status
kubectl exec -n argocd argocd-redis-ha-server-0 -- redis-cli info replication

# Check HAProxy status
kubectl logs -n argocd deployment/argocd-redis-ha-haproxy
```

#### 5. SSL/TLS Issues

```bash
# Check certificate
kubectl get secret argocd-server-tls -n argocd -o yaml

# Verify ingress configuration
kubectl describe ingress argocd-server-ingress -n argocd

# Test TLS handshake
openssl s_client -connect argocd.example.com:443 -servername argocd.example.com
```

### Debugging Commands

```bash
# Get all ArgoCD resources
kubectl get all -n argocd -l app.kubernetes.io/part-of=argocd

# Check service endpoints
kubectl get endpoints -n argocd

# View events
kubectl get events -n argocd --sort-by='.lastTimestamp'

# Check ArgoCD configuration
kubectl get configmap argocd-cm -n argocd -o yaml

# Test internal DNS resolution
kubectl run debug --image=busybox --rm -it --restart=Never \
  -- nslookup argocd-server.argocd.svc.cluster.local
```

## 🚀 Advanced Configuration

### Multi-Cluster Management

```yaml
# Add external cluster
clusters:
  - name: production-cluster
    server: https://prod-k8s-api.example.com
    config:
      bearerToken: your-service-account-token
      tlsClientConfig:
        insecure: false
        caData: LS0tLS1CRUdJTi... # base64 encoded CA cert
```

### ApplicationSet for Multiple Environments

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: multi-env-apps
  namespace: argocd
spec:
  generators:
  - list:
      elements:
      - cluster: development
        url: https://dev-k8s-api.example.com
        namespace: dev
      - cluster: staging
        url: https://staging-k8s-api.example.com
        namespace: staging
      - cluster: production
        url: https://prod-k8s-api.example.com
        namespace: prod
  template:
    metadata:
      name: '{{cluster}}-app'
    spec:
      project: default
      source:
        repoURL: https://github.com/your-org/your-app.git
        targetRevision: HEAD
        path: manifests/{{cluster}}
      destination:
        server: '{{url}}'
        namespace: '{{namespace}}'
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
```

### Progressive Delivery with Argo Rollouts

```yaml
# Install Argo Rollouts
kubectl create namespace argo-rollouts
kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml

# Example Rollout
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: example-rollout
spec:
  replicas: 5
  strategy:
    canary:
      steps:
      - setWeight: 20
      - pause: {}
      - setWeight: 40
      - pause: {duration: 10}
      - setWeight: 60
      - pause: {duration: 10}
      - setWeight: 80
      - pause: {duration: 10}
  selector:
    matchLabels:
      app: rollout-example
  template:
    metadata:
      labels:
        app: rollout-example
    spec:
      containers:
      - name: rollouts-demo
        image: argoproj/rollouts-demo:blue
        ports:
        - name: http
          containerPort: 8080
          protocol: TCP
```

## 📚 Additional Resources

- [ArgoCD Documentation](https://argo-cd.readthedocs.io/)
- [ArgoCD Best Practices](https://argo-cd.readthedocs.io/en/stable/user-guide/best_practices/)
- [GitOps Principles](https://opengitops.dev/)
- [Argo Rollouts Documentation](https://argoproj.github.io/argo-rollouts/)
- [ApplicationSet Documentation](https://argo-cd.readthedocs.io/en/stable/user-guide/application-set/)

## 🤝 Contributing

When modifying the values files:

1. **Test Thoroughly**: Test changes in both local and production-like environments
2. **Version Compatibility**: Ensure compatibility with the target ArgoCD version
3. **Documentation**: Update this README if adding new configuration options
4. **Security Review**: Review security implications of configuration changes
5. **Backup**: Always backup existing configurations before major changes

## ⚠️ Important Notes

- **Resource Requirements**: HA configuration requires significantly more resources
- **Redis Migration**: Switching between single Redis and Redis HA requires data migration
- **ApplicationSet Limitations**: ApplicationSet controller doesn't support multiple replicas
- **Network Policies**: HA configuration requires additional network connectivity
- **Monitoring Overhead**: HA configuration generates more metrics and logs
- **Backup Strategy**: Critical for production - test restore procedures regularly
- **Git Repository Access**: Ensure proper SSH keys or tokens are configured
- **Certificate Management**: Use proper TLS certificates for production deployments

## 🔄 Migration Guide

### From Non-HA to HA

1. **Backup Configuration**: Export all applications and configurations
2. **Plan Maintenance Window**: ArgoCD will be unavailable during migration
3. **Deploy HA Components**: Deploy Redis HA and additional replicas
4. **Migrate Redis Data**: Transfer data from single Redis to HA setup
5. **Update Configuration**: Switch to HA values file
6. **Verify Operation**: Test all applications and functionality
7. **Monitor**: Watch for any performance or connectivity issues

### Version Upgrades

```bash
# Upgrade ArgoCD
helm upgrade argocd argo/argo-cd \
  --namespace argocd \
  -f hack/argocd/values-ha.yaml \
  --version 5.46.0

# Monitor the upgrade
kubectl rollout status deployment/argocd-server -n argocd
kubectl rollout status deployment/argocd-repo-server -n argocd
kubectl rollout status statefulset/argocd-application-controller -n argocd
```

## 🏆 Production Checklist

- [ ] HA configuration deployed and tested
- [ ] External authentication configured (OIDC/LDAP)
- [ ] RBAC policies implemented
- [ ] TLS certificates installed and valid
- [ ] Monitoring and alerting configured
- [ ] Backup procedures tested
- [ ] Disaster recovery plan documented
- [ ] Performance tuning completed
- [ ] Security scan performed
- [ ] Documentation updated
