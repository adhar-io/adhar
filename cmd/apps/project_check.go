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

import (
	"context"
	"fmt"
	"path"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var appProjectGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}

// projectAllowsNamespace reports whether an Argo CD AppProject's
// spec.destinations admit namespace ns. Destination namespaces are globs, as
// Argo CD treats them (`*`, `team-*`). It also returns the patterns it saw, for
// the error message.
func projectAllowsNamespace(project *unstructured.Unstructured, ns string) (bool, []string) {
	dests, _, _ := unstructured.NestedSlice(project.Object, "spec", "destinations")
	var patterns []string
	for _, d := range dests {
		m, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		pattern, _ := m["namespace"].(string)
		if pattern == "" {
			continue
		}
		patterns = append(patterns, pattern)
		if ok, _ := path.Match(pattern, ns); ok {
			return true, patterns
		}
	}
	return false, patterns
}

// checkProjectAdmitsNamespace fails fast when the Argo CD project the
// Application will use cannot deploy into its destination namespace.
//
// Without it the CLI scaffolds the template into a new Gitea repository, creates
// the Application, and only then does Argo CD refuse the sync — leaving a
// non-empty repo that blocks a re-run. The check is best effort: if the
// AppProject can't be read (no cluster access, Argo CD not installed, a project
// managed elsewhere) it steps aside and Argo CD remains the authority.
func checkProjectAdmitsNamespace(ctx context.Context, project, ns string, projectFlagSet bool) error {
	dc, err := helpers.DynamicClient()
	if err != nil {
		return nil
	}
	obj, err := dc.Resource(appProjectGVR).Namespace(globals.AdharSystemNamespace).Get(ctx, project, metav1.GetOptions{})
	if err != nil {
		// Not found may only mean Crossplane hasn't composed it yet (the
		// platform's `adhar` Project is created asynchronously), so warn rather
		// than block; Argo CD reports it if it is still missing at sync time.
		if apierrors.IsNotFound(err) {
			fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Argo CD project %q not found yet; the sync will fail if it never appears (use --project to choose another).", project)))
		}
		return nil
	}
	if ok, patterns := projectAllowsNamespace(obj, ns); !ok {
		hint := "pass --project with a project that owns it — your team's Project, or Argo CD's unrestricted `default`"
		if !projectFlagSet {
			hint = fmt.Sprintf("--project defaults to %q; %s", project, hint)
		}
		return fmt.Errorf("argo CD project %q does not allow namespace %q (it allows: %v); %s", project, ns, patterns, hint)
	}
	return nil
}
