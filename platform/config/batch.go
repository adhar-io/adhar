package config

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// BatchSettings is globalSettings.batch: how much of the cluster batch may hold
// when Volcano schedules it (design 0025 §4.1, §4.3, D7, D17; user decision DEC-3,
// 2026-11-14). Every field is optional. `adhar up` renders the result at seed time
// into core/volcano's root Queue and the scheduler descriptor ConfigMap.
type BatchSettings struct {
	// Capacity is the batch ceiling: the root queue adhar-batch's capability and
	// deserved. Default: 30% of the CPU and 20% of the memory of
	// maxWorkers x worker allocatable, rounded down to whole CPUs and whole GiB.
	Capacity *BatchResources `mapstructure:"capacity" json:"capacity,omitempty"`
	// ProdDeserved is the deserved share of every team's prod leaf queue, what lets
	// prod reclaim from dev and test. Default: per resource, the larger of
	// BatchGangFloor and a quarter of the ceiling, never more than the ceiling. An
	// explicit value above the ceiling is refused; one below the floor is accepted
	// with a warning (the design's own example gang could then never reclaim).
	ProdDeserved *BatchResources `mapstructure:"prodDeserved" json:"prodDeserved,omitempty"`
}

// BatchResources is a CPU and memory pair, in Kubernetes quantity notation.
type BatchResources struct {
	CPU    string `mapstructure:"cpu" json:"cpu,omitempty"`
	Memory string `mapstructure:"memory" json:"memory,omitempty"`
}

// The D17 constants.
const (
	// BatchCeilingCPUPercent and BatchCeilingMemoryPercent are D8's fractions of
	// maxWorkers x worker allocatable. Memory gets the smaller one because memory,
	// not CPU, is what this platform runs out of (design §14).
	BatchCeilingCPUPercent    = 30
	BatchCeilingMemoryPercent = 20
	// BatchGangFloorCPU and BatchGangFloorMemory are the largest gang the design's
	// examples use: §6.3's partitioned job, a 500m / 1Gi manager plus 4 workers at
	// 1 CPU / 1536Mi. A prod share below it cannot reclaim for that gang.
	BatchGangFloorCPU    = "4500m"
	BatchGangFloorMemory = "7Gi"
	// BatchReferenceCeilingCPU and BatchReferenceCeilingMemory are used when the
	// cluster cannot be measured (no worker is up yet, no maxWorkers): D8 applied
	// to the reference cluster of design §2.2 (10 x 8 vCPU / 16 GiB workers).
	BatchReferenceCeilingCPU    = "22"
	BatchReferenceCeilingMemory = "24Gi"
)

// BatchScheduler is what the seed renders: the root queue's ceiling and the prod
// leaves' deserved share, as canonical quantities.
type BatchScheduler struct {
	CeilingCPU, CeilingMemory           string
	ProdDeservedCPU, ProdDeservedMemory string
	// Derived says whether the ceiling was measured from the cluster (false: it
	// came from globalSettings.batch.capacity or the reference defaults).
	Derived bool
}

