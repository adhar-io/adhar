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

package project

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var (
	projOrg       string
	projTeam      string
	projName      string
	projDesc      string
	projTier      string
	projCPU       string
	projMemory    string
	projPods      int
	projNoRepo    bool
	projNamespace = "adhar-system"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new project",
	Long: `Create a new project owned by a Team. The control plane provisions a
guard-railed namespace, an ArgoCD AppProject, and a Gitea repository.

Examples:
  adhar project create --org acme --team payments --name acme-shop
  adhar project create --org acme --team data --name lake --tier staging --cpu 8 --memory 16Gi`,
	RunE: runCreate,
}

func init() {
	createCmd.Flags().StringVar(&projOrg, "org", "", "Owning organisation (required)")
	createCmd.Flags().StringVar(&projTeam, "team", "", "Owning team (required)")
	createCmd.Flags().StringVar(&projName, "name", "", "Project name — also the namespace (required)")
	createCmd.Flags().StringVar(&projDesc, "description", "", "Project description")
	createCmd.Flags().StringVar(&projTier, "tier", "dev", "Tier: dev, test, staging, prod")
	createCmd.Flags().StringVar(&projCPU, "cpu", "4", "CPU quota")
	createCmd.Flags().StringVar(&projMemory, "memory", "8Gi", "Memory quota")
	createCmd.Flags().IntVar(&projPods, "pods", 30, "Pod quota")
	createCmd.Flags().BoolVar(&projNoRepo, "no-repo", false, "Do not create a Gitea repository")
}

func runCreate(cmd *cobra.Command, args []string) error {
	if projOrg == "" || projTeam == "" || projName == "" {
		return fmt.Errorf("--org, --team and --name are required")
	}

	logger.Info(fmt.Sprintf("📦 Creating project %s (org: %s, team: %s, provider: %s)",
		projName, projOrg, projTeam, helpers.ActiveProvider()))

	parameters := map[string]interface{}{
		"organisation":     projOrg,
		"team":             projTeam,
		"name":             projName,
		"description":      projDesc,
		"tier":             projTier,
		"cpuQuota":         projCPU,
		"memoryQuota":      projMemory,
		"podQuota":         projPods,
		"createRepository": !projNoRepo,
	}

	// Same provider-aware control-plane path the Console uses.
	xr := helpers.NewXR("CompositeProject", projName, projNamespace, "project", nil,
		map[string]interface{}{"parameters": parameters})

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := helpers.ApplyXR(ctx, "compositeprojects", xr); err != nil {
		return fmt.Errorf("create project: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf(
		"Project %s created — namespace, ArgoCD AppProject%s provisioning via the control plane",
		projName, repoSuffix())))
	return nil
}

func repoSuffix() string {
	if projNoRepo {
		return ""
	}
	return " and Gitea repository"
}
