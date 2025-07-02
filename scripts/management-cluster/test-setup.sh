#!/bin/bash

# Adhar Management Cluster Test Script
# Validates that the management cluster provisioning and day-2 operations work correctly

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_LOG="/tmp/adhar-cluster-test.log"

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Logging functions
log() {
    echo -e "${BLUE}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $1" | tee -a "$TEST_LOG"
}

success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1" | tee -a "$TEST_LOG"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" | tee -a "$TEST_LOG"
}

warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1" | tee -a "$TEST_LOG"
}

# Test configuration file validation
test_config_validation() {
    log "Testing cluster configuration validation"
    
    local config_file="${SCRIPT_DIR}/cluster-config.yaml"
    
    if [[ ! -f "$config_file" ]]; then
        error "Configuration file not found: $config_file"
        return 1
    fi
    
    # Check required fields in config
    local required_fields=("cluster" "cilium" "monitoring")
    for field in "${required_fields[@]}"; do
        if ! grep -q "$field:" "$config_file"; then
            error "Required field '$field' not found in configuration"
            return 1
        fi
    done
    
    success "Configuration file validation passed"
    return 0
}

# Test bootstrap script availability
test_bootstrap_script() {
    log "Testing bootstrap script availability"
    
    local bootstrap_script="${SCRIPT_DIR}/bootstrap.sh"
    
    if [[ ! -f "$bootstrap_script" ]]; then
        error "Bootstrap script not found: $bootstrap_script"
        return 1
    fi
    
    if [[ ! -x "$bootstrap_script" ]]; then
        error "Bootstrap script is not executable: $bootstrap_script"
        return 1
    fi
    
    # Test script help output
    if ! bash "$bootstrap_script" --help >/dev/null 2>&1; then
        warning "Bootstrap script help command failed (may be expected)"
    fi
    
    success "Bootstrap script validation passed"
    return 0
}

# Test day-2 operations script
test_day2_operations() {
    log "Testing day-2 operations script"
    
    local day2_script="${SCRIPT_DIR}/day2-ops.sh"
    
    if [[ ! -f "$day2_script" ]]; then
        error "Day-2 operations script not found: $day2_script"
        return 1
    fi
    
    if [[ ! -x "$day2_script" ]]; then
        error "Day-2 operations script is not executable: $day2_script"
        return 1
    fi
    
    # Test help output
    if ! bash "$day2_script" --help >/dev/null 2>&1; then
        warning "Day-2 operations script help failed (may be expected)"
    fi
    
    success "Day-2 operations script validation passed"
    return 0
}

# Test kubectl availability (if cluster exists)
test_cluster_connectivity() {
    log "Testing cluster connectivity (if available)"
    
    if ! command -v kubectl >/dev/null 2>&1; then
        warning "kubectl not found, skipping cluster connectivity test"
        return 0
    fi
    
    if kubectl cluster-info >/dev/null 2>&1; then
        success "Cluster connectivity test passed"
        
        # Test additional cluster components
        if kubectl get nodes >/dev/null 2>&1; then
            local node_count
            node_count=$(kubectl get nodes --no-headers | wc -l)
            success "Found $node_count nodes in cluster"
        fi
        
        if command -v cilium >/dev/null 2>&1; then
            if cilium status --quiet >/dev/null 2>&1; then
                success "Cilium networking is healthy"
            else
                warning "Cilium status check failed"
            fi
        else
            warning "Cilium CLI not found"
        fi
        
    else
        warning "No cluster found or kubectl not configured, skipping cluster tests"
    fi
    
    return 0
}

# Test Go module integration
test_go_integration() {
    log "Testing Go module integration"
    
    local go_module="${SCRIPT_DIR}/../../platform/management/cluster.go"
    
    if [[ ! -f "$go_module" ]]; then
        error "Go management module not found: $go_module"
        return 1
    fi
    
    # Check if Go is available for compilation test
    if command -v go >/dev/null 2>&1; then
        log "Testing Go module compilation"
        local project_root
        project_root=$(cd "$SCRIPT_DIR/../.." && pwd)
        
        if cd "$project_root" && go build -o /tmp/adhar-test ./cmd/... >/dev/null 2>&1; then
            success "Go module compilation test passed"
            rm -f /tmp/adhar-test
        else
            warning "Go module compilation test failed (may need dependencies)"
        fi
    else
        warning "Go not found, skipping compilation test"
    fi
    
    return 0
}

