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

import "strings"

// The event taxonomy: `io.adhar.<domain>.<object>.<verb>`.
//
// Three parts, in that order, because that is the order a consumer narrows by —
// first the domain it cares about, then the object, then what happened to it.
// A flat namespace (`io.adhar.nodeadded`) cannot be subscribed to by domain,
// and a deeper one buys nothing.
//
// Verbs are PAST TENSE and describe a fact, never an intention. `created`, not
// `create`: an event is a record of something that already happened, and a bus
// carrying commands is an API with no error channel.
const (
	// Platform and component lifecycle — what `adhar up` and the AdharPlatform
	// controller do.
	TypePlatformReconciled = "io.adhar.platform.platform.reconciled"
	TypePlatformFailed     = "io.adhar.platform.platform.failed"
	TypeComponentReady     = "io.adhar.platform.component.ready"
	TypeComponentFailed    = "io.adhar.platform.component.failed"

	// Cluster shape — nodes, scaling, and the fleet.
	TypeNodeAdded           = "io.adhar.cluster.node.added"
	TypeNodeRemoved         = "io.adhar.cluster.node.removed"
	TypeScaleRefused        = "io.adhar.cluster.scale.refused"
	TypeDataPlaneRegistered = "io.adhar.cluster.dataplane.registered"
	TypeDataPlaneRemoved    = "io.adhar.cluster.dataplane.removed"

	// Tenancy — the ADR-0026 hierarchy.
	TypeOrganisationCreated = "io.adhar.tenancy.organisation.created"
	TypeTeamCreated         = "io.adhar.tenancy.team.created"
	TypeProjectCreated      = "io.adhar.tenancy.project.created"

	// Delivery — GitOps, packages and the paved road.
	TypeRepositoryPushed   = "io.adhar.delivery.repository.pushed"
	TypePackageSynced      = "io.adhar.delivery.package.synced"
	TypePackageDegraded    = "io.adhar.delivery.package.degraded"
	TypeCustomPackageReady = "io.adhar.delivery.custompackage.ready"
	TypePipelineSucceeded  = "io.adhar.delivery.pipeline.succeeded"
	TypePipelineFailed     = "io.adhar.delivery.pipeline.failed"

	// The agent runtime.
	TypeAgentRunStarted  = "io.adhar.ai.run.started"
	TypeAgentRunFinished = "io.adhar.ai.run.finished"
	TypeAgentRunRefused  = "io.adhar.ai.run.refused"
)

// The topics. One per DOMAIN, not one per type: a consumer interested in
// cluster shape wants every node event without enumerating them, and Kafka
// partitions are the unit of ordering — splitting one domain across topics
// throws away the ordering between `node.added` and `node.removed` that makes
// the stream readable.
const (
	TopicPlatform = "adhar.platform"
	TopicCluster  = "adhar.cluster"
	TopicTenancy  = "adhar.tenancy"
	TopicDelivery = "adhar.delivery"
	TopicAI       = "adhar.ai"
)

// Topics is every topic the platform publishes to, which the manifests must
// declare. Enumerated here so the Go taxonomy and the KafkaTopic objects cannot
// drift: a type routed to a topic that does not exist is accepted by the bridge
// and then silently auto-created with the broker's defaults (one partition, the
// broker's retention), which is not what any of these want.
var Topics = []string{TopicPlatform, TopicCluster, TopicTenancy, TopicDelivery, TopicAI}

// TopicFor routes an event type to its topic by DOMAIN — the second segment of
// the type. Deriving it rather than keeping a map means a new event type is
// routed correctly the moment it is named, and a type in an unknown domain is
// reported rather than quietly dropped.
func TopicFor(eventType string) (string, bool) {
	rest, ok := strings.CutPrefix(eventType, TypePrefix)
	if !ok {
		return "", false
	}
	domain, _, ok := strings.Cut(rest, ".")
	if !ok || domain == "" {
		return "", false
	}
	topic := "adhar." + domain
	for _, known := range Topics {
		if known == topic {
			return topic, true
		}
	}
	return "", false
}
