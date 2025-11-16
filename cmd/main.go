/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"adhar-io/adhar/cmd/apps"
	"adhar-io/adhar/cmd/auth"
	"adhar-io/adhar/cmd/backup"
	"adhar-io/adhar/cmd/cluster"
	"adhar-io/adhar/cmd/config"
	"adhar-io/adhar/cmd/db"
	"adhar-io/adhar/cmd/down"
	"adhar-io/adhar/cmd/env"
	"adhar-io/adhar/cmd/get"
	"adhar-io/adhar/cmd/gitops"
	"adhar-io/adhar/cmd/health"
	"adhar-io/adhar/cmd/help"
	"adhar-io/adhar/cmd/logs"
	"adhar-io/adhar/cmd/metrics"
	"adhar-io/adhar/cmd/network"
	"adhar-io/adhar/cmd/pipeline"
	"adhar-io/adhar/cmd/policy"
	"adhar-io/adhar/cmd/restore"
	"adhar-io/adhar/cmd/scale"
	"adhar-io/adhar/cmd/secrets"
	"adhar-io/adhar/cmd/security"
	"adhar-io/adhar/cmd/service"
	"adhar-io/adhar/cmd/storage"
	"adhar-io/adhar/cmd/traces"
	"adhar-io/adhar/cmd/up"
	"adhar-io/adhar/cmd/version"
	"adhar-io/adhar/cmd/webhook"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	_ "k8s.io/client-go/plugin/pkg/client/auth" // Required for cloud provider auth plugins

	// Import providers to ensure they register themselves
	_ "adhar-io/adhar/platform/providers/aws"
	_ "adhar-io/adhar/platform/providers/azure"
	_ "adhar-io/adhar/platform/providers/civo"
	_ "adhar-io/adhar/platform/providers/custom"
	_ "adhar-io/adhar/platform/providers/digitalocean"
	_ "adhar-io/adhar/platform/providers/gcp"
	_ "adhar-io/adhar/platform/providers/kind"
)

func main() {
	// Set the globals from the version package (which is set via ldflags)
	globals.Version = version.Version
	globals.GitCommit = version.GitCommit
	globals.BuildDate = version.BuildDate

	// Initialize platform logger with default configuration
	logger.Init(logger.DefaultConfig())

	// Handle Graceful Shutdown
	interrupted := make(chan os.Signal, 1)
	defer close(interrupted)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupted)

	ctx, cancel := context.WithCancelCause(context.Background())

	go func() {
		<-interrupted
		cancel(fmt.Errorf("command interrupted"))
	}()

	// Execute the root command
	if err := Execute(ctx); err != nil {
		logger.Error("Command execution failed", err, map[string]interface{}{
			"command": os.Args,
		})
		os.Exit(1)
	}
}

func init() {
	// Add modular commands
	AddCommand(
		up.UpCmd,             // Up command for platform creation
		down.DownCmd,         // Down command for platform teardown
		get.GetCmd,           // Get command for resource information
		apps.AppsCmd,         // Apps command for application management
		cluster.ClusterCmd,   // Cluster command for cluster management
		config.ConfigCmd,     // Config command for configuration management
		env.EnvCmd,           // Environment command for environment management
		health.HealthCmd,     // Health command for platform health monitoring
		logs.LogsCmd,         // Logs command for centralized logging
		security.SecurityCmd, // Security command for security operations

		auth.AuthCmd,         // Auth command for authentication and authorization
		gitops.GitOpsCmd,     // GitOps command for GitOps operations
		network.NetworkCmd,   // Network command for network diagnostics
		db.DBCmd,             // DB command for database management
		metrics.MetricsCmd,   // Metrics command for metrics management
		traces.TracesCmd,     // Traces command for distributed tracing
		pipeline.PipelineCmd, // Pipeline command for CI/CD pipelines
		storage.StorageCmd,   // Storage command for storage management
		webhook.WebhookCmd,   // Webhook command for webhook management
		secrets.SecretsCmd,   // Secrets command for secrets management
		service.ServiceCmd,   // Service command for service management
		scale.ScaleCmd,       // Scale command for resource scaling
		backup.BackupCmd,     // Backup command for backup management
		restore.RestoreCmd,   // Restore command for restoration
		policy.PolicyCmd,     // Policy command for policy management
		help.HelpCmd,         // Help command for enhanced help
		version.VersionCmd,   // Version command for version information
	)
}
