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

// Package migrate holds staged, reversible platform migrations. The first is
// `split-planes` (ADR-0023): move an existing dual-role cluster to the
// control-plane / data-plane model without a flag day.
package migrate

import "github.com/spf13/cobra"

// MigrateCmd is the parent for platform migrations.
var MigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Run staged, reversible platform migrations",
	Long: `Run staged, reversible platform migrations.

Migrations converge the platform toward a newer topology in small, verifiable
steps — each reversible, each defaulting to a dry run.

Available migrations:
  split-planes   Move a dual-role cluster to the control-plane / data-plane
                 model (ADR-0023): stand up a local data plane, re-home
                 application workloads onto it, then enforce the separation.

Examples:
  adhar migrate split-planes             # dry run: print the staged plan + status
  adhar migrate split-planes --execute   # create the local data plane, print Git steps`,
}

func init() {
	MigrateCmd.AddCommand(splitPlanesCmd)
}
