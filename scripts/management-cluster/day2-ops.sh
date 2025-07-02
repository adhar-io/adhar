#!/bin/bash
# Adhar Management Cluster - Day-2 Operations
# Comprehensive health monitoring, backup, and maintenance automation

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${SCRIPT_DIR}/cluster-config.yaml"
LOG_DIR="/var/log/adhar"
BACKUP_DIR="/opt/adhar-backups"

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Logging functions
log() {
    echo -e "${BLUE}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $1" | tee -a "${LOG_DIR}/operations.log"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" | tee -a "${LOG_DIR}/operations.log"
}

success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1" | tee -a "${LOG_DIR}/operations.log"
}

warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1" | tee -a "${LOG_DIR}/operations.log"
}

# Initialize logging directory
init_logging() {
    sudo mkdir -p "$LOG_DIR"
    sudo chmod 755 "$LOG_DIR"
}

# Comprehensive health check
health_check() {
    local verbose=false
    local json_output=false
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --verbose)
                verbose=true
                shift
                ;;
            --json)
                json_output=true
                shift
                ;;
            *)
                shift
                ;;
        esac
    done
    
    log "Starting comprehensive health check"
    local overall_health=0
    local health_data=()
    
    if [[ "$json_output" == "false" ]]; then
        echo "Adhar Management Cluster Health Report"
        echo "======================================"
        echo "Timestamp: $(date)"
        echo ""
    fi
    
    # Check Kubernetes API
    local api_status="OK"
    local api_details=""
    if ! kubectl cluster-info >/dev/null 2>&1; then
        api_status="FAILED"
        api_details="Unable to connect to Kubernetes API"
        overall_health=1
    fi
    
    if [[ "$json_output" == "false" ]]; then
        echo -n "Kubernetes API Server... "
        if [[ "$api_status" == "OK" ]]; then
            echo -e "${GREEN}OK${NC}"
        else
            echo -e "${RED}FAILED${NC}"
            [[ "$verbose" == "true" ]] && echo "  Details: $api_details"
        fi
    fi
    health_data+=("\"api\":{\"status\":\"$api_status\",\"details\":\"$api_details\"}")
    
    # Check node status
    local node_count=$(kubectl get nodes --no-headers 2>/dev/null | wc -l)
    local ready_nodes=$(kubectl get nodes --no-headers 2>/dev/null | grep -c " Ready " || echo 0)
    local node_status="OK"
    local node_details="$ready_nodes/$node_count nodes ready"
    
    if [[ $node_count -ne $ready_nodes ]] || [[ $node_count -eq 0 ]]; then
        node_status="FAILED"
        overall_health=1
    fi
    
    if [[ "$json_output" == "false" ]]; then
        echo -n "Node Status... "
        if [[ "$node_status" == "OK" ]]; then
            echo -e "${GREEN}OK${NC} ($node_details)"
        else
            echo -e "${RED}FAILED${NC} ($node_details)"
        fi
        
        if [[ "$verbose" == "true" ]]; then
            echo "  Node Details:"
            kubectl get nodes -o wide 2>/dev/null | sed 's/^/    /'
        fi
    fi
    health_data+=("\"nodes\":{\"status\":\"$node_status\",\"details\":\"$node_details\",\"ready\":$ready_nodes,\"total\":$node_count}")
    
    # Check Cilium status
    local cilium_status="OK"
    local cilium_details=""
    if ! cilium status --quiet >/dev/null 2>&1; then
        cilium_status="FAILED"
        cilium_details="Cilium health check failed"
        overall_health=1
    fi
    
    if [[ "$json_output" == "false" ]]; then
        echo -n "Cilium CNI... "
        if [[ "$cilium_status" == "OK" ]]; then
            echo -e "${GREEN}OK${NC}"
        else
            echo -e "${RED}FAILED${NC}"
            [[ "$verbose" == "true" ]] && echo "  Details: $cilium_details"
        fi
        
        if [[ "$verbose" == "true" ]]; then
            echo "  Cilium Status:"
            cilium status 2>/dev/null | sed 's/^/    /' || echo "    Unable to get Cilium status"
        fi
    fi
    health_data+=("\"cilium\":{\"status\":\"$cilium_status\",\"details\":\"$cilium_details\"}")
    
    # Check CoreDNS
    local coredns_ready=$(kubectl get pods -n kube-system -l k8s-app=kube-dns --no-headers 2>/dev/null | grep -c "Running" || echo 0)
    local coredns_status="OK"
    local coredns_details="$coredns_ready pods running"
    
    if [[ $coredns_ready -eq 0 ]]; then
        coredns_status="FAILED"
        coredns_details="No CoreDNS pods running"
        overall_health=1
    fi
    
    if [[ "$json_output" == "false" ]]; then
        echo -n "CoreDNS... "
        if [[ "$coredns_status" == "OK" ]]; then
            echo -e "${GREEN}OK${NC} ($coredns_details)"
        else
            echo -e "${RED}FAILED${NC} ($coredns_details)"
        fi
    fi
    health_data+=("\"coredns\":{\"status\":\"$coredns_status\",\"details\":\"$coredns_details\",\"ready_pods\":$coredns_ready}")
    
    # Check etcd
    local etcd_ready=$(kubectl get pods -n kube-system -l component=etcd --no-headers 2>/dev/null | grep -c "Running" || echo 0)
    local etcd_status="OK"
    local etcd_details="$etcd_ready pods running"
    
    if [[ $etcd_ready -eq 0 ]]; then
        etcd_status="FAILED"
        etcd_details="No etcd pods running"
        overall_health=1
    fi
    
    if [[ "$json_output" == "false" ]]; then
        echo -n "etcd... "
        if [[ "$etcd_status" == "OK" ]]; then
            echo -e "${GREEN}OK${NC} ($etcd_details)"
        else
            echo -e "${RED}FAILED${NC} ($etcd_details)"
        fi
    fi
    health_data+=("\"etcd\":{\"status\":\"$etcd_status\",\"details\":\"$etcd_details\",\"ready_pods\":$etcd_ready}")
    
    # Check HAProxy
    echo -n "HAProxy Load Balancer... "
    if systemctl is-active --quiet haproxy; then
        echo -e "${GREEN}OK${NC}"
    else
        echo -e "${RED}FAILED${NC}"
        overall_health=1
    fi
    
    # Check Crossplane
    echo -n "Crossplane... "
    local crossplane_ready=$(kubectl get pods -n crossplane-system --no-headers 2>/dev/null | grep -c "Running" || echo 0)
    if [[ $crossplane_ready -gt 0 ]]; then
        echo -e "${GREEN}OK${NC} ($crossplane_ready pods running)"
    else
        echo -e "${YELLOW}WARNING${NC} (Crossplane not found or not ready)"
    fi
    
    # Check monitoring stack
    echo -n "Monitoring Stack... "
    local prometheus_ready=$(kubectl get pods -n monitoring -l app.kubernetes.io/name=prometheus --no-headers 2>/dev/null | grep -c "Running" || echo 0)
    if [[ $prometheus_ready -gt 0 ]]; then
        echo -e "${GREEN}OK${NC}"
    else
        echo -e "${YELLOW}WARNING${NC} (Monitoring not found or not ready)"
    fi
    
    # Check disk space
    echo -n "Disk Space... "
    local disk_usage=$(df / | awk 'NR==2{print int($3/$2*100)}')
    if [[ $disk_usage -lt 80 ]]; then
        echo -e "${GREEN}OK${NC} (${disk_usage}% used)"
    elif [[ $disk_usage -lt 90 ]]; then
        echo -e "${YELLOW}WARNING${NC} (${disk_usage}% used)"
    else
        echo -e "${RED}CRITICAL${NC} (${disk_usage}% used)"
        overall_health=1
    fi
    
    # Check memory usage
    echo -n "Memory Usage... "
    local mem_usage=$(free | awk 'NR==2{printf "%.0f", $3/$2*100}')
    if [[ $mem_usage -lt 80 ]]; then
        echo -e "${GREEN}OK${NC} (${mem_usage}% used)"
    elif [[ $mem_usage -lt 90 ]]; then
        echo -e "${YELLOW}WARNING${NC} (${mem_usage}% used)"
    else
        echo -e "${RED}CRITICAL${NC} (${mem_usage}% used)"
        overall_health=1
    fi
    
    # Check certificate expiration
    echo -n "Certificate Expiration... "
    local cert_days=$(openssl x509 -in /etc/kubernetes/pki/ca.crt -noout -enddate | cut -d= -f2 | xargs -I {} date -d "{}" +%s)
    local current_days=$(date +%s)
    local days_left=$(( (cert_days - current_days) / 86400 ))
    
    if [[ $days_left -gt 90 ]]; then
        echo -e "${GREEN}OK${NC} ($days_left days remaining)"
    elif [[ $days_left -gt 30 ]]; then
        echo -e "${YELLOW}WARNING${NC} ($days_left days remaining)"
    else
        echo -e "${RED}CRITICAL${NC} ($days_left days remaining)"
        overall_health=1
    fi
    
    echo ""
    if [[ $overall_health -eq 0 ]]; then
        echo -e "Overall Health: ${GREEN}HEALTHY${NC}"
        success "Health check completed - all systems operational"
    else
        echo -e "Overall Health: ${RED}DEGRADED${NC}"
        error "Health check completed - issues detected"
    fi
    
    return $overall_health
}

