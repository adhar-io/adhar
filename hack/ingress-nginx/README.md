# Ingress-Nginx Configuration

This directory contains Helm values files for deploying the NGINX Ingress Controller, which provides ingress capabilities for Kubernetes clusters. The configuration is designed to be scalable and adaptable to both development and production environments.

## 📁 Files Overview

- `values.yaml` - **Unified configuration** suitable for all environments
- `generate-manifests.sh` - Script to generate Kubernetes manifests

## 🌐 Ingress-Nginx Overview

NGINX Ingress Controller is a Kubernetes-native load balancer that manages external access to services in a cluster. It provides:

- **HTTP/HTTPS Load Balancing**: Route traffic to backend services
- **SSL/TLS Termination**: Handle certificates and encryption
- **Path-based Routing**: Route requests based on URL paths
- **Host-based Routing**: Route requests based on hostnames
- **Rate Limiting**: Control traffic flow and prevent abuse
- **Authentication**: Integrate with various auth providers

## ⚙️ Configuration Features

### 🔧 **Core Settings**
- **Namespace**: Deployed in `adhar-system` namespace
- **Default Replicas**: 1 (easily scalable)
- **Service Type**: LoadBalancer (configurable)
- **Image**: `registry.k8s.io/ingress-nginx/controller:v1.12.2`
- **Security**: Runs as non-root with security contexts

### 📊 **Scaling Configuration**
```yaml
controller:
  replicaCount: 1                    # Base replica count
  minAvailable: 1                    # PDB minimum available
  
  autoscaling:
    enabled: false                   # HPA disabled by default
    minReplicas: 1                   # Minimum replicas when HPA enabled
    maxReplicas: 11                  # Maximum replicas when HPA enabled
    targetCPUUtilizationPercentage: 50
    targetMemoryUtilizationPercentage: 50
```

### 🌍 **Service Configuration**
```yaml
service:
  type: LoadBalancer               # External access via LoadBalancer
  annotations: {}                  # Cloud provider specific annotations
  externalTrafficPolicy: ""        # Source IP preservation
  loadBalancerSourceRanges: []     # IP whitelist for LoadBalancer
```

### 📈 **Monitoring & Metrics**
```yaml
metrics:
  enabled: false                   # Metrics disabled by default
  service:
    enabled: true                  # Metrics service available
  serviceMonitor:
    enabled: false                 # Prometheus ServiceMonitor
```

## 🚀 Getting Started

### Prerequisites

- Kubernetes cluster (v1.19+)
- Helm 3.x installed
- `kubectl` configured to access your cluster
- LoadBalancer support (cloud provider or MetalLB for on-premises)

### Installation

#### 1. Add NGINX Ingress Helm Repository

```bash
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo update
```

#### 2. Deploy Ingress-Nginx

```bash
# Install NGINX Ingress Controller
helm install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace adhar-system \
  --create-namespace \
  -f hack/ingress-nginx/values.yaml

# Wait for deployment to be ready
kubectl wait --namespace adhar-system \
  --for=condition=ready pod \
  --selector=app.kubernetes.io/component=controller \
  --timeout=120s
```

#### 3. Verify Installation

```bash
# Check controller status
kubectl get pods -n adhar-system -l app.kubernetes.io/name=ingress-nginx

# Check service and external IP
kubectl get svc -n adhar-system ingress-nginx-controller

# Get external IP (for LoadBalancer)
kubectl get svc ingress-nginx-controller -n adhar-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

## 🔧 Environment-Specific Configurations

### 🏠 Local Development (Kind/Minikube)

For local development, you might want to use NodePort instead of LoadBalancer:

```yaml
# Override for local development
controller:
  service:
    type: NodePort
    nodePorts:
      http: 30080
      https: 30443
```

Deploy with override:
```bash
helm install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace adhar-system \
  --create-namespace \
  -f hack/ingress-nginx/values.yaml \
  --set controller.service.type=NodePort \
  --set controller.service.nodePorts.http=30080 \
  --set controller.service.nodePorts.https=30443
```

### 🏭 Production Environment

For production, enable autoscaling and monitoring:

```bash
helm install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace adhar-system \
  --create-namespace \
  -f hack/ingress-nginx/values.yaml \
  --set controller.replicaCount=3 \
  --set controller.autoscaling.enabled=true \
  --set controller.autoscaling.minReplicas=3 \
  --set controller.autoscaling.maxReplicas=10 \
  --set controller.metrics.enabled=true \
  --set controller.metrics.serviceMonitor.enabled=true
