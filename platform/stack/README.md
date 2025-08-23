# Platform Stack - ArgoCD Integration

This directory contains the configuration files for integrating ArgoCD with the Adhar platform, ensuring resilient and stable GitOps operations.

## 🏗️ Architecture Overview

### Resilient Design Principles

1. **Service Name Resolution**: Use service names instead of IP addresses
2. **Dedicated Services**: Separate services for different access patterns
3. **Configuration Management**: Centralized configuration via ConfigMaps
4. **Automatic Recovery**: Self-healing mechanisms for service changes

### Components

#### 1. Gitea Services
- **`gitea-http`**: Headless service for direct pod access
- **`gitea-http-clusterip`**: ClusterIP service (may change IP on restarts)
- **`gitea-argocd`**: Dedicated service for ArgoCD access (most stable)

#### 2. ArgoCD Configuration
- **`gitea-argocd-config`**: ConfigMap for service discovery
- **`repo-environments`**: Secret for environments repository access
- **`repo-packages`**: Secret for packages repository access

#### 3. Repository Secrets
- **Authentication**: Gitea admin credentials
- **URLs**: Service-based URLs (not IP-based)
- **Security**: Insecure flag for internal cluster communication

## 🔧 Configuration Files

### `argocd-auth.yaml`
Main configuration file containing:
- Dedicated Gitea service for ArgoCD
- Repository authentication secrets
- Service discovery ConfigMap

### `update-argocd-endpoints.sh`
Utility script for updating endpoints when services change:
```bash
./platform/stack/update-argocd-endpoints.sh
```

## 🚀 Usage

### Initial Setup
```bash
# Apply the complete configuration
kubectl apply -f platform/stack/argocd-auth.yaml

# Restart ArgoCD to pick up changes
kubectl rollout restart deployment argo-cd-argocd-repo-server -n adhar-system
```

### Updating Endpoints
```bash
# Run the update script when services change
./platform/stack/update-argocd-endpoints.sh
```

### Verification
```bash
# Check service status
kubectl get svc -n adhar-system | grep gitea

# Check repository secrets
kubectl get secrets -n adhar-system | grep repo

# Check ArgoCD logs
kubectl logs -n adhar-system deployment/argo-cd-argocd-repo-server
```

## 🔍 Troubleshooting

### Common Issues

1. **Connection Refused**
   - Service IP may have changed
   - Run `update-argocd-endpoints.sh`
   - Restart ArgoCD repo-server

2. **Repository Not Found**
   - Verify Gitea repositories exist
   - Check repository permissions
   - Verify admin credentials

3. **Service Unavailable**
   - Check Gitea pod status
   - Verify service selectors
   - Check network policies

### Debugging Commands

```bash
# Test service connectivity
kubectl run test-connectivity --image=curlimages/curl --rm -it --restart=Never -- \
  curl -v "http://gitea-argocd.adhar-system.svc.cluster.local:3000/gitea_admin/packages"

# Check service endpoints
kubectl get endpoints gitea-argocd -n adhar-system

# Verify DNS resolution
kubectl run test-dns --image=busybox --rm -it --restart=Never -- \
  nslookup gitea-argocd.adhar-system.svc.cluster.local
```

## 📋 Service URLs

### Current Configuration
- **Environments**: `http://gitea-argocd.adhar-system.svc.cluster.local:3000/gitea_admin/environments`
- **Packages**: `http://gitea-argocd.adhar-system.svc.cluster.local:3000/gitea_admin/packages`

### Fallback URLs
- **Primary**: `gitea-argocd.adhar-system.svc.cluster.local`
- **Secondary**: `gitea-http.adhar-system.svc.cluster.local`
- **Legacy**: `gitea-http-clusterip.adhar-system.svc.cluster.local`

## 🔒 Security

### Authentication
- **Username**: `gitea_admin`
- **Password**: Stored in Kubernetes secrets
- **Access**: Internal cluster only

### Network Security
- **Internal Access**: Services only accessible within cluster
- **No External Exposure**: All communication internal
- **Service Mesh**: Compatible with Istio/Linkerd if needed

## 📈 Monitoring

### Health Checks
- **Service Status**: Monitor service endpoints
- **Pod Health**: Check Gitea and ArgoCD pod status
- **Connection Logs**: Monitor ArgoCD repository access logs

### Metrics
- **Service Response Time**: Monitor Gitea service performance
- **Repository Access**: Track successful/failed repository connections
- **Sync Status**: Monitor ArgoCD application sync status

## 🔄 Maintenance

### Regular Tasks
1. **Service Monitoring**: Check service health weekly
2. **Log Review**: Review ArgoCD logs for issues
3. **Configuration Updates**: Update endpoints when services change
4. **Security Updates**: Rotate credentials periodically

### Emergency Procedures
1. **Service Failure**: Restart affected services
2. **Connectivity Issues**: Run endpoint update script
3. **Authentication Problems**: Verify secret configuration
4. **Repository Issues**: Check Gitea repository status

## 📚 References

- [ArgoCD Documentation](https://argo-cd.readthedocs.io/)
- [Kubernetes Services](https://kubernetes.io/docs/concepts/services-networking/service/)
- [Gitea Documentation](https://docs.gitea.io/)
- [Adhar Platform Documentation](../README.md)