// ResolveBatchScheduler applies D17. maxWorkers is the most workers the cluster
// may have; workerCPUMilli and workerMemoryBytes are one worker's allocatable.
// Any of the three at zero means the cluster could not be measured, and an unset
// ceiling falls back to the reference values. warnings are for `adhar up` to print.
func ResolveBatchScheduler(s *BatchSettings, maxWorkers int, workerCPUMilli, workerMemoryBytes int64) (BatchScheduler, []string, error) {
	var warnings []string
	if s == nil {
		s = &BatchSettings{}
	}
	measured := maxWorkers > 0 && workerCPUMilli > 0 && workerMemoryBytes > 0

	ceilCPU, err := batchQuantity("globalSettings.batch.capacity.cpu", pick(s.Capacity, true))
	if err != nil {
		return BatchScheduler{}, nil, err
	}
	ceilMem, err := batchQuantity("globalSettings.batch.capacity.memory", pick(s.Capacity, false))
	if err != nil {
		return BatchScheduler{}, nil, err
	}
	derived := false
	const gib = int64(1) << 30
	if ceilCPU == nil {
		derived = measured
		if measured {
			// Whole CPUs, never below one: a ceiling of zero would admit nothing.
			whole := int64(maxWorkers) * workerCPUMilli * BatchCeilingCPUPercent / 100 / 1000
			ceilCPU = resource.NewQuantity(max(whole, 1), resource.DecimalSI)
		} else {
			q := resource.MustParse(BatchReferenceCeilingCPU)
			ceilCPU = &q
		}
	}
	if ceilMem == nil {
		derived = derived || measured
		if measured {
			whole := int64(maxWorkers) * workerMemoryBytes * BatchCeilingMemoryPercent / 100 / gib
			ceilMem = resource.NewQuantity(max(whole, 1)*gib, resource.BinarySI)
		} else {
			q := resource.MustParse(BatchReferenceCeilingMemory)
			ceilMem = &q
		}
	}

	share := func(field, explicit, floor string, ceiling *resource.Quantity) (*resource.Quantity, error) {
		f := resource.MustParse(floor)
		q, err := batchQuantity(field, explicit)
		if err != nil {
			return nil, err
		}
		if q != nil {
			if q.Cmp(*ceiling) > 0 {
				return nil, fmt.Errorf("%s %s is larger than the batch ceiling %s; every prod leaf would be refused by the root queue", field, q.String(), ceiling.String())
			}
			if q.Cmp(f) < 0 {
				warnings = append(warnings, fmt.Sprintf("%s %s is below %s, the largest gang the batch design's examples use (design 0025 D17): a prod gang that size could never reclaim its share from dev and test", field, q.String(), f.String()))
			}
			return q, nil
		}
		// The larger of the floor and a quarter of the ceiling, at most the ceiling.
		quarter := resource.NewMilliQuantity(ceiling.MilliValue()/4, ceiling.Format)
		out := f
		if quarter.Cmp(out) > 0 {
			out = *quarter
		}
		if out.Cmp(*ceiling) > 0 {
			out = ceiling.DeepCopy()
			// DEC-3: the share is never below the largest gang unless the whole
			// ceiling is, and then the operator hears it here rather than from a
			// prod gang that can never be admitted.
			warnings = append(warnings, fmt.Sprintf("the batch ceiling %s is below %s, the largest gang the batch design's examples use (design 0025 D17), so %s is the whole ceiling and a prod gang that size never fits: add workers or set globalSettings.batch.capacity", ceiling.String(), f.String(), field))
		}
		return &out, nil
	}
	prodCPU, err := share("globalSettings.batch.prodDeserved.cpu", pick(s.ProdDeserved, true), BatchGangFloorCPU, ceilCPU)
	if err != nil {
		return BatchScheduler{}, nil, err
	}
	prodMem, err := share("globalSettings.batch.prodDeserved.memory", pick(s.ProdDeserved, false), BatchGangFloorMemory, ceilMem)
	if err != nil {
		return BatchScheduler{}, nil, err
	}
	return BatchScheduler{
		CeilingCPU: canonical(ceilCPU), CeilingMemory: canonical(ceilMem),
		ProdDeservedCPU: canonical(prodCPU), ProdDeservedMemory: canonical(prodMem),
		Derived: derived,
	}, warnings, nil
}

func pick(r *BatchResources, cpu bool) string {
	if r == nil {
		return ""
	}
	if cpu {
		return r.CPU
	}
	return r.Memory
}

// batchQuantity parses an optional, strictly positive quantity.
func batchQuantity(field, v string) (*resource.Quantity, error) {
	if v == "" {
		return nil, nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return nil, fmt.Errorf("%s %q is not a Kubernetes quantity: %w", field, v, err)
	}
	if q.Sign() <= 0 {
		return nil, fmt.Errorf("%s %q must be greater than zero", field, v)
	}
	return &q, nil
}

// canonical renders a quantity the way the API server stores it, so the seeded
// manifest and the live object agree byte for byte (an Argo CD diff otherwise).
func canonical(q *resource.Quantity) string {
	c := q.DeepCopy()
	if c.Format == resource.BinarySI && c.MilliValue()%1000 != 0 {
		// A fraction of a byte cannot be expressed in Mi/Gi; whole bytes it is.
		c = *resource.NewQuantity(c.Value(), resource.BinarySI)
	}
	return c.String()
}

type batchSettingError struct {
	field string
	value any
	msg   string
}

// ValidateBatchSettings checks what can be checked without the cluster: every
// value is a positive quantity, and an explicit prod share is not larger than an
// explicit ceiling. A share against a DERIVED ceiling is checked by `adhar up`
// (ResolveBatchScheduler), which knows the cluster.
func ValidateBatchSettings(s *BatchSettings) []batchSettingError {
	if s == nil {
		return nil
	}
	var out []batchSettingError
	parsed := map[string]*resource.Quantity{}
	for _, f := range []struct {
		field string
		r     *BatchResources
		cpu   bool
	}{
		{"globalSettings.batch.capacity.cpu", s.Capacity, true},
		{"globalSettings.batch.capacity.memory", s.Capacity, false},
		{"globalSettings.batch.prodDeserved.cpu", s.ProdDeserved, true},
		{"globalSettings.batch.prodDeserved.memory", s.ProdDeserved, false},
	} {
		v := pick(f.r, f.cpu)
		q, err := batchQuantity(f.field, v)
		if err != nil {
			out = append(out, batchSettingError{f.field, v, err.Error()})
			continue
		}
		parsed[f.field] = q
	}
	for _, r := range []string{"cpu", "memory"} {
		c, p := parsed["globalSettings.batch.capacity."+r], parsed["globalSettings.batch.prodDeserved."+r]
		if c != nil && p != nil && p.Cmp(*c) > 0 {
			out = append(out, batchSettingError{"globalSettings.batch.prodDeserved." + r, p.String(),
				"the prod share is larger than the batch ceiling (globalSettings.batch.capacity." + r + " " + c.String() + "); every prod leaf queue would be refused"})
		}
	}
	return out
}