```

### ☁️ Cloud Provider Specific

#### AWS (EKS)
```yaml
controller:
  service:
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-type: nlb
      service.beta.kubernetes.io/aws-load-balancer-backend-protocol: tcp
      service.beta.kubernetes.io/aws-load-balancer-cross-zone-load-balancing-enabled: "true"
```

#### Google Cloud (GKE)
```yaml
controller:
  service:
    annotations:
      cloud.google.com/load-balancer-type: External
      cloud.google.com/backend-config: '{"default": "ingress-backendconfig"}'
```

#### Azure (AKS)
```yaml
controller:
  service:
    annotations:
      service.beta.kubernetes.io/azure-load-balancer-internal: "false"
      service.beta.kubernetes.io/azure-dns-label-name: your-ingress
```

## 📝 Creating Ingress Resources

### Basic HTTP Ingress

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: example-ingress
  namespace: default
  annotations:
    kubernetes.io/ingress.class: nginx
spec:
  rules:
  - host: example.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: example-service
            port:
              number: 80
```

### HTTPS Ingress with TLS

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: tls-ingress
  namespace: default
  annotations:
    kubernetes.io/ingress.class: nginx
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  tls:
  - hosts:
    - example.com
    secretName: example-tls
  rules:
  - host: example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: example-service
            port:
              number: 80
```

### Path-based Routing

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: path-based-ingress
  namespace: default
  annotations:
    kubernetes.io/ingress.class: nginx
    nginx.ingress.kubernetes.io/rewrite-target: /
spec:
  rules:
  - host: myapp.example.com
    http:
      paths:
      - path: /api
        pathType: Prefix
        backend:
          service:
            name: api-service
            port:
              number: 8080
      - path: /web
        pathType: Prefix
        backend:
          service:
            name: web-service
            port:
              number: 80
```

## 🔐 Security Configuration

### Rate Limiting

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: rate-limited-ingress
  annotations:
    kubernetes.io/ingress.class: nginx
    nginx.ingress.kubernetes.io/rate-limit: "100"
    nginx.ingress.kubernetes.io/rate-limit-window: "1m"
spec:
  # ... ingress rules
```

### IP Whitelisting

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: whitelist-ingress
  annotations:
    kubernetes.io/ingress.class: nginx
    nginx.ingress.kubernetes.io/whitelist-source-range: "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
spec:
  # ... ingress rules
```

### Basic Authentication

```bash
# Create htpasswd file
htpasswd -c auth username

# Create secret
kubectl create secret generic basic-auth --from-file=auth

# Use in ingress
```

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: auth-ingress
  annotations:
    kubernetes.io/ingress.class: nginx
    nginx.ingress.kubernetes.io/auth-type: basic
    nginx.ingress.kubernetes.io/auth-secret: basic-auth
    nginx.ingress.kubernetes.io/auth-realm: 'Authentication Required'
spec:
  # ... ingress rules
```

## 📊 Monitoring & Observability

### Enable Metrics

```bash
# Update to enable metrics
helm upgrade ingress-nginx ingress-nginx/ingress-nginx \
  --namespace adhar-system \
  -f hack/ingress-nginx/values.yaml \
  --set controller.metrics.enabled=true
```

### Prometheus Integration

```yaml
# ServiceMonitor for Prometheus
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: ingress-nginx
  namespace: adhar-system
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: ingress-nginx
      app.kubernetes.io/component: controller
  endpoints:
  - port: metrics
    interval: 30s
    path: /metrics
```

### Key Metrics to Monitor

- **nginx_ingress_controller_requests**: Total number of requests
- **nginx_ingress_controller_request_duration_seconds**: Request duration
- **nginx_ingress_controller_response_size**: Response size
- **nginx_ingress_controller_ssl_expire_time_seconds**: SSL certificate expiry
- **nginx_ingress_controller_nginx_process_cpu_seconds_total**: CPU usage

### Grafana Dashboard

Use the official NGINX Ingress Controller dashboard:
- Dashboard ID: `9614` (from Grafana.com)

## 🔧 Advanced Configuration

### Custom NGINX Configuration

```yaml
controller:
  config:
    # Custom NGINX settings
    proxy-connect-timeout: "10"
    proxy-send-timeout: "120"
    proxy-read-timeout: "120"
    client-body-buffer-size: "1M"
    client-body-timeout: "60"
    client-header-timeout: "60"
    large-client-header-buffers: "4 8k"
    proxy-body-size: "100M"
    ssl-protocols: "TLSv1.2 TLSv1.3"
    ssl-ciphers: "ECDHE+AESGCM:ECDHE+CHACHA20:DHE+AESGCM:DHE+CHACHA20:!aNULL:!MD5:!DSS"
