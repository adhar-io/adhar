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
// The flag's default (localDefaultAppsTimeout, below) is the LOCAL one. A cloud
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

// localDefaultAppsTimeout is the flag's default, which is what a LOCAL
// bring-up runs with (cloud bring-ups use cloudDefaultAppsTimeout unless the
// flag is given). 15m was sized for the 15-app local core; the local profile
// now runs 21 apps including the delivery chain (Nexus, Kargo, the library
// releases), and on a laptop its identity chain alone — Keycloak Ready, then
// the console — measured 21 and 56 minutes on 2026-11-22. A budget shorter
// than the chain reports a converging platform as a failed `adhar up`.
const localDefaultAppsTimeout = 30 * time.Minute

// effectiveAppsTimeout is the apps budget a bring-up runs with: the flag when
// the operator set it (including 0, "exit when the foundation is ready"),
// otherwise the default appropriate to where the cluster runs.
func effectiveAppsTimeout(flag time.Duration, explicit, cloud bool) time.Duration {
	if explicit || !cloud {
		return flag
	}
	return cloudDefaultAppsTimeout
}
