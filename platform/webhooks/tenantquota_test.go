package webhooks

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func qty(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		panic(err)
	}
	return q
}

func TestParseAllowanceReadsResourceQuotaVocabulary(t *testing.T) {
	a, err := ParseAllowance(`
# a team's ceiling
cpu: "32"
memory: 64Gi
pods: "200"
projects: 10
`)
	if err != nil {
		t.Fatal(err)
	}
	if a.CPU.Cmp(qty("32")) != 0 {
		t.Fatalf("cpu = %s", a.CPU.String())
	}
	if a.Memory.Cmp(qty("64Gi")) != 0 {
		t.Fatalf("memory = %s", a.Memory.String())
	}
	if a.Pods != 200 || a.Projects != 10 {
		t.Fatalf("pods=%d projects=%d", a.Pods, a.Projects)
	}
}

func TestParseAllowanceIgnoresUnknownKeys(t *testing.T) {
	// A future axis added to the ConfigMap must not make an older controller
	// refuse every project in the cluster.
	a, err := ParseAllowance("cpu: \"8\"\ngpus: \"4\"\n")
	if err != nil {
		t.Fatalf("unknown key should be ignored, got %v", err)
	}
	if a.CPU.Cmp(qty("8")) != 0 {
		t.Fatalf("cpu = %s", a.CPU.String())
	}
}

func TestParseAllowanceRejectsAMalformedQuantity(t *testing.T) {
	// Silently treating "lots" as unlimited would be worse than refusing to load.
	if _, err := ParseAllowance("cpu: lots\n"); err == nil {
		t.Fatal("expected an error for an unparseable quantity")
	}
}

func TestZeroAxisMeansUnlimitedNotZero(t *testing.T) {
	// The distinction that matters: an operator capping memory only must not have
	// every project denied for exceeding a CPU allowance of zero.
	allow, err := ParseAllowance("memory: 10Gi\n")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Check(allow, Usage{}, ProjectRequest{Name: "p", CPUQuota: "1000", MemoryQuota: "1Gi", PodQuota: 99999})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Allowed {
		t.Fatalf("only memory is capped, so this must be allowed; reasons=%v", v.Reasons)
	}
}

