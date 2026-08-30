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

import "github.com/spf13/cobra"

// ProjectCmd is the parent for project hierarchy operations. A Project is owned
// by a Team (within an Organisation) and, via the Adhar control plane
// (Crossplane CompositeProject), provisions a guard-railed namespace, an ArgoCD
// AppProject, and its own Gitea repository. This is the same control-plane path
// the Adhar Console uses, so CLI and Console behave identically.
var ProjectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage Adhar projects (Organisation → Team → Project → Application)",
	Long: `Manage projects in the Adhar ownership hierarchy.

A Project is owned by a Team and provisions — through the control plane — a
namespace with quotas, an ArgoCD AppProject, and a Gitea repository. Create
Applications inside a Project.`,
}

func init() {
	ProjectCmd.AddCommand(createCmd)
}
