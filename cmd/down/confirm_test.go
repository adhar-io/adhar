package down

import (
	"strings"
	"testing"

	"adhar-io/adhar/globals"
)

// The panel guards an irreversible, billable operation, so it has to say WHAT,
// WHERE and under WHICH NAME. Each of these was missing at some point and each
// omission is a way to delete the wrong thing.
func TestConfirmationNamesWhatWhereAndWhichName(t *testing.T) {
	plan := buildTeardownPlan("../../examples/digitalocean-config.yaml", "", "")

	// LoadConfig alone leaves ResolvedEnvironments empty, which made the panel say
	// "every environment in the file" and print no cluster at all.
	if len(plan.Environments) == 0 {
		t.Fatal("environments were not resolved; the panel cannot name its scope")
	}
	if len(plan.Clusters) == 0 {
		t.Fatal("no cluster names resolved; the panel cannot say what it will look for")
	}
	if !plan.CloudResources {
		t.Error("a digitalocean environment must count as cloud resources")
	}

	out := RenderTeardownConfirmation(plan)
	for _, want := range []string{"digitalocean", "dev", globals.DefaultClusterName, "cannot be undone"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel is missing %q:\n%s", want, out)
		}
	}
}

// A local teardown must not borrow the cloud's language: it destroys containers,
// not volumes anybody paid for, and overstating it teaches the reader to skim.
func TestLocalTeardownDoesNotClaimCloudDataLoss(t *testing.T) {
	plan := buildTeardownPlan("../../examples/config.yaml", "", "")
	if plan.CloudResources {
		t.Fatalf("the shipped template defaults to kind; got cloud resources")
	}
	out := RenderTeardownConfirmation(plan)
	if strings.Contains(out, "cloud infrastructure") {
		t.Errorf("a local teardown must not announce cloud infrastructure:\n%s", out)
	}
	if strings.Contains(out, "Volume data is destroyed") {
		t.Errorf("a local teardown has no paid volumes to warn about:\n%s", out)
	}
}

// --name must be reflected, because it is what the teardown actually searches for.
func TestConfirmationReflectsTheNameOverride(t *testing.T) {
	out := RenderTeardownConfirmation(buildTeardownPlan("../../examples/aws-config.yaml", "dev", "my-cluster"))
	if !strings.Contains(out, "my-cluster") {
		t.Errorf("panel must name the --name override:\n%s", out)
	}
}

// The scope names the environments rather than only counting them: "EVERY
// environment in <file>" told the operator the one thing they already knew.
func TestScopeLineNamesEnvironments(t *testing.T) {
	tests := []struct {
		name string
		plan teardownPlan
		want string
	}{
		{"single named", teardownPlan{Environments: []string{"dev"}}, "dev"},
		{"all, one env", teardownPlan{Environments: []string{"dev"}, AllEnvironments: true}, "every environment — dev"},
		{"all, several", teardownPlan{Environments: []string{"dev", "prod"}, AllEnvironments: true}, "every environment (2) — dev, prod"},
		{"unreadable config", teardownPlan{AllEnvironments: true}, "every environment in the file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.plan.scopeLine(); got != tc.want {
				t.Errorf("scopeLine() = %q, want %q", got, tc.want)
			}
		})
	}
}
