package webhooks

// Hierarchical tenant quotas.
//
// A CompositeProject already gets a guard-railed namespace with a ResourceQuota
// (see compositions/project/local.yaml), so no single project can exhaust the
// cluster. What was missing is the level above: nothing stopped one team creating
// thirty projects, each individually reasonable, and consuming everything.
// Per-namespace quotas are a floor-level control; a platform shared by many teams
// also needs a ceiling per TEAM.
//
// The ceiling is declared in a ConfigMap rather than a new CRD:
//
//   adhar-system/adhar-tenant-quotas
//     default: |            # applies to any team without its own entry
//       cpu: "32"
//       memory: 64Gi
//       pods: "200"
//       projects: 10
//     platform: |           # a named team's own allowance
//       cpu: "128"
//       ...
//
// A ConfigMap because the allowance is policy, not infrastructure: it belongs in
// the environments repo where it is reviewed like any other config, and it must
// be editable without a CRD migration. An absent ConfigMap means no ceiling is
// enforced — the platform must not start rejecting every project the moment this
// code ships, so the feature fails OPEN by design and is opted into by declaring
// the allowance.
//
// The arithmetic here is deliberately separate from admission so it can be tested
// without an API server: getting a quota comparison subtly wrong either blocks
// legitimate work or silently permits overcommit, and neither is visible in a
// smoke test.

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// TenantQuotaConfigMap is where allowances are declared.
const (
	TenantQuotaConfigMap = "adhar-tenant-quotas"
	// TenantQuotaDefaultKey applies to teams with no entry of their own.
	TenantQuotaDefaultKey = "default"
)

// Allowance is a team's ceiling. A zero field means "not limited on this axis",
// which lets an operator cap memory without having to invent a CPU number.
type Allowance struct {
	CPU      resource.Quantity
	Memory   resource.Quantity
	Pods     int64
	Projects int
}

// Usage is what a team has already committed, summed across its projects.
type Usage struct {
	CPU      resource.Quantity
	Memory   resource.Quantity
	Pods     int64
	Projects int
}

// ProjectRequest is one project's asks, as declared on the composite's spec.
type ProjectRequest struct {
	// Name identifies the project so an UPDATE can exclude its own current
	// usage from the sum — otherwise re-applying an unchanged project appears to
	// double its cost and is rejected.
	Name        string
	CPUQuota    string
	MemoryQuota string
	PodQuota    int64
}

// ParseAllowance reads one ConfigMap value. The format is intentionally the same
// vocabulary as a ResourceQuota so there is nothing new to learn.
func ParseAllowance(raw string) (Allowance, error) {
	var a Allowance
	for lineNo, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Allowance{}, fmt.Errorf("line %d: %q is not key: value", lineNo+1, line)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if value == "" {
			continue
		}
		switch key {
		case "cpu":
			q, err := resource.ParseQuantity(value)
			if err != nil {
				return Allowance{}, fmt.Errorf("cpu %q: %w", value, err)
			}
			a.CPU = q
		case "memory":
			q, err := resource.ParseQuantity(value)
			if err != nil {
				return Allowance{}, fmt.Errorf("memory %q: %w", value, err)
			}
			a.Memory = q
		case "pods":
			q, err := resource.ParseQuantity(value)
			if err != nil {
				return Allowance{}, fmt.Errorf("pods %q: %w", value, err)
			}
			a.Pods = q.Value()
		case "projects":
			q, err := resource.ParseQuantity(value)
			if err != nil {
				return Allowance{}, fmt.Errorf("projects %q: %w", value, err)
			}
			a.Projects = int(q.Value())
		default:
			// Unknown keys are ignored rather than rejected: a future axis added
			// to the ConfigMap must not make an older controller refuse every
			// project in the cluster.
			continue
		}
	}
	return a, nil
}

