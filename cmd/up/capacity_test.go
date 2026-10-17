package up

import (
	"os"
	"strings"
	"testing"
	"time"
)

func readBootstrapSource() (string, error) {
	b, err := os.ReadFile("bootstrap.go")
	return string(b), err
}

// A cluster that starts too small for the profile must say so, with the time
// the autoscaler will take; a cluster that fits says nothing.
func TestCapacityAdviceNamesTheShortfallAndTheWait(t *testing.T) {
	// Two Standard_E4bds_v5 workers (3.5 vCPU allocatable) against ~18 vCPU of requests.
	got := capacityAdvice(2, 3500, productionProfileRequestCPUMilli, 8, 3*time.Minute, 4)
	for _, want := range []string{"requests ~18 vCPU", "2 workers offer 7.0", "add ~5 workers", "nodeCount=7"} {
		if !strings.Contains(got, want) {
			t.Errorf("advice %q lacks %q", got, want)
		}
	}
	if got := capacityAdvice(8, 7860, productionProfileRequestCPUMilli, 8, 3*time.Minute, 4); got != "" {
		t.Errorf("a cluster that fits the profile was warned about: %q", got)
	}
	// maxWorkers below the need is a different sentence: waiting will not help.
	capped := capacityAdvice(2, 3500, productionProfileRequestCPUMilli, 5, 3*time.Minute, 4)
	if !strings.Contains(capped, "maxWorkers=5") || !strings.Contains(capped, "stays Pending") {
		t.Errorf("a cap below the need was not called out: %q", capped)
	}
	if capacityAdvice(0, 3500, productionProfileRequestCPUMilli, 5, time.Minute, 1) != "" {
		t.Error("no workers known must produce no advice")
	}
}

// The advisory is given on the cloud path, where capacity decides the duration.
func TestTheCloudBringUpStatesItsCapacityShortfall(t *testing.T) {
	b, err := readBootstrapSource()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b, "capacityAdvice(workers, perWorker, productionProfileRequestCPUMilli") {
		t.Error("bootstrap.go does not compute the capacity advisory; a starved bring-up gives the operator no clue")
	}
}
