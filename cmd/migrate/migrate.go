/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package migrate holds the staged, reversible platform migrations. They are
// surfaced as subcommands of `adhar upgrade` (wired in cmd/main.go) rather than
// under a separate top-level verb: a migration is one kind of upgrade, and an
// operator looking for "how do I move this platform forward" should find both
// the version converge and the topology change in the same place.
//
// `split-planes` (ADR-0023): move an existing dual-role cluster to the
// control-plane / data-plane model without a flag day.
package migrate

import "github.com/spf13/cobra"

// Migrations returns every migration subcommand, in the order they should be
// offered.
func Migrations() []*cobra.Command {
	return []*cobra.Command{SplitPlanesCmd}
}
