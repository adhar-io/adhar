package k8s

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"adhar-io/adhar/globals"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VeleroNamespaceEnv lets an operator pin the namespace when discovery cannot
// reach the cluster or Velero was installed somewhere unusual.
const VeleroNamespaceEnv = "ADHAR_VELERO_NAMESPACE"

// veleroDefaultNamespace is Velero's own upstream default, kept only as the last
// guess for a cluster this platform did not install.
const veleroDefaultNamespace = "velero"

var (
	veleroNSOnce sync.Once
	veleroNS     string
)

// VeleroNamespace reports the namespace Velero actually runs in.
//
// It is NOT a constant, and specifically not the upstream default "velero".
// This platform installs every package into `adhar-system` (ADR-0011), so every
// backup and restore subcommand that hardcoded "velero" failed outright against
// its own cluster:
//
//	adhar backup create adhar-verify-1
//	  ✖ failed to create backup "adhar-verify-1": namespaces "velero" not found
//
// Velero itself was healthy in `adhar-system` the whole time. Swapping one
// hardcoded namespace for another would only move the bug to anyone running an
// upstream Velero, so this discovers it and falls back in a documented order:
//
//  1. $ADHAR_VELERO_NAMESPACE — an explicit operator override always wins.
//  2. The namespace of a Deployment named `velero`, checking this platform's
//     namespace first and Velero's upstream default second.
//  3. Any namespace holding a Deployment labelled `app.kubernetes.io/name=velero`,
//     which covers a Helm install into a third namespace.
//  4. `adhar-system` — the platform's own convention, so the error an operator
//     sees names the place Velero is SUPPOSED to be rather than somewhere it
//     never was.
//
// Resolved once per process: the answer cannot change under a running command,
// and every subcommand reads it several times.
func VeleroNamespace() string {
	veleroNSOnce.Do(func() { veleroNS = discoverVeleroNamespace() })
	return veleroNS
}

func discoverVeleroNamespace() string {
	if ns := strings.TrimSpace(os.Getenv(VeleroNamespaceEnv)); ns != "" {
		return ns
	}

	cs, err := GetClientset()
	if err != nil {
		// Unreachable cluster. The caller will fail on its own next call with a
		// connection error, which is a better message than anything about
		// namespaces, so just name the expected place.
		return globals.AdharSystemNamespace
	}

	// Short timeout on purpose: this runs BEFORE the work the operator asked for,
	// so every second here is added to the time-to-error on an unreachable
	// cluster — and the command is going to fail with a connection error anyway.
	// A Deployment Get is a cheap call; 5s covers a loaded API server without
	// making `adhar backup list` sit silent on a dead one.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, ns := range []string{globals.AdharSystemNamespace, veleroDefaultNamespace} {
		if _, err := cs.AppsV1().Deployments(ns).Get(ctx, "velero", metav1.GetOptions{}); err == nil {
			return ns
		}
	}

	// A Helm install into some third namespace still sets the standard label.
	if list, err := cs.AppsV1().Deployments("").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=velero",
		Limit:         5,
	}); err == nil && len(list.Items) > 0 {
		return list.Items[0].Namespace
	}

	return globals.AdharSystemNamespace
}

// resetVeleroCache clears the memoised answer.
//
// Exists for the tests: VeleroNamespace memoises because the answer cannot
// change under a running command, and that same memoisation would make every
// test after the first one assert against a stale value.
func resetVeleroCache() {
	veleroNSOnce = sync.Once{}
	veleroNS = ""
}
