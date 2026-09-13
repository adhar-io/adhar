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

package auth

// discover.go finds the Keycloak endpoints of the cluster kubectl is pointed at.
//
// The --issuer / --admin-url defaults are the LOCAL Kind URLs
// (keycloak.adhar.localtest.me:8443). On any cloud platform those are simply
// wrong, and every auth subcommand failed in a way that read like an outage
// rather than a misconfiguration:
//
//	Error: no --admin-token supplied and client_credentials grant failed:
//	could not reach https://keycloak.adhar.localtest.me:8443/... x509:
//	certificate signed by unknown authority
//
// — on a platform whose Keycloak was healthy the whole time at
// keycloak.platform.adhar.io. Asking every user to pass --issuer and
// --admin-url on every command is not a fix; the CLI already knows which
// cluster it is talking to, so it can look the answer up.
//
// The AdharPlatform CR is the authority: `spec.buildCustomization` carries the
// host/port/protocol that every platform URL is templated from, so a URL derived
// here matches what the cluster actually serves. Discovery is best-effort and
// silent — offline, or pointed at a non-Adhar cluster, the flag defaults stand.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// discoveredKeycloak is the result of looking the platform up in the cluster.
type discoveredKeycloak struct {
	Issuer   string // https://keycloak.<host>[:<port>]/realms/<realm>
	AdminURL string // https://keycloak.<host>[:<port>]
}

// discoverKeycloak derives the Keycloak endpoints from the AdharPlatform CR in
// the cluster the current kubeconfig points at. It returns ok=false for any
// reason at all — no cluster, no CR, missing fields — because failing to
// discover must never be worse than the previous hardcoded behaviour.
func discoverKeycloak(realm string) (discoveredKeycloak, bool) {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return discoveredKeycloak{}, false
	}
	c, err := helpers.GetKubeClient(cfg)
	if err != nil {
		return discoveredKeycloak{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "platform.adhar.io",
		Version: "v1alpha1",
		Kind:    "AdharPlatformList",
	})
	if err := c.List(ctx, list, client.InNamespace(globals.AdharSystemNamespace)); err != nil || len(list.Items) == 0 {
		return discoveredKeycloak{}, false
	}

	bc, found, err := unstructured.NestedMap(list.Items[0].Object, "spec", "buildCustomization")
	if err != nil || !found {
		return discoveredKeycloak{}, false
	}

	host, _ := bc["host"].(string)
	if host == "" {
		return discoveredKeycloak{}, false
	}
	protocol, _ := bc["protocol"].(string)
	if protocol == "" {
		protocol = "https"
	}

	// The port suffix is omitted for the scheme default, matching the
	// `{{ .Host }}{{ .PortSuffix }}` convention the stack manifests template —
	// so the URL built here is byte-identical to the one Keycloak advertises as
	// its issuer. A mismatch there would make tokens fail validation.
	suffix := ""
	switch p := bc["port"].(type) {
	case int64:
		if !(p == 443 && protocol == "https") && !(p == 80 && protocol == "http") && p != 0 {
			suffix = fmt.Sprintf(":%d", p)
		}
	case float64:
		n := int64(p)
		if !(n == 443 && protocol == "https") && !(n == 80 && protocol == "http") && n != 0 {
			suffix = fmt.Sprintf(":%d", n)
		}
	}

	base := fmt.Sprintf("%s://keycloak.%s%s", protocol, strings.Trim(host, "/"), suffix)
	return discoveredKeycloak{
		Issuer:   base + "/realms/" + realm,
		AdminURL: base,
	}, true
}