func TestCheckSumsAcrossProjectsNotJustTheNewOne(t *testing.T) {
	// The whole point: each project is individually reasonable, the total is not.
	allow, _ := ParseAllowance("cpu: \"10\"\n")
	existing := []ProjectRequest{
		{Name: "a", CPUQuota: "4"},
		{Name: "b", CPUQuota: "4"},
	}
	usage, err := SumUsage(existing, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Check(allow, usage, ProjectRequest{Name: "c", CPUQuota: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Allowed {
		t.Fatal("4+4+4 > 10 must be denied")
	}
	if len(v.Reasons) != 1 || !strings.Contains(v.Reasons[0], "cpu") {
		t.Fatalf("reasons = %v", v.Reasons)
	}
}

func TestUpdatingAProjectDoesNotCountItTwice(t *testing.T) {
	// The regression this exists for: on UPDATE the project is already in the
	// list, so a naive sum makes an unchanged re-apply look like a doubling and
	// rejects it — which means a team cannot edit a project's description without
	// tripping quota.
	allow, _ := ParseAllowance("cpu: \"10\"\n")
	existing := []ProjectRequest{{Name: "a", CPUQuota: "8"}}

	usage, err := SumUsage(existing, "a") // exclude the one being admitted
	if err != nil {
		t.Fatal(err)
	}
	v, err := Check(allow, usage, ProjectRequest{Name: "a", CPUQuota: "8"})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Allowed {
		t.Fatalf("re-applying an unchanged project must be allowed; reasons=%v", v.Reasons)
	}

	// And a genuine increase past the ceiling is still caught.
	v2, _ := Check(allow, usage, ProjectRequest{Name: "a", CPUQuota: "11"})
	if v2.Allowed {
		t.Fatal("raising a project past the ceiling must still be denied")
	}
}

func TestProjectCountCeiling(t *testing.T) {
	allow, _ := ParseAllowance("projects: 2\n")
	usage, _ := SumUsage([]ProjectRequest{{Name: "a"}, {Name: "b"}}, "")
	v, _ := Check(allow, usage, ProjectRequest{Name: "c"})
	if v.Allowed {
		t.Fatal("a third project with an allowance of 2 must be denied")
	}
	if !strings.Contains(strings.Join(v.Reasons, " "), "projects") {
		t.Fatalf("reasons = %v", v.Reasons)
	}
}

func TestAllBreachedAxesAreReportedTogether(t *testing.T) {
	// One attempt should tell a team everything they have to change, rather than
	// making them rediscover the next limit on every retry.
	allow, _ := ParseAllowance("cpu: \"2\"\nmemory: 1Gi\npods: 5\nprojects: 1\n")
	usage, _ := SumUsage([]ProjectRequest{{Name: "a", CPUQuota: "2", MemoryQuota: "1Gi", PodQuota: 5}}, "")
	v, _ := Check(allow, usage, ProjectRequest{Name: "b", CPUQuota: "2", MemoryQuota: "1Gi", PodQuota: 5})
	if v.Allowed {
		t.Fatal("should be denied")
	}
	joined := strings.Join(v.Reasons, " | ")
	for _, axis := range []string{"cpu", "memory", "pods", "projects"} {
		if !strings.Contains(joined, axis) {
			t.Fatalf("%s missing from reasons: %s", axis, joined)
		}
	}
}

func TestExactlyAtTheCeilingIsAllowed(t *testing.T) {
	// An off-by-one here denies a team the last unit of what they were granted.
	allow, _ := ParseAllowance("cpu: \"10\"\npods: 10\n")
	usage, _ := SumUsage([]ProjectRequest{{Name: "a", CPUQuota: "6", PodQuota: 6}}, "")
	v, _ := Check(allow, usage, ProjectRequest{Name: "b", CPUQuota: "4", PodQuota: 4})
	if !v.Allowed {
		t.Fatalf("6+4 == 10 is within a ceiling of 10; reasons=%v", v.Reasons)
	}
}

func TestMilliCPUAndBinaryMemoryUnitsAddCorrectly(t *testing.T) {
	// Quantities, not floats: 500m+500m must be 1, and Mi/Gi must not be mixed up.
	allow, _ := ParseAllowance("cpu: \"1\"\nmemory: 1Gi\n")
	usage, err := SumUsage([]ProjectRequest{
		{Name: "a", CPUQuota: "500m", MemoryQuota: "512Mi"},
		{Name: "b", CPUQuota: "400m", MemoryQuota: "256Mi"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if usage.CPU.Cmp(qty("900m")) != 0 {
		t.Fatalf("cpu sum = %s, want 900m", usage.CPU.String())
	}
	if usage.Memory.Cmp(qty("768Mi")) != 0 {
		t.Fatalf("memory sum = %s, want 768Mi", usage.Memory.String())
	}
	// 900m + 100m == 1 exactly → allowed.
	if v, _ := Check(allow, usage, ProjectRequest{Name: "c", CPUQuota: "100m", MemoryQuota: "256Mi"}); !v.Allowed {
		t.Fatalf("should fit exactly; reasons=%v", v.Reasons)
	}
	// One more milli-CPU does not.
	if v, _ := Check(allow, usage, ProjectRequest{Name: "c", CPUQuota: "101m"}); v.Allowed {
		t.Fatal("901m > 1 cpu must be denied")
	}
}

func TestSumUsageRejectsAMalformedProjectQuota(t *testing.T) {
	// A project whose quota cannot be parsed must not silently count as zero —
	// that would let a typo buy unlimited capacity.
	if _, err := SumUsage([]ProjectRequest{{Name: "bad", CPUQuota: "1 cpu"}}, ""); err == nil {
		t.Fatal("expected an error for an unparseable project quota")
	}
}

func TestNoAllowanceMeansNoCeiling(t *testing.T) {
	// The feature fails OPEN: shipping this code must not start rejecting every
	// project on clusters that never declared an allowance.
	v, err := Check(Allowance{}, Usage{Projects: 500, Pods: 99999}, ProjectRequest{Name: "x", CPUQuota: "999", MemoryQuota: "999Gi", PodQuota: 9999})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Allowed {
		t.Fatalf("an empty allowance must not enforce anything; reasons=%v", v.Reasons)
	}
}

func TestDenyMessageNamesTheTeamAndWhereToRaiseIt(t *testing.T) {
	msg := DenyMessage("payments", Verdict{Reasons: []string{"cpu: over"}})
	for _, want := range []string{"payments", "cpu: over", TenantQuotaConfigMap} {
		if !strings.Contains(msg, want) {
			t.Fatalf("DenyMessage missing %q: %s", want, msg)
		}
	}
}
