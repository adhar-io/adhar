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
// "install the in-cluster manager and return nil", so the checklist printed
// every stage ticked over a console that was not up (DigitalOcean and Civo,
// 2026-10-15). An honest failure here is resumable: ArgoCD keeps converging,
// the in-cluster manager is already installed, and a re-run of `adhar up`
// picks the wait back up.
func verifyConsoleReady(ctx context.Context, c client.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ready, detail := adharplatform.ConsoleGate(ctx, c)
	if ready {
		return nil
	}
	return fmt.Errorf("adhar up did not complete: %s. The run completes only once the console is Healthy and serving; "+
		"ArgoCD keeps converging in the background and the in-cluster manager is installed — re-run `adhar up` to resume "+
		"the wait, or inspect `adhar get status`", detail)
}
