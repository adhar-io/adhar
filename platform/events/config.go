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
	"fmt"
	"os"
	"strings"
)

// EnvBridgeURL names the KafkaBridge the platform publishes through.
//
// Opt-IN, deliberately. Deriving the adhar-controlplane bridge address automatically
// would turn every local `adhar up` — where Kafka is not enabled — into a
// controller retrying HTTP against a Service that does not exist, once per
// reconcile, forever. A platform with no bus must be silent about not having
// one, so the switch is an environment variable set by the manifest that also
// runs the bridge.
const EnvBridgeURL = "ADHAR_EVENTS_BRIDGE_URL"

// EnvSource overrides the CloudEvents `source` of this process. Normally
// derived from the cluster and component names instead.
const EnvSource = "ADHAR_EVENTS_SOURCE"

// DefaultBridgeURL is the adhar-controlplane address of the bridge the kafka-operator
// package declares (`KafkaBridge/adhar-events`). Strimzi names the Service
// `<bridge>-bridge-service`, which is not a name anyone would guess, so it is
// written down once here for the manifest that sets EnvBridgeURL to copy.
const DefaultBridgeURL = "http://adhar-events-bridge-service.adhar-system.svc.cluster.local:8080"

// SourceFor builds a CloudEvents `source` URI for a component.
//
// A `source` must identify the EMITTER uniquely, because (source, id) is what
// makes an event unique. Including the cluster means two Adhar platforms
// publishing into one aggregated stream — a fleet hub reading its members —
// cannot collide.
func SourceFor(cluster, component string) string {
	cluster = strings.TrimSpace(cluster)
	if cluster == "" {
		cluster = "unknown"
	}
	return fmt.Sprintf("/adhar/%s/%s", cluster, component)
}

// FromEnv builds the Options for a component from the environment.
//
// Never returns an error: a missing or malformed bus configuration must leave
// the platform working, and the resulting publisher is simply disabled.
func FromEnv(cluster, component string) Options {
	return Options{
		BridgeURL: strings.TrimRight(strings.TrimSpace(os.Getenv(EnvBridgeURL)), "/"),
		Source:    firstNonEmpty(os.Getenv(EnvSource), SourceFor(cluster, component)),
	}
}

// NewFromEnv is the one call a component makes to join the bus.
func NewFromEnv(cluster, component string) *Publisher {
	return NewPublisher(FromEnv(cluster, component))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