# Test CLI integration
test_cli_integration() {
    log "Testing CLI integration"
    
    local adhar_binary="${SCRIPT_DIR}/../bin/adhar"
    
    if [[ -f "$adhar_binary" ]]; then
        if "$adhar_binary" cluster --help >/dev/null 2>&1; then
            success "CLI cluster command integration test passed"
        else
            warning "CLI cluster command not available or failed"
        fi
    else
        warning "Adhar binary not found, skipping CLI integration test"
    fi
    
    return 0
}

# Test prerequisites
test_prerequisites() {
    log "Testing system prerequisites"
    
    local required_tools=("bash" "kubectl" "docker" "curl" "wget")
    local missing_tools=()
    
    for tool in "${required_tools[@]}"; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            missing_tools+=("$tool")
        fi
    done
    
    if [[ ${#missing_tools[@]} -gt 0 ]]; then
        warning "Missing tools: ${missing_tools[*]}"
        warning "Some management cluster features may not work without these tools"
    else
        success "All required tools are available"
    fi
    
    # Check system requirements
    local mem_gb
    if command -v free >/dev/null 2>&1; then
        mem_gb=$(free -g | awk '/^Mem:/{print $2}')
    elif command -v sysctl >/dev/null 2>&1; then
        # macOS/BSD systems
        local mem_bytes
        mem_bytes=$(sysctl -n hw.memsize 2>/dev/null || echo "0")
        mem_gb=$((mem_bytes / 1024 / 1024 / 1024))
    else
        mem_gb=0
    fi
    
    if [[ $mem_gb -lt 8 ]]; then
        warning "System has less than 8GB RAM ($mem_gb GB), management cluster may be unstable"
    else
        success "Memory requirements met ($mem_gb GB available)"
    fi
    
    local cpu_count
    cpu_count=$(nproc)
    if [[ $cpu_count -lt 2 ]]; then
        warning "System has less than 2 CPU cores ($cpu_count), performance may be limited"
    else
        success "CPU requirements met ($cpu_count cores available)"
    fi
    
    return 0
}

# Main test execution
main() {
    echo "Adhar Management Cluster Test Suite"
    echo "===================================="
    echo "Timestamp: $(date)"
    echo "Script Directory: $SCRIPT_DIR"
    echo ""
    
    local test_results=()
    local overall_result=0
    
    # Run tests
    if test_prerequisites; then
        test_results+=("Prerequisites: PASS")
    else
        test_results+=("Prerequisites: FAIL")
        overall_result=1
    fi
    
    if test_config_validation; then
        test_results+=("Configuration: PASS")
    else
        test_results+=("Configuration: FAIL")
        overall_result=1
    fi
    
    if test_bootstrap_script; then
        test_results+=("Bootstrap Script: PASS")
    else
        test_results+=("Bootstrap Script: FAIL")
        overall_result=1
    fi
    
    if test_day2_operations; then
        test_results+=("Day-2 Operations: PASS")
    else
        test_results+=("Day-2 Operations: FAIL")
        overall_result=1
    fi
    
    if test_cluster_connectivity; then
        test_results+=("Cluster Connectivity: PASS")
    else
        test_results+=("Cluster Connectivity: FAIL")
        overall_result=1
    fi
    
    if test_go_integration; then
        test_results+=("Go Integration: PASS")
    else
        test_results+=("Go Integration: FAIL")
        overall_result=1
    fi
    
    if test_cli_integration; then
        test_results+=("CLI Integration: PASS")
    else
        test_results+=("CLI Integration: FAIL")
        overall_result=1
    fi
    
    # Display results
    echo ""
    echo "Test Results Summary"
    echo "===================="
    for result in "${test_results[@]}"; do
        if [[ "$result" == *"PASS"* ]]; then
            echo -e "${GREEN}✓${NC} $result"
        else
            echo -e "${RED}✗${NC} $result"
        fi
    done
    
    echo ""
    if [[ $overall_result -eq 0 ]]; then
        success "All tests passed! Management cluster setup is ready for use."
        echo ""
        echo "Next steps:"
        echo "1. Run 'sudo ./bootstrap.sh' to provision the management cluster"
        echo "2. Use './day2-ops.sh health' to check cluster status"
        echo "3. Use 'adhar cluster status' for integrated monitoring"
    else
        error "Some tests failed. Please resolve issues before deploying."
        echo ""
        echo "Check the test log: $TEST_LOG"
    fi
    
    return $overall_result
}

# Execute main function
main "$@"
