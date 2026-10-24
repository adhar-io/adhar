package controller

import (
	"testing"
	"time"
)

// A manager that cannot renew its lease exits, and on a loaded control plane
// the controller-runtime default (10s) was lost mid-scale-up, orphaning a
// worker instance. Keep the deadline generous and the timings consistent.
func TestLeaderElectionToleratesASlowAPIServer(t *testing.T) {
	if renewDeadline < 30*time.Second {
		t.Errorf("renewDeadline %s: a loaded API server answers renewals in seconds; keep it >= 30s", renewDeadline)
	}
	if !(leaseDuration > renewDeadline && renewDeadline > retryPeriod) {
		t.Errorf("need leaseDuration > renewDeadline > retryPeriod, got %s / %s / %s", leaseDuration, renewDeadline, retryPeriod)
	}
}
