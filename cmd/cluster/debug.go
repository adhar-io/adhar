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

package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

var debugCmd = &cobra.Command{
	Use:   "debug [name]",
	Short: "Debug a cluster instance",
	Long:  "Provides SSH command to connect to a cluster's master node for debugging.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return debugCluster(cmd, args[0])
	},
}

func debugCluster(_ *cobra.Command, clusterName string) error {
	fmt.Printf("🔍 Attempting to debug cluster: %s\n", clusterName)

	// This is a Civo-specific debug implementation for now.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not get user home directory: %w", err)
	}

	// The private key for the first master node is saved with a predictable name.
	masterKeyName := fmt.Sprintf("%s-master-0.pem", clusterName)
	keyPath := filepath.Join(homeDir, ".adhar", "keys", masterKeyName)

	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return fmt.Errorf("private key for cluster '%s' not found at '%s'. Please run the cluster creation again to generate the key.", clusterName, keyPath)
	}

	fmt.Printf("🔑 Private key found: %s\n", keyPath)

	// Now, we need to find the public IP of the master node.
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	providerConfig, ok := cfg.Providers["civo"]
	if !ok {
		return fmt.Errorf("civo provider not found in configuration")
	}
	prov, err := pfactory.DefaultFactory.CreateProvider("civo", providerConfig.ToProviderMap())
	if err != nil {
		return fmt.Errorf("failed to create civo provider: %w", err)
	}

	clusters, err := prov.ListClusters(context.Background())
	if err != nil {
		return fmt.Errorf("failed to list clusters: %w", err)
	}

	var masterIP string
	for _, cluster := range clusters {
		if cluster.Name == clusterName {
			masterIP = strings.TrimPrefix(cluster.Endpoint, "https://")
			masterIP = strings.TrimSuffix(masterIP, ":6443")
			break
		}
	}

	if masterIP == "" {
		return fmt.Errorf("could not find a public IP for the master node of cluster '%s'. Is the cluster still running?", clusterName)
	}

	fmt.Printf("🖥️ Master node IP found: %s\n", masterIP)
	fmt.Printf("\nTo connect to the master node, run the following command in your terminal:\n\n")
	fmt.Printf("ssh -i %s root@%s\n\n", keyPath, masterIP)
	fmt.Printf("Once connected, you can check the setup log with:\n\n")
	fmt.Printf("tail -f /var/log/k8s-setup.log\n\n")

	return nil
}
