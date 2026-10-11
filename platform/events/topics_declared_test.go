package events

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Go taxonomy and the KafkaTopic objects are two halves of one contract, and
// the failure mode of a mismatch is SILENT: the shared cluster has
// `auto.create.topics.enable: true`, so publishing to a topic nobody declared
// succeeds and the broker creates it with one partition and the broker's default
// retention — not the retention these streams were given. Nothing errors; the
// events are simply kept for the wrong length of time on the wrong number of
// partitions, which is only noticed when someone needs the history.
func TestTheEventTopicsAreDeclaredInTheStack(t *testing.T) {
	path := filepath.Join("..", "stack", "packages", "data", "kafka-operator", "manifests", "events-bus.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the event bus manifests: %v", err)
	}
	declared := map[string]map[string]interface{}{}
	var bridges int
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	for {
		var doc map[string]interface{}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil {
			continue
		}
		meta, _ := doc["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		switch doc["kind"] {
		case "KafkaTopic":
			spec, _ := doc["spec"].(map[string]interface{})
			declared[name] = spec
		case "KafkaBridge":
			bridges++
		}
	}
	for _, topic := range Topics {
		spec, ok := declared[topic]
		if !ok {
			var have []string
			for n := range declared {
				have = append(have, n)
			}
			sort.Strings(have)
			t.Errorf("the taxonomy publishes to %q but no KafkaTopic declares it (declared: %s); "+
				"the broker would auto-create it with one partition and the wrong retention",
				topic, strings.Join(have, ", "))
			continue
		}
		// Retention must be SET. A topic that inherits the broker default is a
		// stream whose history length nobody chose.
		cfg, _ := spec["config"].(map[string]interface{})
		if cfg == nil || cfg["retention.ms"] == nil {
			t.Errorf("KafkaTopic %q sets no retention.ms, so its history length is the broker's default", topic)
		}
		if p, ok := spec["partitions"].(int); ok && p < 1 {
			t.Errorf("KafkaTopic %q declares %d partitions", topic, p)
		}
	}
	// And nothing declared that the taxonomy never writes to: a topic with no
	// producer is a thing to maintain, back up and explain for no reason.
	for name := range declared {
		var used bool
		for _, topic := range Topics {
			if topic == name {
				used = true
			}
		}
		if !used {
			t.Errorf("KafkaTopic %q is declared but the taxonomy never publishes to it", name)
		}
	}
	// The bridge is the only way any of this reaches Kafka.
	if bridges != 1 {
		t.Errorf("found %d KafkaBridge(s); the bus needs exactly one", bridges)
	}
}

// The bridge must not declare a CORS block.
//
// Strimzi renders ANY cors block as `http.cors.enabled=true`, so an empty
// allow-list is not "deny everything": the bridge builds its router with no
// permitted origin or method and exits 0 before serving. On 2026-10-11 that
// cost five minutes of operator timeout per attempt, with a container that
// reported "Completed" and a log that simply stopped — no error anywhere. No
// browser talks to the bridge (the Console publishes from its server side), so
// the correct expression of that is no block at all.
func TestTheBridgeDeclaresNoCorsBlock(t *testing.T) {
	path := filepath.Join("..", "stack", "packages", "data", "kafka-operator", "manifests", "events-bus.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the event bus manifests: %v", err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	for {
		var doc map[string]interface{}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil || doc["kind"] != "KafkaBridge" {
			continue
		}
		spec, _ := doc["spec"].(map[string]interface{})
		httpCfg, _ := spec["http"].(map[string]interface{})
		if _, has := httpCfg["cors"]; has {
			t.Error("KafkaBridge declares spec.http.cors; any cors block enables CORS, and an " +
				"empty allow-list makes the bridge exit 0 before it serves")
		}
	}
}
