package adharplatform

import "testing"

// `adhar up` hands over once the ENTRY POINT is serving, not once every
// application is. Waiting for all of them was the wrong bar: the catalogue is
// large (75 applications on a cloud profile), the tail is long, and the count
// does not even rise monotonically — an observed run went 26/75 and then back to
// 20/75 as later waves restarted things. The person who ran the command is
// waiting for a console to open.
func TestUsableGatesOnTheEntryPointNotTheWholeCatalogue(t *testing.T) {
	tests := []struct {
		name   string
		report ConvergenceReport
		want   bool
		why    string
	}{
		{
			name:   "console ready with a long tail still converging",
			report: ConvergenceReport{Total: 75, Healthy: 26, GatePresent: true, GateReady: true},
			want:   true,
			why:    "the console is what the command promised; the rest converges behind it",
		},
		{
			name:   "console not ready yet, even with most apps healthy",
			report: ConvergenceReport{Total: 75, Healthy: 74, GatePresent: true, GateReady: false},
			want:   false,
			why:    "handing over without an entry point gives the user nothing to open",
		},
		{
			// A profile that does not deploy the console has nothing to gate on, so
			// the old bar applies rather than exiting immediately.
			name:   "gate absent falls back to full convergence",
			report: ConvergenceReport{Total: 10, Healthy: 9, GatePresent: false},
			want:   false,
			why:    "no entry point to wait for means wait for everything",
		},
		{
			name:   "gate absent and everything healthy",
			report: ConvergenceReport{Total: 10, Healthy: 10, GatePresent: false},
			want:   true,
			why:    "full convergence still satisfies the fallback",
		},
		{
			// Total 0 means the ApplicationSet has not produced anything yet. Neither
			// bar is met, so keep waiting instead of declaring an empty platform ready.
			name:   "no applications yet is not ready",
			report: ConvergenceReport{Total: 0, Healthy: 0, GatePresent: false},
			want:   false,
			why:    "an empty ArgoCD is a platform that has not started, not one that is done",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.report.Usable(); got != tc.want {
				t.Errorf("Usable() = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// Converged stays strict: it is what distinguishes "everything is up" from
// "usable", and the two log different things.
func TestConvergedStillMeansEveryApplication(t *testing.T) {
	if (ConvergenceReport{Total: 75, Healthy: 26, GatePresent: true, GateReady: true}).Converged() {
		t.Error("26/75 must not count as converged just because the console is ready")
	}
	if !(ConvergenceReport{Total: 75, Healthy: 75}).Converged() {
		t.Error("75/75 is converged")
	}
	if (ConvergenceReport{Total: 0, Healthy: 0}).Converged() {
		t.Error("an empty application list is not convergence")
	}
}
