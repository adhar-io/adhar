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

package up

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/platform/controllers/adharplatform"
)

// verifyConsoleReady is the last word of `adhar up`, on every path out of the
// wait: the controller shutting down on its own, the apps budget running out,
// or the platform status becoming unreadable. Whatever ended the wait, the
// run completes ONLY if adhar-console is Synced + Healthy and serving — the
// same gate the controller waits for (adharplatform.ConsoleGate).
//
// Before this, a spent budget and an unreadable API both fell through to
// "install the adhar-controlplane manager and return nil", so the checklist printed
// every stage ticked over a console that was not up (DigitalOcean and Civo,
// 2026-10-15). An honest failure here is resumable: ArgoCD keeps converging,
// the adhar-controlplane manager is already installed, and a re-run of `adhar up`
// picks the wait back up.
func verifyConsoleReady(ctx context.Context, c client.Client) error {
	return verifyConsoleReadyWithin(ctx, c, consoleVerdictGrace, consoleVerdictPoll)
}

// consoleVerdictGrace is how long the final verdict waits for the console to
// settle after the wait ended, and consoleVerdictPoll how often it re-reads.
//
// The verdict used to be ONE read. The bootstrap controller hands over when the
// console first reads Healthy and serving, the adhar-controlplane manager install takes
// up to two minutes, and in that window the console's PostSync hook restarts
// the console Deployment (it reloads the OIDC client it has just been given) —
// so the single read landed on Progressing and failed a run whose console was
// ninety seconds from Healthy. Three DO bring-ups in a row ended that way
// (2026-10-17). The grace is generous because a console still rolling out is
// the common case here, not a fault; a console that never settles still fails.
const (
	consoleVerdictGrace = 10 * time.Minute
	consoleVerdictPoll  = 10 * time.Second
)

func verifyConsoleReadyWithin(ctx context.Context, c client.Client, grace, poll time.Duration) error {
	deadline := time.Now().Add(grace)
	var detail string
	for {
		readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var ready bool
		ready, detail = adharplatform.ConsoleGate(readCtx, c)
		cancel()
		if ready {
			return nil
		}
		if time.Now().Add(poll).After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	return fmt.Errorf("adhar up did not complete: %s. The run completes only once the console is Healthy and serving; "+
		"ArgoCD keeps converging in the background and the adhar-controlplane manager is installed — re-run `adhar up` to resume "+
		"the wait, or inspect `adhar get status`", detail)
}