// SumUsage adds up what a team's existing projects already claim.
//
// `exclude` is the project being admitted: on an UPDATE its current row is
// already in the list, and counting it would make an unchanged re-apply look
// like a doubling.
func SumUsage(existing []ProjectRequest, exclude string) (Usage, error) {
	var u Usage
	for _, p := range existing {
		if p.Name == exclude {
			continue
		}
		u.Projects++
		if p.CPUQuota != "" {
			q, err := resource.ParseQuantity(p.CPUQuota)
			if err != nil {
				return Usage{}, fmt.Errorf("project %s cpuQuota %q: %w", p.Name, p.CPUQuota, err)
			}
			u.CPU.Add(q)
		}
		if p.MemoryQuota != "" {
			q, err := resource.ParseQuantity(p.MemoryQuota)
			if err != nil {
				return Usage{}, fmt.Errorf("project %s memoryQuota %q: %w", p.Name, p.MemoryQuota, err)
			}
			u.Memory.Add(q)
		}
		u.Pods += p.PodQuota
	}
	return u, nil
}

// Verdict is the outcome of a quota check.
type Verdict struct {
	Allowed bool
	// Reasons names every axis that would be exceeded, not just the first, so a
	// team resizing a project learns everything it has to change in one attempt.
	Reasons []string
}

// Check decides whether admitting `req` keeps the team within `allow`.
//
// An axis with a zero allowance is unlimited. That is the difference between "we
// do not cap memory" and "memory cap is zero", and conflating them would deny
// every project the moment an operator declares a CPU-only allowance.
func Check(allow Allowance, current Usage, req ProjectRequest) (Verdict, error) {
	v := Verdict{Allowed: true}

	if allow.Projects > 0 && current.Projects+1 > allow.Projects {
		v.Allowed = false
		v.Reasons = append(v.Reasons, fmt.Sprintf(
			"projects: %d already exist and the team's allowance is %d", current.Projects, allow.Projects))
	}

	if !allow.CPU.IsZero() && req.CPUQuota != "" {
		q, err := resource.ParseQuantity(req.CPUQuota)
		if err != nil {
			return Verdict{}, fmt.Errorf("cpuQuota %q: %w", req.CPUQuota, err)
		}
		total := current.CPU.DeepCopy()
		total.Add(q)
		if total.Cmp(allow.CPU) > 0 {
			v.Allowed = false
			v.Reasons = append(v.Reasons, fmt.Sprintf(
				"cpu: %s requested + %s already committed = %s, over the team's %s",
				q.String(), current.CPU.String(), total.String(), allow.CPU.String()))
		}
	}

	if !allow.Memory.IsZero() && req.MemoryQuota != "" {
		q, err := resource.ParseQuantity(req.MemoryQuota)
		if err != nil {
			return Verdict{}, fmt.Errorf("memoryQuota %q: %w", req.MemoryQuota, err)
		}
		total := current.Memory.DeepCopy()
		total.Add(q)
		if total.Cmp(allow.Memory) > 0 {
			v.Allowed = false
			v.Reasons = append(v.Reasons, fmt.Sprintf(
				"memory: %s requested + %s already committed = %s, over the team's %s",
				q.String(), current.Memory.String(), total.String(), allow.Memory.String()))
		}
	}

	if allow.Pods > 0 && req.PodQuota > 0 {
		total := current.Pods + req.PodQuota
		if total > allow.Pods {
			v.Allowed = false
			v.Reasons = append(v.Reasons, fmt.Sprintf(
				"pods: %d requested + %d already committed = %d, over the team's %d",
				req.PodQuota, current.Pods, total, allow.Pods))
		}
	}

	sort.Strings(v.Reasons)
	return v, nil
}

// DenyMessage renders a verdict as the sentence an engineer sees when their
// project is rejected. It names the team and every breached axis, because the
// alternative — "quota exceeded" — sends them to ask an administrator what the
// limit is.
func DenyMessage(team string, v Verdict) string {
	return fmt.Sprintf("project exceeds the %q team's tenant quota — %s. Raise the team's entry in the %s ConfigMap, or reduce this project's quotas.",
		team, strings.Join(v.Reasons, "; "), TenantQuotaConfigMap)
}
