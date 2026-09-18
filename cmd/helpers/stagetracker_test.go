package helpers

import (
	"bytes"
	"strings"
	"testing"
)

func TestStageTrackerSetDetailPrintsProgressOnceOnPlainOutput(t *testing.T) {
	var buf bytes.Buffer
	tr := NewStageTracker(&buf, "Provisioning", []StageDef{{Label: "GitOps sync", Detail: "apps"}}, false)
	tr.Start()
	tr.Activate(0)
	tr.SetDetail(0, "3/28 apps Synced + Healthy")
	tr.SetDetail(0, "3/28 apps Synced + Healthy") // unchanged: not printed again
	tr.SetDetail(0, "28/28 apps Synced + Healthy")
	tr.Done(0)
	tr.Stop()
	out := buf.String()
	if strings.Count(out, "3/28 apps") != 1 || strings.Count(out, "28/28 apps") != 1 {
		t.Errorf("each distinct detail is printed exactly once:\n%s", out)
	}
	if !strings.Contains(out, "GitOps sync") || !strings.Contains(out, "✓") {
		t.Errorf("stage completion still rendered:\n%s", out)
	}
	tr.SetDetail(5, "out of range must not panic")
}
