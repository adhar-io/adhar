package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// One slow kubelet must not blank every pod on its node: metrics-server's
// 10 s default dropped a loaded GCP worker and every HPA there went Degraded
// with "no metrics returned from resource metrics API" (2026-10-21). And
// metrics-server refuses to START unless resolution x 0.9 >= the timeout.
func TestMetricsServerToleratesASlowKubelet(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "observability", "metrics-server", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	get := func(flag string) time.Duration {
		m := regexp.MustCompile(`- --` + flag + `=(\S+)`).FindAllStringSubmatch(src, -1)
		if len(m) != 1 {
			t.Fatalf("--%s appears %d times; want exactly once", flag, len(m))
		}
		d, err := time.ParseDuration(m[0][1])
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	timeout, res := get("kubelet-request-timeout"), get("metric-resolution")
	if timeout < 20*time.Second {
		t.Errorf("kubelet-request-timeout %s: a loaded node's kubelet takes longer", timeout)
	}
	if res*9/10 < timeout {
		t.Errorf("metric-resolution %s x 0.9 < kubelet-request-timeout %s: metrics-server refuses to start", res, timeout)
	}
	v, _ := os.ReadFile(filepath.Join(stackPackagesDir(t), "observability", "metrics-server", "values.yaml"))
	if !strings.Contains(string(v), "--kubelet-request-timeout="+timeout.String()) && !strings.Contains(string(v), "--kubelet-request-timeout=25s") {
		t.Error("values.yaml and the manifest disagree on the timeout; regenerating would revert it")
	}
}
