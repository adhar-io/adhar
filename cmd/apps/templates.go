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

package apps

// `adhar application templates` lists the platform's golden paths and, for one
// template, the parameters `deploy --param` accepts. Without it the parameter
// names are only discoverable by reading template.yaml in Gitea, which makes a
// required parameter look like an arbitrary failure.

import (
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"code.gitea.io/sdk/gitea"
	"github.com/spf13/cobra"
)

var templatesCmd = &cobra.Command{
	Use:     "templates [template]",
	Aliases: []string{"template"},
	Short:   "List the platform's application templates",
	Long: `List the golden-path templates available to 'adhar application deploy --template'.

Templates come from the curated ` + globals.GiteaPlatformOrg + `/` + globals.GitOpsRepoTemplates + ` repository in the
platform's Gitea — the same collection the Adhar Console's Create wizard offers.

Naming a template shows the parameters it accepts, which are passed as
--param key=value.

Examples:
  adhar application templates
  adhar application templates go-web-service`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTemplates,
}

func init() {
	ApplicationCmd.AddCommand(templatesCmd)
}

func runTemplates(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	gc, err := newGiteaClient(ctx)
	if err != nil {
		return fmt.Errorf("connecting to the platform Gitea (is the cluster up?): %w", err)
	}
	if len(args) == 1 {
		return describeTemplate(gc, args[0])
	}
	return listTemplates(gc)
}

func listTemplates(gc *gitea.Client) error {
	entries, _, err := gc.ListContents(globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, "main", globals.GitOpsTemplatesPath)
	if err != nil {
		return fmt.Errorf("listing %s/%s/%s: %w",
			globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, globals.GitOpsTemplatesPath, err)
	}
	var ids []string
	for _, e := range entries {
		if e != nil && e.Type == "dir" && e.Name != "organization" {
			ids = append(ids, e.Name)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return fmt.Errorf("no templates found in %s/%s under %s; is the adhar-libraries mirror healthy?",
			globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, globals.GitOpsTemplatesPath)
	}

	table := helpers.NewTable("TEMPLATE", "TITLE", "DESCRIPTION")
	for _, id := range ids {
		title, description := id, ""
		// A template that fails to parse is still listed by id: it can be named
		// on the command line, and hiding it would look like it does not exist.
		if t, err := loadBackstageTemplate(gc, id); err == nil {
			if t.Metadata.Title != "" {
				title = t.Metadata.Title
			}
			description = firstSentence(t.Metadata.Description)
		}
		table.Row(id, title, description)
	}
	fmt.Println(table.Render())
	fmt.Printf("\n%d templates from %s/%s. Parameters: adhar application templates <template>\n",
		len(ids), globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates)
	return nil
}

func describeTemplate(gc *gitea.Client, id string) error {
	t, err := loadBackstageTemplate(gc, id)
	if err != nil {
		return err
	}
	title := t.Metadata.Title
	if title == "" {
		title = id
	}
	fmt.Println(helpers.TitleStyle.Render(title))
	if d := strings.TrimSpace(t.Metadata.Description); d != "" {
		fmt.Printf("\n%s\n", d)
	}

	table := helpers.NewTable("PARAMETER", "TYPE", "REQUIRED", "DEFAULT", "DESCRIPTION")
	rows := 0
	for _, group := range t.Spec.Parameters {
		required := map[string]bool{}
		for _, r := range group.Required {
			required[r] = true
		}
		keys := make([]string, 0, len(group.Properties))
		for k := range group.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := group.Properties[k]
			def := ""
			if !p.Default.IsZero() {
				var v any
				if p.Default.Decode(&v) == nil {
					def = stringify(v)
				}
			}
			req := ""
			if required[k] {
				req = "yes"
			}
			typ := p.Type
			if typ == "" {
				typ = "string"
			}
			table.Row(k, typ, req, def, firstSentence(p.Description))
			rows++
		}
	}
	if rows == 0 {
		fmt.Println("\nThis template declares no parameters.")
		return nil
	}
	fmt.Printf("\n%s\n", table.Render())
	fmt.Printf("\nDeploy it:\n  adhar application deploy <name> --template=%s\n", id)
	return nil
}

// firstSentence keeps a table row to one line; template descriptions are written
// as multi-line prose for the Console's cards.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if len(s) > 90 {
		s = strings.TrimSpace(s[:87]) + "…"
	}
	return s
}
