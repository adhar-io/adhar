package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAutoscalingWithDefaults(t *testing.T) {
	// A nil spec must still yield usable values: the reconciler reads the
	// defaulted form for platforms created before the field existed.
	d := (*AutoscalingSpec)(nil).WithDefaults()
	if d.NodeGroup != DefaultAutoscalingNodeGroup {
		t.Fatalf("node group: %q", d.NodeGroup)
	}
	if d.MinWorkers != DefaultMinWorkers || d.MaxWorkers != DefaultMaxWorkers {
		t.Fatalf("bounds: %d..%d", d.MinWorkers, d.MaxWorkers)
	}
	if d.ScaleDownDelay != DefaultScaleDownDelay || d.ScaleUpCooldown != DefaultScaleUpCooldown {
		t.Fatalf("durations: %v / %v", d.ScaleDownDelay, d.ScaleUpCooldown)
	}

	// Explicit values survive, and an inverted range is clamped rather than
	// left to oscillate.
	s := &AutoscalingSpec{MinWorkers: 3, MaxWorkers: 2, ScaleUpCooldown: metav1.Duration{Duration: 90 * time.Second}}
	got := s.WithDefaults()
	if got.MinWorkers != 3 || got.MaxWorkers != 3 {
		t.Fatalf("expected max clamped up to the min, got %d..%d", got.MinWorkers, got.MaxWorkers)
	}
	if got.ScaleUpCooldown.Duration != 90*time.Second {
		t.Fatalf("explicit cooldown lost: %v", got.ScaleUpCooldown)
	}
}

func TestScaleDownThreshold(t *testing.T) {
	cases := map[string]float64{
		"50%":    0.5,
		"  35% ": 0.35,
		"0.25":   0.25,
		"100%":   1,
		"150%":   1,   // clamped: a threshold above full capacity is meaningless
		"":       0.5, // unset falls back rather than disabling scale-down
		"lots":   0.5, // a typo must not silently change behaviour
		"-10%":   0.5,
	}
	for in, want := range cases {
		got := AutoscalingSpec{ScaleDownUtilizationThreshold: in}.ScaleDownThreshold()
		if got != want {
			t.Errorf("%q: want %v, got %v", in, want, got)
		}
	}
}
