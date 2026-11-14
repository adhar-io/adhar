package config

import (
	"strings"
	"testing"
)

const gib = int64(1) << 30

// D17 (design 0025, user decision DEC-3): the batch ceiling is 30% of the CPU and
// 20% of the memory of maxWorkers x worker allocatable, rounded down to whole CPUs
// and GiB; the prod share is the larger of the example-gang floor and a quarter of
// the ceiling, never more than the ceiling. Mutation-tested: each constant and each
// branch below was reverted and a row seen failing.
func TestBatchSchedulerIsSizedFromTheCluster(t *testing.T) {
	cases := []struct {
		name                     string
		settings                 *BatchSettings
		maxWorkers               int
		cpuMilli, memBytes       int64
		ceilCPU, ceilMem         string
		prodCPU, prodMem         string
		derived                  bool
		wantWarning, wantErrText string
	}{
		{
			// Design §2.2: 10 x s-8vcpu-16gb, 7500m / 12.5 GiB allocatable after the
			// kubelet's 500m / 2.5 GiB reserve and 1 GiB eviction margin.
			name: "the reference cluster", maxWorkers: 10, cpuMilli: 7500, memBytes: 25 * gib / 2,
			ceilCPU: "22", ceilMem: "25Gi", prodCPU: "5500m", prodMem: "7Gi", derived: true,
		},
		{
			name: "a large cluster: the quarter wins", maxWorkers: 20, cpuMilli: 15500, memBytes: 60 * gib,
			ceilCPU: "93", ceilMem: "240Gi", prodCPU: "23250m", prodMem: "60Gi", derived: true,
		},
		{
			// 0.3 x 2 x 3500m = 2.1 -> 2; 0.2 x 2 x 6.5Gi = 2.6 -> 2Gi. The floor is
			// larger than the ceiling, so the share is the ceiling.
			name: "a small cluster: the share is clamped to the ceiling", maxWorkers: 2, cpuMilli: 3500, memBytes: 13 * gib / 2,
			ceilCPU: "2", ceilMem: "2Gi", prodCPU: "2", prodMem: "2Gi", derived: true,
			wantWarning: "never fits",
		},
		{
			name: "a cluster too small for one whole CPU still gets one", maxWorkers: 1, cpuMilli: 1500, memBytes: 3 * gib,
			ceilCPU: "1", ceilMem: "1Gi", prodCPU: "1", prodMem: "1Gi", derived: true,
			wantWarning: "never fits",
		},
		{
			name: "an unmeasured cluster takes the reference ceiling", maxWorkers: 0,
			ceilCPU: "22", ceilMem: "24Gi", prodCPU: "5500m", prodMem: "7Gi",
		},
		{
			name:       "an explicit ceiling wins over the measurement",
			settings:   &BatchSettings{Capacity: &BatchResources{CPU: "40", Memory: "64Gi"}},
			maxWorkers: 10, cpuMilli: 7500, memBytes: 25 * gib / 2,
			ceilCPU: "40", ceilMem: "64Gi", prodCPU: "10", prodMem: "16Gi",
		},
		{
			name:       "an explicit share is used as given",
			settings:   &BatchSettings{ProdDeserved: &BatchResources{CPU: "6", Memory: "8Gi"}},
			maxWorkers: 10, cpuMilli: 7500, memBytes: 25 * gib / 2,
			ceilCPU: "22", ceilMem: "25Gi", prodCPU: "6", prodMem: "8Gi", derived: true,
		},
		{
			name:       "a share below the floor is accepted with a warning",
			settings:   &BatchSettings{ProdDeserved: &BatchResources{CPU: "2", Memory: "4Gi"}},
			maxWorkers: 10, cpuMilli: 7500, memBytes: 25 * gib / 2,
			ceilCPU: "22", ceilMem: "25Gi", prodCPU: "2", prodMem: "4Gi", derived: true,
			wantWarning: "could never reclaim",
		},
		{
			name:       "a share above the ceiling is refused",
			settings:   &BatchSettings{ProdDeserved: &BatchResources{CPU: "23"}},
			maxWorkers: 10, cpuMilli: 7500, memBytes: 25 * gib / 2,
			wantErrText: "larger than the batch ceiling",
		},
		{
			name:        "a value that is not a quantity is refused",
			settings:    &BatchSettings{Capacity: &BatchResources{Memory: "lots"}},
			wantErrText: "not a Kubernetes quantity",
		},
		{
			name:        "a zero ceiling is refused",
			settings:    &BatchSettings{Capacity: &BatchResources{CPU: "0"}},
			wantErrText: "greater than zero",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, warnings, err := ResolveBatchScheduler(c.settings, c.maxWorkers, c.cpuMilli, c.memBytes)
			if c.wantErrText != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrText) {
					t.Fatalf("err = %v, want one saying %q", err, c.wantErrText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := BatchScheduler{CeilingCPU: c.ceilCPU, CeilingMemory: c.ceilMem, ProdDeservedCPU: c.prodCPU, ProdDeservedMemory: c.prodMem, Derived: c.derived}
			if got != want {
				t.Errorf("got %+v\nwant %+v", got, want)
			}
			joined := strings.Join(warnings, "\n")
			if (c.wantWarning == "") != (joined == "") || !strings.Contains(joined, c.wantWarning) {
				t.Errorf("warnings %q, want %q", joined, c.wantWarning)
			}
		})
	}
}

// The validator checks what it can without the cluster.
func TestBatchSettingsAreValidatedWithoutTheCluster(t *testing.T) {
	if errs := ValidateBatchSettings(nil); len(errs) != 0 {
		t.Errorf("no globalSettings.batch: %v", errs)
	}
	ok := &BatchSettings{Capacity: &BatchResources{CPU: "22", Memory: "24Gi"}, ProdDeserved: &BatchResources{CPU: "5500m", Memory: "7Gi"}}
	if errs := ValidateBatchSettings(ok); len(errs) != 0 {
		t.Errorf("a valid batch block: %v", errs)
	}
	bad := &BatchSettings{Capacity: &BatchResources{CPU: "4", Memory: "-1Gi"}, ProdDeserved: &BatchResources{CPU: "5", Memory: "x"}}
	errs := ValidateBatchSettings(bad)
	fields := map[string]bool{}
	for _, e := range errs {
		fields[e.field] = true
	}
	for _, f := range []string{"globalSettings.batch.capacity.memory", "globalSettings.batch.prodDeserved.memory", "globalSettings.batch.prodDeserved.cpu"} {
		if !fields[f] {
			t.Errorf("no error for %s: %v", f, errs)
		}
	}
}
