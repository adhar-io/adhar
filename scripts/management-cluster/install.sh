#!/bin/bash

# Adhar Management Cluster Installation Wrapper
# Provides a unified interface for management cluster operations

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Logging functions
log() {
    echo -e "${BLUE}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $1"
}

success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

# Display help
show_help() {
    cat << EOF
Adhar Management Cluster Installation Tool

USAGE:
    $(basename "$0") [COMMAND] [OPTIONS]

COMMANDS:
    test        Run validation tests before installation
    install     Install and bootstrap management cluster
    status      Check cluster health and status
    backup      Create cluster backup
    cleanup     Clean up cluster resources
    help        Show this help message

INSTALL OPTIONS:
    --config FILE    Cluster configuration file (default: cluster-config.yaml)
    --force          Force installation even if cluster exists
    --dry-run        Show what would be done without making changes

STATUS OPTIONS:
    --verbose        Show detailed status information
    --json          Output status in JSON format

BACKUP OPTIONS:
    --output DIR     Backup output directory
    --etcd-only     Backup only etcd data

CLEANUP OPTIONS:
    --force         Force cleanup without confirmation
    --dry-run       Show what would be cleaned without making changes

EXAMPLES:
    $(basename "$0") test                    # Validate setup before installation
    $(basename "$0") install                 # Bootstrap with default config
    $(basename "$0") install --config my-config.yaml
    $(basename "$0") status --verbose        # Detailed health check
    $(basename "$0") backup --output /backups
    $(basename "$0") cleanup --dry-run       # Preview cleanup actions

EOF
}

# Run validation tests
run_tests() {
    log "Running management cluster validation tests"
    
    if [[ -x "$SCRIPT_DIR/test-setup.sh" ]]; then
        "$SCRIPT_DIR/test-setup.sh"
    else
        error "Test script not found or not executable: $SCRIPT_DIR/test-setup.sh"
        return 1
    fi
}

# Install and configure management cluster
run_install() {
    local config_file="$SCRIPT_DIR/cluster-config.yaml"
    local force=false
    local dry_run=false
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --config)
                config_file="$2"
                shift 2
                ;;
            --force)
                force=true
                shift
                ;;
            --dry-run)
                dry_run=true
                shift
                ;;
            *)
                shift
                ;;
        esac
    done
    
    log "Installing management cluster"
    log "Configuration: $config_file"
    log "Force: $force"
    log "Dry run: $dry_run"
    
    # Validate configuration file
    if [[ ! -f "$config_file" ]]; then
        error "Configuration file not found: $config_file"
        return 1
    fi
    
    # Run validation tests first
    if ! run_tests; then
        error "Validation tests failed. Please resolve issues before installation."
        return 1
    fi
    
    # Check if cluster already exists
    if kubectl cluster-info >/dev/null 2>&1 && [[ "$force" != "true" ]]; then
        warning "Kubernetes cluster already detected. Use --force to override."
        log "Current cluster info:"
        kubectl cluster-info
        return 1
    fi
    
    # Run bootstrap script
    if [[ "$dry_run" == "true" ]]; then
        log "DRY RUN: Would execute bootstrap script with config: $config_file"
        return 0
    fi
    
    log "Executing management cluster bootstrap"
    if [[ -x "$SCRIPT_DIR/bootstrap.sh" ]]; then
        sudo "$SCRIPT_DIR/bootstrap.sh"
    else
        error "Bootstrap script not found or not executable: $SCRIPT_DIR/bootstrap.sh"
        return 1
    fi
    
    # Verify installation
    log "Verifying cluster installation"
    sleep 30  # Wait for cluster to stabilize
    
    if kubectl cluster-info >/dev/null 2>&1; then
        success "Management cluster installation completed successfully!"
        log ""
        log "Next steps:"
        log "1. Run: $(basename "$0") status --verbose"
        log "2. Set up workstation: export KUBECONFIG=/etc/kubernetes/admin.conf"
        log "3. Access Hubble UI: kubectl port-forward -n kube-system svc/hubble-ui 12000:80"
    else
        error "Cluster installation verification failed"
        return 1
    fi
}

# Check cluster status
run_status() {
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
    
    log "Checking management cluster status"
    
    if [[ -x "$SCRIPT_DIR/day2-ops.sh" ]]; then
        local args=("health")
        [[ "$verbose" == "true" ]] && args+=("--verbose")
        [[ "$json_output" == "true" ]] && args+=("--json")
        
        "$SCRIPT_DIR/day2-ops.sh" "${args[@]}"
    else
        error "Day-2 operations script not found: $SCRIPT_DIR/day2-ops.sh"
        return 1
    fi
}

# Create cluster backup
run_backup() {
    local output_dir="/var/lib/adhar/backups"
    local etcd_only=false
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --output)
                output_dir="$2"
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
    
    log "Creating management cluster backup"
    log "Output directory: $output_dir"
    log "etcd only: $etcd_only"
    
    if [[ -x "$SCRIPT_DIR/day2-ops.sh" ]]; then
        local args=("backup" "--output" "$output_dir")
        [[ "$etcd_only" == "true" ]] && args+=("--etcd-only")
        
        "$SCRIPT_DIR/day2-ops.sh" "${args[@]}"
    else
        error "Day-2 operations script not found: $SCRIPT_DIR/day2-ops.sh"
        return 1
    fi
}

# Clean up cluster resources
run_cleanup() {
    local force=false
    local dry_run=false
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --force)
                force=true
                shift
                ;;
            --dry-run)
                dry_run=true
                shift
                ;;
            *)
                shift
                ;;
        esac
    done
    
    log "Cleaning up management cluster resources"
    log "Force: $force"
    log "Dry run: $dry_run"
    
    if [[ -x "$SCRIPT_DIR/day2-ops.sh" ]]; then
        local args=("cleanup")
        [[ "$force" == "true" ]] && args+=("--force")
        [[ "$dry_run" == "true" ]] && args+=("--dry-run")
        
        "$SCRIPT_DIR/day2-ops.sh" "${args[@]}"
    else
        error "Day-2 operations script not found: $SCRIPT_DIR/day2-ops.sh"
        return 1
    fi
}

# Main execution
main() {
    local command="${1:-help}"
    shift || true
    
    case "$command" in
        test)
            run_tests "$@"
            ;;
        install)
            run_install "$@"
            ;;
        status)
            run_status "$@"
            ;;
        backup)
            run_backup "$@"
            ;;
        cleanup)
            run_cleanup "$@"
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            error "Unknown command: $command"
            echo ""
            show_help
            exit 1
            ;;
    esac
}

# Execute main function with all arguments
main "$@"
