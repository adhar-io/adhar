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

package help

import "github.com/spf13/cobra"

// HelpCmd delegates to the shared, persona-grouped help renderer installed via
// rootCmd.SetHelpFunc, so `adhar help [command]` renders identically to
// `adhar [command] --help`. Keeping a single rendering path means the help UX
// never diverges between the two ways users ask for it.
var HelpCmd = &cobra.Command{
	Use:   "help [command]",
	Short: "Get help on any command",
	Long: `Show help for Adhar or any of its commands.

  adhar help              overview of every command, grouped by persona
  adhar help <command>    detailed help for a command (same as --help)`,
	Run: func(cmd *cobra.Command, args []string) {
		target, _, err := cmd.Root().Find(args)
		if err != nil || target == nil {
			target = cmd.Root()
		}
		target.InitDefaultHelpFlag()
		_ = target.Help()
	},
}
