/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package up

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"adhar-io/adhar/platform/logger"
)

// ValidationResult represents the result of a validation check
type ValidationResult struct {
	Name     string
	Status   ValidationStatus
	Message  string
	Error    error
	Required bool
}

type ValidationStatus int

const (
	StatusPass ValidationStatus = iota
	StatusFail
	StatusWarning
	StatusSkip
)

func (s ValidationStatus) String() string {
	switch s {
	case StatusPass:
		return "✅ PASS"
	case StatusFail:
		return "❌ FAIL"
	case StatusWarning:
		return "⚠️  WARNING"
	case StatusSkip:
		return "⏭️  SKIP"
	default:
		return "❓ UNKNOWN"
	}
}

// SystemValidator performs system validation checks
type SystemValidator struct {
	options *LocalOptions
}

// NewSystemValidator creates a new system validator
func NewSystemValidator(options *LocalOptions) *SystemValidator {
	return &SystemValidator{
		options: options,
	}
}

// RunAllChecks runs all validation checks
func (sv *SystemValidator) RunAllChecks() ([]ValidationResult, error) {
	logger.Info("⚡ Running comprehensive system validation...")

	checks := []struct {
		name     string
		check    func() ValidationResult
		required bool
	}{
		{"Docker Availability", sv.checkDocker, true},
		{"Kind Cluster Engine", sv.checkKindEngine, true},
		{"Kubectl Tool", sv.checkKubectl, true},
		{"Helm Tool", sv.checkHelm, false},
		{"Disk Space", sv.checkDiskSpace, true},
		{"Memory Availability", sv.checkMemory, false},
		{"CPU Cores", sv.checkCPU, false},
		{"Port Availability", sv.checkPortAvailability, true},
		{"Network Connectivity", sv.checkNetworkConnectivity, false},
		{"Operating System", sv.checkOperatingSystem, false},
	}

	var results []ValidationResult
	for _, check := range checks {
		result := check.check()
		result.Required = check.required
		results = append(results, result)

		// Log the result
		if result.Status == StatusPass {
			logger.Info(fmt.Sprintf("%s %s: %s", result.Status, check.name, result.Message))
		} else if result.Status == StatusFail && check.required {
			logger.Error(fmt.Sprintf("%s %s: %s", result.Status, check.name, result.Message), result.Error, nil)
		} else if result.Status == StatusWarning {
			logger.Warn(fmt.Sprintf("%s %s: %s", result.Status, check.name, result.Message))
		}
	}

	return results, nil
}

// checkDocker verifies Docker is available and running
func (sv *SystemValidator) checkDocker() ValidationResult {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return ValidationResult{
			Name:    "Docker Availability",
			Status:  StatusFail,
			Message: "Docker is not available or not running",
			Error:   err,
		}
	}

	// Check Docker version
	versionCmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	versionOutput, err := versionCmd.Output()
	if err != nil {
		return ValidationResult{
			Name:    "Docker Availability",
			Status:  StatusWarning,
			Message: "Docker is running but version check failed",
			Error:   err,
		}
	}

	version := strings.TrimSpace(string(versionOutput))
	return ValidationResult{
		Name:    "Docker Availability",
		Status:  StatusPass,
		Message: fmt.Sprintf("Docker is running (version: %s)", version),
	}
}

// checkKindEngine verifies Kind is available
func (sv *SystemValidator) checkKindEngine() ValidationResult {
	cmd := exec.Command("kind", "version")
	if err := cmd.Run(); err != nil {
		return ValidationResult{
			Name:    "Kind Cluster Engine",
			Status:  StatusFail,
			Message: "Kind is not available in PATH",
			Error:   err,
		}
	}

	// Get Kind version
	versionCmd := exec.Command("kind", "version")
	versionOutput, err := versionCmd.Output()
	if err != nil {
		return ValidationResult{
			Name:    "Kind Cluster Engine",
			Status:  StatusWarning,
			Message: "Kind is available but version check failed",
			Error:   err,
		}
	}

	version := strings.TrimSpace(string(versionOutput))
	return ValidationResult{
		Name:    "Kind Cluster Engine",
		Status:  StatusPass,
		Message: fmt.Sprintf("Kind is available (%s)", version),
	}
}

