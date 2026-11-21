package batch

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// chunkedXR is batchJobXR declared as remote chunking (design 0025 D11), with lineage.
func chunkedXR(name, ns string) *unstructured.Unstructured {
	xr := batchJobXR(name, ns, "", "")
	p := xr.Object["spec"].(map[string]interface{})["parameters"].(map[string]interface{})
	p["mode"] = "chunked"
	p["workers"] = int64(3)
	p["chunking"] = map[string]interface{}{"requestQueue": name + "-chunks", "replyQueue": name + "-replies"}
	p["lineage"] = map[string]interface{}{"enabled": true, "credentialsSecretRef": map[string]interface{}{"name": name + "-om"}}
	return xr
}

// withDistribution gives every pod template of a run the env the composition puts on
// a chunked run's pods.
func withDistribution(run *unstructured.Unstructured, value string) *unstructured.Unstructured {
	for _, c := range containersOf(run) {
		c["env"] = append(c["env"].([]interface{}), map[string]interface{}{"name": envDistribution, "value": value})
	}
	return run
}

func indexed(run *unstructured.Unstructured) *unstructured.Unstructured {
	run.Object["spec"].(map[string]interface{})["completionMode"] = "Indexed"
	return run
}

// A chunked and a partitioned run are the same kind of object (an Indexed Job, or a
// Volcano gang); only the distribution the composition writes on their pods tells
// them apart, and the definition's mode tells the job's.
func TestModeIsReadFromTheDefinitionAndTheRun(t *testing.T) {
	at := testNow.Add(-time.Hour)
	for _, c := range []struct {
		name string
		run  *unstructured.Unstructured
		want string
	}{
		{"a single run", jobObject("importer-1", "prod", "importer", "running", at), modeSingle},
		{"a partitioned Indexed Job", indexed(jobObject("importer-2", "prod", "importer", "running", at)), modePartitioned},
		{"a chunked Indexed Job", withDistribution(indexed(jobObject("importer-3", "prod", "importer", "running", at)), "chunked"), modeChunked},
		{"a partitioned gang", volcanoRun("settle-1", "Running", at), modePartitioned},
		{"a chunked gang", withDistribution(volcanoRun("settle-2", "Running", at), "chunked"), modeChunked},
	} {
		if got := runModeOf(c.run); got != c.want {
			t.Errorf("%s: mode %s, want %s", c.name, got, c.want)
		}
	}
	if jobModeOf(batchJobXR("importer", "prod", "", "")) != modeSingle || jobModeOf(chunkedXR("ledger", "dev")) != modeChunked {
		t.Error("a definition's mode is its spec.parameters.mode, single by default")
	}
	if got := modeDetail(chunkedXR("ledger", "dev")); got != "chunked: 1 manager + 3 workers, remote chunking over RabbitMQ (chunks on Queue ledger-chunks, replies on Queue ledger-replies)" {
		t.Errorf("describe's mode line = %q", got)
	}
	part := batchJobXR("settle", "prod", "", "")
	part.Object["spec"].(map[string]interface{})["parameters"].(map[string]interface{})["mode"] = "partitioned"
	if got := modeDetail(part); got != "partitioned: 1 manager + 2 workers, 8 partitions over the JobRepository's database" {
		t.Errorf("describe's mode line for a partitioned job with the defaults = %q", got)
	}
	if got := lineageDetail(chunkedXR("ledger", "dev")); got != "OpenLineage to the platform's OpenMetadata, token from Secret ledger-om" {
		t.Errorf("lineage line = %q", got)
	}
	if got := lineageDetail(batchJobXR("importer", "prod", "", "")); got != "" {
		t.Errorf("a job without lineage describes %q", got)
	}
}

// `adhar batch list` and `runs` say chunked: the list in a MODE column and in -o json,
// the runs table only when a run is of the manager-and-workers shape.
func TestListAndRunsShowModeChunked(t *testing.T) {
	fixedClock(t)
	at := testNow.Add(-time.Hour)
	run := withDistribution(indexed(jobObject("ledger-1", "dev", "ledger", "running", at)), "chunked")
	dc := fakeCluster(chunkedXR("ledger", "dev"), batchJobXR("importer", "dev", "", ""), run)
	views, err := listJobs(context.Background(), dc, "dev")
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, v := range views {
		modes[v.Name] = v.Mode
	}
	if modes["ledger"] != "chunked" || modes["importer"] != "single" {
		t.Errorf("list modes = %v", modes)
	}
	var ledger jobView
	for _, v := range views {
		if v.Name == "ledger" {
			ledger = v
		}
	}
	if ledger.LastRun == nil || ledger.LastRun.Mode != "chunked" {
		t.Fatalf("the chunked job's last run = %+v", ledger.LastRun)
	}
	table := runsTable([]runView{*ledger.LastRun}).Render()
	if !strings.Contains(table, "MODE") || !strings.Contains(table, "chunked") {
		t.Errorf("the runs table does not say chunked:\n%s", table)
	}
	single := runViewOf(runRowOf(jobObject("importer-1", "dev", "importer", "running", at), nil, testNow), testNow)
	if strings.Contains(runsTable([]runView{single}).Render(), "MODE") {
		t.Error("a table of single runs grew a MODE column")
	}
}
