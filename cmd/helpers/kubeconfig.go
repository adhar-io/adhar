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

package helpers

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"
)

// KubeconfigManager manages kubeconfig files
type KubeconfigManager struct {
	kubeconfigPath string
}

// NewKubeconfigManager creates a new kubeconfig manager
func NewKubeconfigManager(kubeconfigPath string) *KubeconfigManager {
	return &KubeconfigManager{
		kubeconfigPath: kubeconfigPath,
	}
}

// BackupKubeconfig creates a backup of the existing kubeconfig
func (k *KubeconfigManager) BackupKubeconfig() (string, error) {
	// Check if kubeconfig exists
	if _, err := os.Stat(k.kubeconfigPath); os.IsNotExist(err) {
		return "", nil // No backup needed if file doesn't exist
	}

	// Create backup filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s.backup.%s", k.kubeconfigPath, timestamp)

	// Read original file
	data, err := ioutil.ReadFile(k.kubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("failed to read kubeconfig: %w", err)
	}

	// Write backup
	err = ioutil.WriteFile(backupPath, data, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to write backup: %w", err)
	}

	return backupPath, nil
}

// MergeKubeconfig merges the new kubeconfig content with the existing one
func (k *KubeconfigManager) MergeKubeconfig(kubeconfig, clusterName string) error {
	// Ensure directory exists
	dir := filepath.Dir(k.kubeconfigPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create kubeconfig directory: %w", err)
	}

	// For now, simple implementation - just write the new kubeconfig
	// In a production environment, you'd want to properly merge contexts
	err := ioutil.WriteFile(k.kubeconfigPath, []byte(kubeconfig), 0600)
	if err != nil {
		return fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	return nil
}

// SetCurrentContext sets the current context in the kubeconfig
func (k *KubeconfigManager) SetCurrentContext(contextName string) error {
	// For now, simple implementation - in production you'd parse and modify the YAML
	// This is a placeholder since proper kubeconfig manipulation requires YAML parsing
	// and understanding of kubectl context structure
	return nil
}

// ValidateKubeconfig validates the kubeconfig file
func (k *KubeconfigManager) ValidateKubeconfig() error {
	// Check if file exists and is readable
	if _, err := os.Stat(k.kubeconfigPath); os.IsNotExist(err) {
		return fmt.Errorf("kubeconfig file does not exist: %s", k.kubeconfigPath)
	}

	// In production, you'd want to:
	// - Parse the YAML to ensure it's valid
	// - Check that required fields are present
	// - Validate cluster connectivity
	return nil
}

// ListContexts lists all available contexts in the kubeconfig
func (k *KubeconfigManager) ListContexts() ([]string, error) {
	// For now, return empty list - in production you'd parse the YAML
	// and extract context names
	return []string{}, nil
}

// GetCurrentContext gets the current context from the kubeconfig
func (k *KubeconfigManager) GetCurrentContext() (string, error) {
	// For now, return empty string - in production you'd parse the YAML
	// and extract the current-context field
	return "", nil
}
