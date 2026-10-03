package up

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"
)

// ready_panel.go gives a cloud bring-up the SAME closing panel as a local one.
//
// The cloud path had grown its own: a bold "Access" heading over three
// hand-indented Printf lines with two URLs squeezed onto one row and a
// parenthetical example trailing off the end. Local meanwhile called
// helpers.RenderReadyPanel and got a bordered, aligned, brand-coloured table.
// Same product, same moment in the run, two different-looking endings — and the
// cloud one was the uglier of the two while describing the more expensive cluster.
//
// Everything is the shared renderer now, so the two cannot drift apart again: the
// only difference is the URL shape (a cloud platform is behind a load balancer on
// 443, so no port suffix) and one extra hint naming the kube-context, which a
// cloud run has and a local one does not need.

// cloudPlatformApps are the UIs listed in the panel, in the order they appear.
// Matches the local list: the platform installs the same things on every provider,
// and an operator moving between them should not have to re-learn the output.
var cloudPlatformApps = []string{"Console", "ArgoCD", "Gitea", "Keycloak", "Grafana", "Headlamp", "Hubble"}

// renderCloudReadyPanel is the closing panel for a provisioned cloud environment.
func renderCloudReadyPanel(host, clusterName string) string {
	access := make([][2]string, 0, len(cloudPlatformApps))
	for _, name := range cloudPlatformApps {
		// No port: a cloud platform is served by the Gateway's load balancer on
		// 443, unlike the local Kind flow's high port.
		access = append(access, [2]string{name, fmt.Sprintf("https://%s.%s", lower(name), host)})
	}

	hints := [][2]string{
		{"Credentials", "adhar get secrets"},
		{"Status", "adhar get status"},
		{"Context", "kubectl config use-context " + KubeContextName(clusterName)},
		{"Teardown", "adhar down -f <config> --env <env>"},
	}
	// Leading newline so the panel is not flush against whatever printed above it
	// (a summary box, or the blocker warning).
	return "\n" + helpers.RenderReadyPanel(access, hints)
}

// lower is ASCII-only on purpose: these are fixed app names, and
// strings.ToLower would pull in locale behaviour for no benefit.
func lower(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}
