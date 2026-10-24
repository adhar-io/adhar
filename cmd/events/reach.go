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

package events

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"k8s.io/client-go/rest"
)

// bridgeService is the Service Strimzi creates for `KafkaBridge/adhar-events`.
// The `-bridge-service` suffix is Strimzi's, not ours, and it is not a name
// anyone guesses — which is why it is a constant rather than a format string
// built at three call sites.
const (
	bridgeService = "adhar-events-bridge-service"
	bridgePort    = "8080"
)

// portForwardBridge reaches the adhar-controlplane bridge.
//
// Through the API SERVER'S SERVICE PROXY, not an actual port-forward. The name
// is kept because that is what a reader expects to find, but there is no local
// listener, no goroutine to leak and no port to collide: every request is a
// normal authenticated call to
//
//	/api/v1/namespaces/<ns>/services/<svc>:<port>/proxy/<path>
//
// which the kubeconfig already authorises and which needs no second credential.
// A port-forward would also have to be torn down on every exit path, and a
// forward left behind outlives the command that made it.
//
// The returned cleanup exists so callers need not care which mechanism was
// used; today it is a no-op.
func portForwardBridge(ctx context.Context) (string, func(), error) {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return "", nil, unreachable(err)
	}
	// A client built from the kubeconfig: same CA, same credentials, same
	// proxy settings as every other call this CLI makes.
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return "", nil, unreachable(err)
	}
	base := fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s:%s/proxy",
		strings.TrimRight(cfg.Host, "/"), globals.AdharSystemNamespace, bridgeService, bridgePort)

	// Prove it answers before subscribing, so a cluster with no bus says so
	// once, clearly, instead of failing inside the consumer protocol with a
	// 404 from the API server that reads like a bridge error.
	if err := probe(ctx, httpClient, base); err != nil {
		return "", nil, err
	}
	installTransport(httpClient)
	return base, func() {}, nil
}

// probe checks the bridge is reachable, is a bridge, and can reach Kafka.
//
// `GET /topics`, NOT `/healthy`. The bridge serves its health and readiness
// endpoints on a SEPARATE management port (8081, `rest-api-mgmt`) that the
// Service does not expose; asking for `/healthy` on the REST port returns 501
// Not Implemented, so a probe written against it reports a broken bridge on a
// perfectly healthy one. `/topics` is on the REST port, and it is the better
// question anyway: it fails when the bridge cannot reach the BROKER, which is
// the failure an operator actually hits (a flapping broker answers the
// bridge's own health check just fine).
func probe(ctx context.Context, c *http.Client, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/topics", nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("the platform event bridge is not reachable: %w\n"+
			"  The bus is opt-in. Check that the kafka-operator package is enabled and that\n"+
			"  KafkaBridge/adhar-events is Ready: kubectl -n %s get kafkabridge adhar-events",
			err, globals.AdharSystemNamespace)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("no event bridge on this cluster (the API server has no Service %s/%s)\n"+
			"  The event bus is opt-in: enable the kafka-operator package, which declares\n"+
			"  the bridge and the five adhar.* topics",
			globals.AdharSystemNamespace, bridgeService)
	case resp.StatusCode >= 500:
		// The bridge is up; Kafka is not reachable from it. Distinguished
		// because the remedy is completely different — nothing is wrong with
		// the bus configuration, the broker is down or flapping.
		return fmt.Errorf("the event bridge is running but cannot reach Kafka (%s)\n"+
			"  Check the broker: kubectl -n %s get kafka,pod -l strimzi.io/cluster=adhar-kafka\n"+
			"  A broker that is restarting (CPU starvation shows up as exit 137) leaves the\n"+
			"  bridge with no node assignment and every publish failing on topic metadata",
			resp.Status, globals.AdharSystemNamespace)
	case resp.StatusCode >= 300:
		return fmt.Errorf("the event bridge answered %s on /topics", resp.Status)
	}
	return nil
}

// sharedTransport carries the kubeconfig's credentials into bridgeClient, which
// otherwise builds a plain http.Client that the API server would reject.
var sharedTransport *http.Client

func installTransport(c *http.Client) { sharedTransport = c }

// unreachable gives the same explanation every other command gives when the
// cluster cannot be reached, rather than a bare error from client-go.
func unreachable(err error) error {
	return fmt.Errorf("could not connect to the cluster: %w\n"+
		"  Is it running? Try `adhar up`, or check your kubeconfig context", err)
}
