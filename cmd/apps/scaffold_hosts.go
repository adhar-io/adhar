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

package apps

import (
	"encoding/json"
	"regexp"
	"strings"
)

// The hostnames a scaffolded application's HTTPRoute serves.
//
// Every app gets its platform route by default — <name>.<environment>.<base> —
// because the environment is part of the address: `my-service.<base>` cannot
// be the dev copy and the prod copy at once, and the promotion chain this
// platform is built around (dev → test → prod) needs one hostname per
// environment. The Gateway terminates TLS for `*.<env>.<base>` precisely so
// this resolves and serves the right certificate on a fresh scaffold.
//
// On top of that, the template's `customHostnames` parameter adds any
// hostnames the team wants. They are appended, never substituted: the platform
// route stays first, so the console's links keep working. A custom hostname
// outside the Gateway's listeners is the team's to wire (listener +
// certificate); the route still lists it. The Console's scaffolder applies the
// same rules (apps/console/app/server/scaffold-hosts.ts).

var hostnameRe = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// platformRouteHost is <name>.<env>.<base>, the base with no port.
func platformRouteHost(name, environment, baseDomain string) string {
	base := strings.Trim(stripPort(baseDomain), ".")
	return name + "." + environment + "." + base
}

func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i+1:], ".") {
		return host[:i]
	}
	return host
}

// parseCustomHostnames reads a comma/space/newline separated list, lower-cases,
// strips schemes and paths, drops anything that is not a hostname, de-duplicates.
func parseCustomHostnames(raw string) []string {
	var out []string
	for _, p := range regexp.MustCompile(`[\s,]+`).Split(raw, -1) {
		h := strings.ToLower(strings.TrimSpace(p))
		h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
		if i := strings.Index(h, "/"); i >= 0 {
			h = h[:i]
		}
		if h == "" || !hostnameRe.MatchString(h) {
			continue
		}
		if !contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// routeHostnames is the ordered list the route serves: the platform route first,
// then the custom ones.
func routeHostnames(platformHost, custom string) []string {
	list := []string{strings.ToLower(platformHost)}
	for _, h := range parseCustomHostnames(custom) {
		if !contains(list, h) {
			list = append(list, h)
		}
	}
	return list
}

// routeHostnamesJSON is what the templates substitute into
// `hostnames: ${{ values.hostnamesJson }}` — a JSON list is a YAML flow sequence.
func routeHostnamesJSON(platformHost, custom string) string {
	b, _ := json.Marshal(routeHostnames(platformHost, custom))
	return string(b)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// envHostname moves a platform route from one environment to another:
// `app.dev.base` → `app.test.base`. A hostname that does not carry the
// current environment label (a template's local fallback) is returned as is.
func envHostname(hostname, currentEnv, targetEnv string) string {
	marker := "." + currentEnv + "."
	if i := strings.Index(hostname, marker); i > 0 {
		return hostname[:i] + "." + targetEnv + "." + hostname[i+len(marker):]
	}
	return hostname
}
