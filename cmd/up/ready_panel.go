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
//
// `conv` is what the bootstrap last saw of GitOps convergence, and it decides
// whether this panel may say "ready". Pass nil only where convergence was never
// attempted.
//
// WHY THE PANEL IS CONDITIONAL. It used to be printed on every successful
// provision, unconditionally — a green "Platform ready" box listing seven URLs,
// the console first. On the first GCP bring-up (2026-10-03) every one of those
// URLs refused connections: Keycloak was in a startup-probe restart loop, so
// `keycloak-clients` was never written, so the console's own ExternalSecret
// never synced, so the console Deployment at sync-wave 20 was never created.
// The controller had logged the truth ("still converging at the timeout") and
// then this panel overrode that impression with a list of links. An operator
// reads the box, not the log line above it.
//
// The platform genuinely does keep converging in the background, and that is
// worth handing over — but handing it over as "ready" when the entry point is
// not serving is the same failure as `adhar down` green-ticking a cluster that
// was still running.
func renderCloudReadyPanel(host, clusterName string, conv *convergenceSnapshot) string {
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
	if conv.serving() {
		return "\n" + helpers.RenderReadyPanel(access, hints)
	}
	return "\n" + helpers.RenderConvergingPanel(convergingHeadline(conv), access, hints)
}

// convergingHeadline says, in one line, why the URLs above are not promised yet.
//
// It names the count rather than a duration: "39/75" is checkable against
// `adhar get status`, whereas "a few more minutes" is a guess that is wrong
// exactly when it matters — a console stuck behind a crash-looping dependency
// never arrives, and the operator needs to go looking rather than wait.
func convergingHeadline(conv *convergenceSnapshot) string {
	if conv == nil || !conv.Known {
		return "Platform convergence could not be read — check `adhar get status` before using the URLs below"
	}
	gate := conv.GateApp
	if gate == "" {
		gate = "the console"
	}
	if conv.Total > 0 {
		return fmt.Sprintf("Cluster is up; %s is NOT serving yet (%d/%d apps Synced + Healthy). ArgoCD keeps converging.",
			gate, conv.Healthy, conv.Total)
	}
	return fmt.Sprintf("Cluster is up; %s is NOT serving yet. ArgoCD keeps converging.", gate)
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
