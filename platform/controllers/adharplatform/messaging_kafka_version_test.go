package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A Messaging must default to a Kafka version the platform's Strimzi can run.
//
// The composition emits `spec.kafka.version` from `kafkaVersion`, whose default
// lived in two places (the XRD and the composition) and in both read 3.8.0 —
// a version the operator this platform installs stopped supporting several
// releases ago. Strimzi's response is a Kafka CR stuck at
//
//	NotReady UnsupportedKafkaVersionException: Unsupported Kafka.spec.kafka.version: 3.8.0
//
// with no pods and no bootstrap Service, so the application that asked for
// the stream exits on `DNS resolution failed for <name>-kafka-bootstrap`
// (my-service, 2026-10-14, after sixteen hours of CrashLoopBackOff). Nothing
// in the XR looked wrong — Synced=True, Ready=False "Creating".
//
// The shared `adhar-kafka` cluster in the kafka-operator package is pinned
// against the same operator and is live-verified on every bring-up, so it is
// the one version the defaults may use. Both defaults and the example are
// held to it.
func TestMessagingDefaultsToTheKafkaVersionTheOperatorRuns(t *testing.T) {
	shared := readStackFile(t, "data/kafka-operator/manifests/kafka-cluster.yaml")
	m := regexp.MustCompile(`(?m)^\s+version:\s*"?(\d+\.\d+\.\d+)"?\s*$`).FindStringSubmatch(shared)
	if m == nil {
		t.Fatal("the shared adhar-kafka cluster no longer pins spec.kafka.version; update this guard")
	}
	want := m[1]

	xrd := readConfigurationFile(t, "xrd", "messaging.xrd.yaml")
	xm := regexp.MustCompile(`kafkaVersion:\s*\n\s+type: string\s*\n\s+default:\s*"?([^"\s]+)"?`).FindStringSubmatch(xrd)
	if xm == nil {
		t.Fatal("the Messaging XRD has no kafkaVersion default")
	}
	if xm[1] != want {
		t.Errorf("Messaging XRD defaults kafkaVersion to %s, but the Strimzi this platform runs serves %s "+
			"(the shared adhar-kafka version); a Messaging created with the default composes a Kafka "+
			"the operator refuses", xm[1], want)
	}

	comp := readConfigurationFile(t, "compositions", "messaging", "local-strimzi.yaml")
	cm := regexp.MustCompile(`dig "kafkaVersion" "([^"]+)" \$spec`).FindStringSubmatch(comp)
	if cm == nil {
		t.Fatal("the Strimzi composition no longer defaults kafkaVersion with dig; update this guard")
	}
	if cm[1] != want {
		t.Errorf("Strimzi composition defaults kafkaVersion to %s, want %s (the shared adhar-kafka version)", cm[1], want)
	}

	example := readRepoFile(t, "examples", "messaging.yaml")
	if !strings.Contains(example, `kafkaVersion: "`+want+`"`) {
		t.Errorf("examples/messaging.yaml does not pin kafkaVersion %q; the example is what people copy", want)
	}
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return string(b)
}