# Backup etcd and cluster state
backup_cluster() {
    local backup_output="$BACKUP_DIR"
    local etcd_only=false
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --output)
                backup_output="$2"
                shift 2
                ;;
            --etcd-only)
                etcd_only=true
                shift
                ;;
            *)
                shift
                ;;
        esac
    done
    
    log "Starting cluster backup"
    
    local backup_timestamp
    backup_timestamp=$(date +%Y%m%d-%H%M%S)
    local backup_path="${backup_output}/${backup_timestamp}"
    
    sudo mkdir -p "$backup_path"
    
    # Backup etcd
    log "Backing up etcd"
    if ! sudo ETCDCTL_API=3 etcdctl snapshot save "${backup_path}/etcd-snapshot.db" \
        --endpoints=https://127.0.0.1:2379 \
        --cacert=/etc/kubernetes/pki/etcd/ca.crt \
        --cert=/etc/kubernetes/pki/etcd/server.crt \
        --key=/etc/kubernetes/pki/etcd/server.key; then
        error "Failed to create etcd backup"
        return 1
    fi
    
    # Verify etcd backup
    if ! sudo ETCDCTL_API=3 etcdctl snapshot status "${backup_path}/etcd-snapshot.db"; then
        error "etcd backup verification failed"
        return 1
    fi
    
    if [[ "$etcd_only" == "true" ]]; then
        success "etcd backup completed successfully at $backup_path"
        return 0
    fi
    
    # Backup Kubernetes certificates
    log "Backing up Kubernetes certificates"
    sudo cp -r /etc/kubernetes/pki "${backup_path}/"
    
    # Backup kubeconfig
    if [[ -f ~/.kube/config ]]; then
        cp ~/.kube/config "${backup_path}/admin.conf"
    fi
    
    # Backup cluster state
    log "Backing up cluster state"
    kubectl get all --all-namespaces -o yaml > "${backup_path}/cluster-state.yaml" 2>/dev/null || warning "Failed to backup cluster state"
    kubectl get configmaps --all-namespaces -o yaml > "${backup_path}/configmaps.yaml" 2>/dev/null || warning "Failed to backup configmaps"
    kubectl get secrets --all-namespaces -o yaml > "${backup_path}/secrets.yaml" 2>/dev/null || warning "Failed to backup secrets"
    kubectl get persistentvolumes -o yaml > "${backup_path}/persistent-volumes.yaml" 2>/dev/null || warning "Failed to backup persistent volumes"
    
    # Backup Crossplane resources
    if kubectl get crd compositeresourcedefinitions.apiextensions.crossplane.io >/dev/null 2>&1; then
        log "Backing up Crossplane resources"
        kubectl get xrd -o yaml > "${backup_path}/crossplane-xrds.yaml" 2>/dev/null || warning "Failed to backup Crossplane XRDs"
        kubectl get compositions -o yaml > "${backup_path}/crossplane-compositions.yaml" 2>/dev/null || warning "Failed to backup Crossplane compositions"
        kubectl get providers -o yaml > "${backup_path}/crossplane-providers.yaml" 2>/dev/null || warning "Failed to backup Crossplane providers"
    fi
    
    # Compress backup
    log "Compressing backup"
    sudo tar -czf "${backup_path}.tar.gz" -C "$backup_output" "$backup_timestamp"
    sudo rm -rf "$backup_path"
    
    # Cleanup old backups (keep last 30 days)
    find "$backup_output" -name "*.tar.gz" -mtime +30 -delete 2>/dev/null || true
    
    success "Backup completed: ${backup_path}.tar.gz"
}

