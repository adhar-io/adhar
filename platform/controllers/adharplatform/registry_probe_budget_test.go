package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The registries every pipeline reads through need a probe budget that survives
// a hiccup, because when they flap the damage appears somewhere else entirely.
//
// harbor-core is the registry's front door: every buildpack build and every
// image scan goes through it. It shipped liveness and readiness at
// failureThreshold 2 on a 10s period, so two consecutive misses emptied the
// Service endpoints and restarted it. On 2026-10-13 a worker's kubelet stopped
// posting node status and that alone made core flap for half an hour — during
// which every build died at the CNB ANALYZING phase with
// `connect: connection refused` and every scan with `unable to find the
// specified image`. Neither message mentions Harbor, which is why it reads as a
// broken pipeline.
//
// 30s is the floor, not a target: the platform had already raised Harbor's
// DATABASE for the same reason (its upstream budget let kubelet kill a healthy
// database 29 times), and Nexus sits at 120s/900s. Scoped to these services
// deliberately — 75 of the 403 probes in the stack are under 30s and most are
// harmless, so this pins the ones whose unavailability stops all CI.
func TestPipelineCriticalRegistriesTolerateAHiccup(t *testing.T) {
	const floorSeconds = 30

	// name -> the package manifest that defines it.
	critical := map[string]string{
		"harbor-core":     filepath.Join("application", "harbor", "manifests", "install.yaml"),
		"harbor-registry": filepath.Join("application", "harbor", "manifests", "install.yaml"),
		"nexus":           filepath.Join("application", "nexus", "manifests", "install.yaml"),
	}

	type probe struct {
		PeriodSeconds    int `yaml:"periodSeconds"`
		FailureThreshold int `yaml:"failureThreshold"`
		TimeoutSeconds   int `yaml:"timeoutSeconds"`
	}
	type container struct {
		Name           string `yaml:"name"`
		LivenessProbe  *probe `yaml:"livenessProbe"`
		ReadinessProbe *probe `yaml:"readinessProbe"`
	}
	type workload struct {
		Kind     string                `yaml:"kind"`
		Metadata struct{ Name string } `yaml:"metadata"`
		Spec     struct {
			Template struct {
				Spec struct {
					Containers []container `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}

	checked := 0
	for name, rel := range critical {
		b, err := os.ReadFile(filepath.Join(stackRoot(t), "packages", rel))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, doc := range strings.Split(string(b), "\n---") {
			var w workload
			if yaml.Unmarshal([]byte(doc), &w) != nil {
				continue
			}
			if w.Metadata.Name != name || (w.Kind != "Deployment" && w.Kind != "StatefulSet") {
				continue
			}
			for _, c := range w.Spec.Template.Spec.Containers {
				for label, p := range map[string]*probe{"liveness": c.LivenessProbe, "readiness": c.ReadinessProbe} {
					if p == nil {
						continue
					}
					period, threshold := p.PeriodSeconds, p.FailureThreshold
					if period == 0 {
						period = 10
					}
					if threshold == 0 {
						threshold = 3
					}
					checked++
					if budget := period * threshold; budget < floorSeconds {
						t.Errorf("%s/%s %s probe allows only %ds (period %d x threshold %d); at least %ds "+
							"is required, because a flap here empties the Service endpoints and every "+
							"build and scan on the platform fails with an error that never mentions this "+
							"service", name, c.Name, label, budget, period, threshold, floorSeconds)
					}
				}
			}
		}
	}
	if checked < 4 {
		t.Fatalf("examined only %d probes across the critical registries; this guard is no longer "+
			"finding them", checked)
	}
}
