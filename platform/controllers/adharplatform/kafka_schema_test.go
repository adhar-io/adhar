package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Broker compute belongs on the KafkaNodePool, not on `spec.kafka`.
//
// Strimzi's v1 Kafka schema declares `jvmOptions`, `livenessProbe` and
// `readinessProbe` under `spec.kafka`, but NOT `resources` — compute moved to
// the pool when KafkaNodePool became the unit of scaling. A field the schema
// does not declare is not ignored, it is rejected, and under ServerSideApply
// the whole Application sync fails to even build its patch:
//
//	failed to create typed patch object (adhar-system/adhar-kafka;
//	kafka.strimzi.io/v1, Kind=Kafka): .spec.kafka.resources: field not
//	declared in schema
//
// Live on 2026-10-14 that failed the kafka-operator sync 219 times, so the
// shared `adhar-kafka` cluster was never created — and every Kafka-using
// service failed to start, my-service dying in
// `internalKafkaListenerEndpointRegistry` with no broker to reach. The sync
// error names a patch, not a missing cluster, so it reads like an Argo CD
// fault rather than one invalid field.
func TestBrokerResourcesLiveOnTheNodePoolNotOnSpecKafka(t *testing.T) {
	body := kafkaClusterManifest(t)

	var pool, kafka map[string]any
	for _, chunk := range strings.Split(body, "\n---") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &doc); err != nil || doc == nil {
			continue
		}
		switch doc["kind"] {
		case "KafkaNodePool":
			pool = doc
		case "Kafka":
			kafka = doc
		}
	}
	if pool == nil || kafka == nil {
		t.Fatal("the kafka-cluster manifest no longer defines both a Kafka and a KafkaNodePool")
	}

	spec, _ := kafka["spec"].(map[string]any)
	kcfg, _ := spec["kafka"].(map[string]any)
	if _, bad := kcfg["resources"]; bad {
		t.Error("spec.kafka.resources is set, and the v1 Kafka schema does not declare it; " +
			"ServerSideApply cannot build a patch and the whole sync fails, so no Kafka " +
			"cluster is ever created")
	}

	// And it must actually be declared somewhere, or brokers get no CPU share
	// and lose the race against their own probe on a loaded node.
	pspec, _ := pool["spec"].(map[string]any)
	res, ok := pspec["resources"].(map[string]any)
	if !ok {
		t.Fatal("the KafkaNodePool declares no resources; a broker with no CPU request gets no " +
			"CFS share and fails its liveness probe on exactly the loaded nodes where boot " +
			"is slowest")
	}
	reqs, _ := res["requests"].(map[string]any)
	for _, k := range []string{"cpu", "memory"} {
		if reqs[k] == nil {
			t.Errorf("the KafkaNodePool resources declare no %s request", k)
		}
	}

	// The probe budget stays on spec.kafka, which DOES declare it — Strimzi has
	// no startupProbe, so liveness carries the boot budget.
	if _, ok := kcfg["livenessProbe"]; !ok {
		t.Error("spec.kafka.livenessProbe is gone; Strimzi exposes no startupProbe, so liveness " +
			"is what carries a booting broker's budget")
	}
}

func kafkaClusterManifest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t),
		"data/kafka-operator/manifests/kafka-cluster.yaml"))
	if err != nil {
		t.Fatalf("reading the kafka cluster manifest: %v", err)
	}
	return string(b)
}
