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

import "time"

// cloudDefaultAppsTimeout is how long a CLOUD bring-up keeps driving the apps
// when `--apps-timeout` is not given.
//
// The flag's default (15m) was written for the local core of 15 apps. A cloud
// profile has ~77, and its critical path is the identity chain — CNPG → the
// keycloak-db cluster → Keycloak's JVM → the client Job writes
// `keycloak-clients` → every consumer's ExternalSecret and oauth2-proxy,
// the console among them — which takes 20–25 minutes on a fresh cluster.
// With the strict completion verdict (`adhar up` succeeds only once the
// console is Healthy and serving), a 15-minute budget turned a healthy but
// unfinished bring-up into a reported failure (DigitalOcean, 2026-10-16:
// Keycloak went Healthy two minutes after the budget expired, and the console
// three minutes after that). The budget must cover the chain; an operator who
// wants a shorter or longer wait still says so with the flag.
const cloudDefaultAppsTimeout = 45 * time.Minute

// effectiveAppsTimeout is the apps budget a bring-up runs with: the flag when
// the operator set it (including 0, "exit when the foundation is ready"),
// otherwise the default appropriate to where the cluster runs.
func effectiveAppsTimeout(flag time.Duration, explicit, cloud bool) time.Duration {
	if explicit || !cloud {
		return flag
	}
	return cloudDefaultAppsTimeout
}
