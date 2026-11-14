package batch

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

// A1.7 runs on the local profile "with no Kafka, Kargo, Alloy or Volcano": the
// suite only proves anything about that profile while the packages it needs
// are on and those four are off in the ApplicationSet `adhar up` applies.
var (
	// LocalBatchNeeds is the chain from scaffold to run: the batch package, the
	// build (Tekton, kpack, Harbor, the supply chain) and the JobRepository's Postgres.
	LocalBatchNeeds = []string{"adhar-batch", "buildpack", "tekton", "harbor", "adhar-supply-chain", "cnpg"}
	// LocalBatchWithout is what Phase 1 must not depend on locally.
	LocalBatchWithout = []string{"kafka-operator", "kargo", "alloy", "volcano"}
)

// LocalProfileProblems reads an ApplicationSet and says how it departs from that.
func LocalProfileProblems(appset []byte) ([]string, error) {
	var doc struct {
		Spec struct {
			Generators []struct {
				List struct {
					Elements []struct {
						Name    string `json:"name"`
						Enabled string `json:"enabled"`
					} `json:"elements"`
				} `json:"list"`
			} `json:"generators"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(appset, &doc); err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	declared := map[string]bool{}
	for _, g := range doc.Spec.Generators {
		for _, e := range g.List.Elements {
			declared[e.Name] = true
			enabled[e.Name] = enabled[e.Name] || e.Enabled == "true"
		}
	}
	if len(declared) == 0 {
		return nil, fmt.Errorf("no list elements: not the platform ApplicationSet")
	}
	var problems []string
	for _, n := range LocalBatchNeeds {
		if !enabled[n] {
			problems = append(problems, n+" is not enabled, and the batch chain needs it")
		}
	}
	for _, n := range LocalBatchWithout {
		if enabled[n] {
			problems = append(problems, n+" is enabled, so the suite no longer proves Phase 1 runs without it (A1.7)")
		}
	}
	return problems, nil
}