# Monitor resource usage
monitor_resources() {
    log "Monitoring resource usage"
    
    echo "Resource Usage Report"
    echo "===================="
    echo "Timestamp: $(date)"
    echo ""
    
    # System resources
    echo "System Resources:"
    echo "=================="
    echo "CPU Usage:"
    top -bn1 | grep "Cpu(s)" | awk '{print $2}' | cut -d'%' -f1
    
    echo "Memory Usage:"
    free -h
    
    echo "Disk Usage:"
    df -h
    
    echo ""
    
    # Kubernetes resources
    echo "Kubernetes Resources:"
    echo "===================="
    kubectl top nodes 2>/dev/null || echo "Metrics server not available"
    echo ""
    kubectl top pods --all-namespaces --sort-by=cpu 2>/dev/null | head -10 || echo "Pod metrics not available"
}

# Cleanup old logs and temporary files
cleanup_system() {
    log "Starting system cleanup"
    
    # Clean up old logs
    sudo journalctl --vacuum-time=7d
    
    # Clean up old container images
    sudo crictl rmi --prune >/dev/null 2>&1 || true
    
    # Clean up old audit logs
    find /var/log/kubernetes -name "*.log" -mtime +7 -delete 2>/dev/null || true
    
    # Clean up temporary files
    sudo find /tmp -type f -mtime +3 -delete 2>/dev/null || true
    
    success "System cleanup completed"
}