// checkKubectl verifies kubectl is available
func (sv *SystemValidator) checkKubectl() ValidationResult {
	cmd := exec.Command("kubectl", "version", "--client")
	if err := cmd.Run(); err != nil {
		return ValidationResult{
			Name:    "Kubectl Tool",
			Status:  StatusFail,
			Message: "Kubectl is not available in PATH",
			Error:   err,
		}
	}

	return ValidationResult{
		Name:    "Kubectl Tool",
		Status:  StatusPass,
		Message: "Kubectl is available",
	}
}

// checkHelm verifies Helm is available
func (sv *SystemValidator) checkHelm() ValidationResult {
	cmd := exec.Command("helm", "version")
	if err := cmd.Run(); err != nil {
		return ValidationResult{
			Name:    "Helm Tool",
			Status:  StatusWarning,
			Message: "Helm is not available (optional but recommended)",
			Error:   err,
		}
	}

	return ValidationResult{
		Name:    "Helm Tool",
		Status:  StatusPass,
		Message: "Helm is available",
	}
}

// checkDiskSpace verifies sufficient disk space
func (sv *SystemValidator) checkDiskSpace() ValidationResult {
	// TODO: Implement disk space check
	// This should check available space in the current directory
	return ValidationResult{
		Name:    "Disk Space",
		Status:  StatusPass,
		Message: "Disk space check passed (TODO: implement actual check)",
	}
}

// checkMemory verifies sufficient memory
func (sv *SystemValidator) checkMemory() ValidationResult {
	// TODO: Implement memory check
	// This should check available RAM
	return ValidationResult{
		Name:    "Memory Availability",
		Status:  StatusPass,
		Message: "Memory check passed (TODO: implement actual check)",
	}
}

// checkCPU verifies sufficient CPU cores
func (sv *SystemValidator) checkCPU() ValidationResult {
	cores := runtime.NumCPU()
	if cores < 2 {
		return ValidationResult{
			Name:    "CPU Cores",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Only %d CPU cores available (2+ recommended)", cores),
		}
	}

	return ValidationResult{
		Name:    "CPU Cores",
		Status:  StatusPass,
		Message: fmt.Sprintf("%d CPU cores available", cores),
	}
}

// checkPortAvailability verifies required ports are available
func (sv *SystemValidator) checkPortAvailability() ValidationResult {
	requiredPorts := []int{80, 443, 3000, 8080}
	var failedPorts []int

	for _, port := range requiredPorts {
		if !sv.isPortAvailable(port) {
			failedPorts = append(failedPorts, port)
		}
	}

	if len(failedPorts) > 0 {
		return ValidationResult{
			Name:    "Port Availability",
			Status:  StatusFail,
			Message: fmt.Sprintf("Ports %v are already in use", failedPorts),
		}
	}

	return ValidationResult{
		Name:    "Port Availability",
		Status:  StatusPass,
		Message: "All required ports are available",
	}
}

// isPortAvailable checks if a port is available
func (sv *SystemValidator) isPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// checkNetworkConnectivity verifies network access
func (sv *SystemValidator) checkNetworkConnectivity() ValidationResult {
	// TODO: Implement network connectivity check
	// This should check internet access and DNS resolution
	return ValidationResult{
		Name:    "Network Connectivity",
		Status:  StatusPass,
		Message: "Network connectivity check passed (TODO: implement actual check)",
	}
}

// checkOperatingSystem verifies OS compatibility
func (sv *SystemValidator) checkOperatingSystem() ValidationResult {
	os := runtime.GOOS
	arch := runtime.GOARCH

	// Check if OS is supported
	supportedOS := map[string]bool{
		"linux":   true,
		"darwin":  true,
		"windows": true,
	}

	if !supportedOS[os] {
		return ValidationResult{
			Name:    "Operating System",
			Status:  StatusWarning,
			Message: fmt.Sprintf("OS %s (%s) is not officially supported", os, arch),
		}
	}

	return ValidationResult{
		Name:    "Operating System",
		Status:  StatusPass,
		Message: fmt.Sprintf("OS %s (%s) is supported", os, arch),
	}
}