```

### Custom Headers

```yaml
controller:
  config:
    # Add custom headers
    add-headers: "adhar-system/custom-headers"
```

```yaml
# ConfigMap for custom headers
apiVersion: v1
kind: ConfigMap
metadata:
  name: custom-headers
  namespace: adhar-system
data:
  X-Frame-Options: "SAMEORIGIN"
  X-Content-Type-Options: "nosniff"
  X-XSS-Protection: "1; mode=block"
  Strict-Transport-Security: "max-age=31536000; includeSubDomains"
```

### Admission Webhook

```yaml
controller:
  admissionWebhooks:
    enabled: true
    failurePolicy: Fail
    port: 8443
    certificate: "/usr/local/certificates/cert"
    key: "/usr/local/certificates/key"
```

## 🔄 Scaling and Performance

### Horizontal Pod Autoscaler

```bash
# Enable HPA
helm upgrade ingress-nginx ingress-nginx/ingress-nginx \
  --namespace adhar-system \
  -f hack/ingress-nginx/values.yaml \
  --set controller.autoscaling.enabled=true \
  --set controller.autoscaling.minReplicas=2 \
  --set controller.autoscaling.maxReplicas=10 \
  --set controller.autoscaling.targetCPUUtilizationPercentage=70
```

### Resource Limits

```yaml
controller:
  resources:
    limits:
      cpu: 1000m
      memory: 1Gi
    requests:
      cpu: 100m
      memory: 128Mi
```

### Node Affinity and Anti-Affinity

```yaml
controller:
  affinity:
    podAntiAffinity:
      preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          labelSelector:
            matchExpressions:
            - key: app.kubernetes.io/name
              operator: In
              values:
              - ingress-nginx
          topologyKey: kubernetes.io/hostname
  
  nodeSelector:
    node-role.kubernetes.io/worker: "true"
  
  tolerations:
  - key: node-role.kubernetes.io/master
    operator: Equal
    effect: NoSchedule
```

## 🐛 Troubleshooting

### Common Issues

#### 1. External IP Pending

```bash
# Check LoadBalancer service
kubectl describe svc ingress-nginx-controller -n adhar-system

# For cloud providers, check if LoadBalancer is supported
kubectl get nodes -o wide

# For on-premises, consider using MetalLB or NodePort
```

#### 2. Certificate Issues

```bash
# Check TLS secrets
kubectl get secrets -n default

# Describe ingress for certificate status
kubectl describe ingress your-ingress -n default

# Check cert-manager logs (if using cert-manager)
kubectl logs -n cert-manager deployment/cert-manager
```

#### 3. Backend Service Unavailable

```bash
# Check if backend service exists
kubectl get svc your-service -n your-namespace

# Check if pods are running
kubectl get pods -n your-namespace

# Check ingress configuration
kubectl describe ingress your-ingress -n your-namespace
```

#### 4. Configuration Issues

```bash
# Check controller logs
kubectl logs -n adhar-system deployment/ingress-nginx-controller

# Check admission webhook logs
kubectl logs -n adhar-system deployment/ingress-nginx-controller -c webhook

# Validate ingress configuration
kubectl get ingress -A -o yaml | kubectl apply --dry-run=client -f -
```

### Debugging Commands

```bash
# Get all ingress resources
kubectl get ingress -A

# Check controller status
kubectl get pods -n adhar-system -l app.kubernetes.io/name=ingress-nginx

# Check service endpoints
kubectl get endpoints -n adhar-system

# View controller configuration
kubectl exec -n adhar-system deployment/ingress-nginx-controller -- cat /etc/nginx/nginx.conf

# Test connectivity to backend
kubectl run debug --image=busybox --rm -it --restart=Never -- wget -qO- http://your-service.your-namespace.svc.cluster.local
```

### Log Analysis

```bash
# Controller logs
kubectl logs -n adhar-system deployment/ingress-nginx-controller -f

# Access logs (if enabled)
kubectl logs -n adhar-system deployment/ingress-nginx-controller | grep "access_log"

# Error logs
kubectl logs -n adhar-system deployment/ingress-nginx-controller | grep "error"
```

## 🔄 Upgrade and Maintenance

### Upgrading NGINX Ingress

```bash
# Update Helm repository
helm repo update

