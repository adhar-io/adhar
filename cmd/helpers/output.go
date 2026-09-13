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

// output.go decides when the CLI's decorative header and footer must be
// suppressed because stdout is being consumed by another program.
//
// Why this exists: the root command printed the banner and footer around EVERY
// command's stdout, which silently broke every machine-readable output the CLI
// advertises. `adhar auth token` documents itself as
//
//	curl -H "Authorization: Bearer $(adhar auth token)" ...
//
// and the design docs use `kubectl --token "$(adhar auth token)"` — both of which
// captured the banner line and the "Built with ❤️" footer along with the token,
// producing an unusable credential. `-o json` was worse: no `adhar ... -o json`
// output anywhere in the CLI could be piped into `jq`, because the JSON document
// was never the whole of stdout.

import (
	"strings"

	"github.com/spf13/cobra"
)

// AnnotationPlainOutput marks a command whose stdout is machine-readable even in
// its default (non-JSON) form, so the header and footer must be suppressed.
// `adhar auth token` is the canonical case: its normal output IS a bearer token.
const AnnotationPlainOutput = "adhar.io/plain-output"

// machineReadableFormats are the `-o` values whose output is a document meant for
// another program. Deliberately an exact match: `adhar cluster kubeconfig -o`
// takes a FILE PATH rather than a format, and a path such as
// `/tmp/kubeconfig.yaml` must not be mistaken for a format request.
var machineReadableFormats = map[string]bool{
	"json": true,
	"yaml": true,
	"yml":  true,
}

// IsMachineReadableOutput reports whether cmd's stdout should carry nothing but
// the command's own payload.
func IsMachineReadableOutput(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Annotations[AnnotationPlainOutput] == "true" {
		return true
	}
	// Flags() includes flags inherited from parents, which is where the command
	// groups (`auth`, `get`, …) define -o/--output.
	f := cmd.Flags().Lookup("output")
	if f == nil {
		return false
	}
	return machineReadableFormats[strings.ToLower(strings.TrimSpace(f.Value.String()))]
}
