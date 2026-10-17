/*
Copyright 2026 The Adhar Authors.

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

package adharplatform

import (
	"context"
	"fmt"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	synccommon "github.com/cnoe-io/argocd-api/api/argo/gitops-engine"
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/globals"
)

// ConsoleGate is the one definition of "the platform is up": the entry-point
// application (adhar-console) is Synced + Healthy AND its Service has a ready
// endpoint. The reconciler uses it to decide when a bootstrap run may stop,
// and `adhar up` uses it for its verdict — the same function, so the CLI
// cannot report completion on a weaker condition than the controller waited
// for. `detail` is a sentence a person can act on when it is false.
func ConsoleGate(ctx context.Context, c client.Client) (ready bool, detail string) {
	app := &argov1alpha1.Application{}
	if err := c.Get(ctx, client.ObjectKey{Name: readyGateApp, Namespace: globals.AdharSystemNamespace}, app); err != nil {
		return false, fmt.Sprintf("Application %s could not be read: %v", readyGateApp, err)
	}
	serving, err := consoleServing(ctx, c)
	state := fmt.Sprintf("%s is %s/%s", readyGateApp, app.Status.Sync.Status, app.Status.Health.Status)
	switch {
	case err != nil:
		// A read failure is a fact about the CALLER (scheme, RBAC, API), not
		// about the console; saying "not serving" here sent an operator
		// chasing a healthy console (2026-10-16).
		state += fmt.Sprintf("; whether it is serving could not be determined (listing EndpointSlices of Service %s: %v)", consoleServiceName, err)
	case serving:
		state += " and serving"
	default:
		state += fmt.Sprintf(" and not serving (no ready endpoint behind Service %s)", consoleServiceName)
	}
	if operationInFlight(app) {
		state += "; a sync operation is still running"
	}
	return consoleSettled(app) && serving && err == nil, state
}

// consoleSettled is appConverged plus "no sync operation in flight". Argo CD
// health is a verdict on the managed resources and says nothing about hooks:
// the console's PostSync hook (adhar-console-oidc-reload) restarts the console
// Deployment the moment its OIDC client exists, so on every fresh cloud cluster
// the Application read Synced/Healthy with a ready endpoint for a few seconds,
// the bootstrap controller handed over, and the CLI's verdict then found it
// Progressing again and failed the run (three DO bring-ups, 2026-10-17). A
// run completes on a console that has FINISHED syncing, hooks included.
func consoleSettled(app *argov1alpha1.Application) bool {
	return appConverged(app) && !operationInFlight(app)
}

func operationInFlight(app *argov1alpha1.Application) bool {
	if app.Status.OperationState == nil {
		return false
	}
	switch app.Status.OperationState.Phase {
	case synccommon.OperationRunning, synccommon.OperationTerminating:
		return true
	}
	return false
}

// consoleServing reports whether the console Service has a READY endpoint —
// a Healthy Application with no endpoint is a console a browser cannot open.
// The error is returned, not folded into false: a client that cannot list
// EndpointSlices (scheme, RBAC, API) has learned nothing about the console,
// and the caller must say so rather than claim it is down.
func consoleServing(ctx context.Context, c client.Client) (bool, error) {
	slices := &discoveryv1.EndpointSliceList{}
	if err := c.List(ctx, slices,
		client.InNamespace(globals.AdharSystemNamespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: consoleServiceName},
	); err != nil {
		return false, err
	}
	for _, sl := range slices.Items {
		for _, ep := range sl.Endpoints {
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				return true, nil
			}
		}
	}
	return false, nil
}