# Check current version
helm list -n adhar-system

# Upgrade to latest version
helm upgrade ingress-nginx ingress-nginx/ingress-nginx \
  --namespace adhar-system \
  -f hack/ingress-nginx/values.yaml

# Monitor the upgrade
kubectl rollout status deployment/ingress-nginx-controller -n adhar-system
```

### Configuration Reload

```bash
# Force configuration reload
kubectl patch deployment ingress-nginx-controller -n adhar-system \
  -p '{"spec":{"template":{"metadata":{"annotations":{"date":"'$(date +'%s')'"}}}}}'

# Or delete pods to force recreation
kubectl delete pods -n adhar-system -l app.kubernetes.io/name=ingress-nginx
```

## 📚 Best Practices

### 🏗️ **Production Deployment**

1. **High Availability**: Deploy multiple replicas across different nodes
2. **Resource Limits**: Set appropriate CPU and memory limits
3. **Monitoring**: Enable metrics and set up alerting
4. **Security**: Use TLS everywhere and implement security headers
5. **Backup**: Backup ingress configurations and certificates

### 🔒 **Security Best Practices**

1. **TLS Configuration**: Use modern TLS versions and ciphers
2. **Rate Limiting**: Implement rate limiting to prevent abuse
3. **IP Whitelisting**: Restrict access where appropriate
4. **Regular Updates**: Keep NGINX Ingress Controller updated
5. **Certificate Management**: Use automated certificate management

### ⚡ **Performance Optimization**

1. **Connection Pooling**: Configure upstream connection pooling
2. **Caching**: Implement response caching where appropriate
3. **Compression**: Enable gzip compression
4. **Keep-Alive**: Optimize keep-alive settings
5. **Buffer Sizes**: Tune buffer sizes for your workload

## 📚 Additional Resources

- [NGINX Ingress Controller Documentation](https://kubernetes.github.io/ingress-nginx/)
- [NGINX Configuration Options](https://kubernetes.github.io/ingress-nginx/user-guide/nginx-configuration/)
- [Troubleshooting Guide](https://kubernetes.github.io/ingress-nginx/troubleshooting/)
- [Annotations Reference](https://kubernetes.github.io/ingress-nginx/user-guide/nginx-configuration/annotations/)
- [Cert-Manager Integration](https://cert-manager.io/docs/usage/ingress/)

## 🤝 Contributing

When modifying the values files:

1. **Test Thoroughly**: Test changes in both local and production-like environments
2. **Version Compatibility**: Ensure compatibility with the target Kubernetes version
3. **Documentation**: Update this README if adding new configuration options
4. **Security Review**: Review security implications of configuration changes
5. **Performance Impact**: Consider performance implications of changes

## ⚠️ Important Notes

- **LoadBalancer Support**: Ensure your cluster supports LoadBalancer services or use alternatives
- **Certificate Management**: Plan for SSL/TLS certificate lifecycle management
- **Resource Planning**: Monitor resource usage and scale appropriately
- **Network Policies**: Consider network policies for additional security
- **Backup Strategy**: Backup ingress configurations and certificates regularly
- **Version Compatibility**: Check compatibility between ingress controller and Kubernetes versions

## 🔄 Quick Reference

### Common Annotations

```yaml
# Basic annotations
kubernetes.io/ingress.class: nginx

# SSL/TLS
nginx.ingress.kubernetes.io/ssl-redirect: "true"
cert-manager.io/cluster-issuer: letsencrypt-prod

# Routing
nginx.ingress.kubernetes.io/rewrite-target: /
nginx.ingress.kubernetes.io/use-regex: "true"

# Security
nginx.ingress.kubernetes.io/rate-limit: "100"
nginx.ingress.kubernetes.io/whitelist-source-range: "10.0.0.0/8"

# Backend configuration
nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
nginx.ingress.kubernetes.io/proxy-body-size: "100m"
```

### Service Types Summary

| **Type** | **Use Case** | **External Access** | **IP Assignment** |
|----------|--------------|-------------------|-------------------|
| **LoadBalancer** | Production (cloud) | Yes | Cloud provider assigns |
| **NodePort** | Development/on-prem | Yes | Uses node IPs + port |
| **ClusterIP** | Internal only | No | Internal cluster IP |

This comprehensive configuration provides a robust foundation for running NGINX Ingress Controller in any Kubernetes environment! 🚀
