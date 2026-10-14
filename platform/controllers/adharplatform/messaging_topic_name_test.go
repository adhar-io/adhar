package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A Messaging topic without a name must be refused, not composed.
//
// The composition names each KafkaTopic `{{ .name }}` and tags it
// `composition-resource-name: topic-{{ .name }}`. With the name missing both
// render as `<no value>`: the apiserver rejects the object, Crossplane marks the
// composite unready, and — because one failed composed resource fails the
// whole XR — the Kafka cluster the application actually depends on is never
// created either. Live on 2026-10-14 `Messaging/my-service-events` sat with no
// Kafka cluster at all, and the pod that needed it read
// `Topic events not present in metadata` for an hour, which points at the
// broker and not at a one-line typo in `deploy/kafka.yaml`.
//
// Two layers: the XRD requires the field (an unnamed topic is rejected at
// admission), and the composition fails by name (for a cluster whose XRD
// predates the requirement).
func TestAMessagingTopicWithoutANameIsRefused(t *testing.T) {
	xrd := readConfigurationFile(t, "xrd", "messaging.xrd.yaml")
	items := topicItemsSchema(t, xrd)
	if !strings.Contains(items, "required: [name]") {
		t.Error("the Messaging XRD's spec.topics items do not require `name`; an unnamed topic " +
			"is accepted at admission and fails the whole XR later")
	}
	if !regexp.MustCompile(`name:\s*\{[^}]*minLength:\s*1`).MatchString(items) {
		t.Error("the Messaging XRD accepts `name: \"\"` for a topic; it needs minLength: 1")
	}

	comp := readConfigurationFile(t, "compositions", "messaging", "local-strimzi.yaml")
	loop := strings.Index(comp, `{{- range dig "topics" (list) $spec }}`)
	if loop < 0 {
		t.Fatal("the Strimzi composition no longer ranges over spec.topics; update this guard")
	}
	kind := strings.Index(comp[loop:], "kind: KafkaTopic")
	if kind < 0 {
		t.Fatal("no KafkaTopic is composed inside the topics loop")
	}
	// The refusal must sit between the loop head and the object it protects.
	between := comp[loop : loop+kind]
	if !strings.Contains(between, `{{- if not (dig "name" "" .) }}`) || !strings.Contains(between, "{{- fail ") {
		t.Error("the Strimzi composition composes a KafkaTopic for an unnamed topic instead of " +
			"failing by name; the result is an object called `topic-<no value>` that fails " +
			"the whole Messaging")
	}
}

func topicItemsSchema(t *testing.T, xrd string) string {
	t.Helper()
	start := strings.Index(xrd, "topics:\n")
	if start < 0 {
		t.Fatal("the Messaging XRD has no spec.topics")
	}
	rest := xrd[start:]
	// The topics array ends where the next sibling at the same indent begins
	// or at the spec's `required:` list.
	end := strings.Index(rest, "\n              required:")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func readConfigurationFile(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{"..", "..", "controlplane", "configuration"}, parts...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return string(b)
}

// Every composed object with no readiness of its own must say so, or the
// Messaging never becomes Ready.
//
// function-auto-ready marks the composite Ready when every composed resource
// carries `Ready=True`. A ConfigMap and a PodMonitor have no status at all,
// and Strimzi writes no conditions on a KafkaNodePool — so with a broker
// running and the Kafka CR Ready=True, the XR still reported
//
//	Ready=False Creating: Unready resources: kafka-podmonitor, kafka-pool
//
// and Argo CD, whose health check for platform.adhar.io kinds reads that
// condition, held `my-service` Degraded (2026-10-14). The same carve-out
// already exists for the CNPG ScheduledBackup in the database composition.
func TestMessagingMarksItsStatuslessResourcesReady(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "messaging", "local-strimzi.yaml")
	for _, name := range []string{"kafka-metrics-cm", "kafka-pool", "kafka-podmonitor"} {
		marker := "composition-resource-name: " + name + "\n"
		i := strings.Index(comp, marker)
		if i < 0 {
			t.Fatalf("composed resource %q is gone from the Strimzi composition; update this guard", name)
		}
		block := comp[i+len(marker):]
		// The annotation belongs to this object's metadata, before its spec.
		if j := strings.Index(block, "\n            spec:"); j >= 0 {
			block = block[:j]
		}
		if !strings.Contains(block, `gotemplating.fn.crossplane.io/ready: "True"`) {
			t.Errorf("%s has no Ready condition of its own and is not annotated ready; "+
				"function-auto-ready will hold the Messaging unready forever", name)
		}
	}
}