# Update cluster components
update_cluster() {
    log "Checking for cluster updates"
    
    # Check if updates are available
    sudo dnf check-update kubelet kubeadm kubectl --disableexcludes=kubernetes >/dev/null 2>&1
    local update_available=$?
    
    if [[ $update_available -eq 100 ]]; then
        warning "Updates available for Kubernetes components"
        echo "To update, run: sudo ./upgrade.sh <version>"
    else
        success "Kubernetes components are up to date"
    fi
    
    # Check Cilium updates
    local current_cilium=$(cilium version --client | grep "cilium-cli" | awk '{print $2}')
    log "Current Cilium CLI version: $current_cilium"
    
    # Check Helm chart updates
    helm repo update >/dev/null 2>&1
    local outdated_releases=$(helm list --all-namespaces -o json | jq -r '.[] | select(.status == "deployed") | .name')
    
    if [[ -n "$outdated_releases" ]]; then
        log "Checking Helm releases for updates:"
        for release in $outdated_releases; do
            echo "  - $release"
        done
    fi
}

# Security audit
security_audit() {
    log "Running security audit"
    
    echo "Security Audit Report"
    echo "===================="
    echo "Timestamp: $(date)"
    echo ""
    
    # Check for pods running as root
    echo "Pods running as root:"
    kubectl get pods --all-namespaces -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.spec.securityContext.runAsUser}{"\n"}{end}' 2>/dev/null | grep -E " 0$| $" || echo "None found"
    
    echo ""
    
    # Check for pods with privileged containers
    echo "Privileged containers:"
    kubectl get pods --all-namespaces -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.spec.containers[*].securityContext.privileged}{"\n"}{end}' 2>/dev/null | grep true || echo "None found"
    
    echo ""
    
    # Check network policies
    echo "Network policies:"
    local netpol_count=$(kubectl get networkpolicies --all-namespaces --no-headers 2>/dev/null | wc -l)
    echo "Total network policies: $netpol_count"
    
    # Check RBAC
    echo "RBAC analysis:"
    local cluster_admin_count=$(kubectl get clusterrolebindings -o json 2>/dev/null | jq -r '.items[] | select(.roleRef.name == "cluster-admin") | .subjects[]?.name' | wc -l)
    echo "Cluster admin bindings: $cluster_admin_count"
}

# Performance optimization
optimize_performance() {
    log "Running performance optimization"
    
    # Optimize sysctl parameters
    sudo sysctl -w vm.max_map_count=262144 >/dev/null 2>&1 || true
    sudo sysctl -w fs.file-max=1048576 >/dev/null 2>&1 || true
    
    # Optimize container runtime
    sudo systemctl restart containerd
    
    # Clean up unused images
    sudo crictl rmi --prune >/dev/null 2>&1 || true
    
    success "Performance optimization completed"
}

# Main operations workflow
main() {
    local operation=${1:-"health"}
    shift  # Remove first argument, pass rest to operation functions
    
    init_logging
    
    case "$operation" in
        "health")
            health_check "$@"
            ;;
        "backup")
            backup_cluster "$@"
            ;;
        "monitor")
            monitor_resources "$@"
            ;;
        "cleanup")
            cleanup_system "$@"
            ;;
        "update")
            update_cluster "$@"
            ;;
        "security")
            security_audit "$@"
            ;;
        "optimize")
            optimize_performance "$@"
            ;;
        "full")
            log "Running full day-2 operations cycle"
            health_check "$@"
            backup_cluster "$@"
            monitor_resources "$@"
            cleanup_system "$@"
            update_cluster "$@"
            security_audit "$@"
            optimize_performance "$@"
            success "Full operations cycle completed"
            ;;
        *)
            echo "Usage: $0 {health|backup|monitor|cleanup|update|security|optimize|full} [OPTIONS]"
            echo ""
            echo "Operations:"
            echo "  health   - Comprehensive health check"
            echo "             Options: --verbose, --json"
            echo "  backup   - Backup etcd and cluster state"
            echo "             Options: --output DIR, --etcd-only"
            echo "  monitor  - Monitor resource usage"
            echo "  cleanup  - Clean up logs and temporary files"
            echo "             Options: --dry-run, --force"
            echo "  update   - Check for available updates"
            echo "  security - Run security audit"
            echo "  optimize - Optimize system performance"
            echo "  full     - Run all operations"
            exit 1
            ;;
    esac
}

# Execute main function
main "$@"
