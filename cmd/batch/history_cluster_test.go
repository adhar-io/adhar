/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"
	platformevents "adhar-io/adhar/platform/events"

	"k8s.io/client-go/rest"
)

const bridgeProxyPath = "/api/v1/namespaces/adhar-system/services/adhar-events-bridge-service:8080/proxy"

// clusterDouble is an API server as far as `history` and `dlq` go: the
// BatchJob, no runs, and — when it has a bus — the event bridge behind the
// service proxy, speaking the consumer protocol and naming its consumers the
// way the real bridge does behind that proxy: without the proxy's path.
type clusterDouble struct {
	mu         sync.Mutex
	bus        bool
	events     []platformevents.Event
	requests   []string
	subscribed []string
	deleted    int
}

func (c *clusterDouble) serve(t *testing.T) *httptest.Server {
	notFound := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.requests = append(c.requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case path == "/apis/platform.adhar.io/v1alpha1/namespaces/prod/batchjobs/importer":
			_ = json.NewEncoder(w).Encode(batchJobXR("importer", "prod", "", "").Object)
		case path == "/apis/batch/v1/namespaces/prod/jobs":
			_, _ = w.Write([]byte(`{"apiVersion":"batch/v1","kind":"JobList","items":[]}`))
		case path == "/api/v1/namespaces/prod/pods":
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"PodList","items":[]}`))
		case !c.bus || !strings.HasPrefix(path, bridgeProxyPath+"/"):
			notFound(w)
		default:
			c.bridge(t, w, r, strings.TrimPrefix(path, bridgeProxyPath))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (c *clusterDouble) bridge(t *testing.T, w http.ResponseWriter, r *http.Request, path string) {
	switch {
	case r.Method == http.MethodGet && path == "/topics":
		_, _ = w.Write([]byte(`["adhar.batch"]`))
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/consumers/") && strings.Count(path, "/") == 2:
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]string{"instance_id": req.Name, "base_uri": "http://" + r.Host + path + "/instances/" + req.Name})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/subscription"):
		var req map[string][]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		c.subscribed = req["topics"]
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/records"):
		records := []map[string]interface{}{}
		for i, e := range c.events {
			records = append(records, map[string]interface{}{"topic": platformevents.TopicBatch, "partition": 0, "offset": i, "key": e.Subject, "value": e})
		}
		c.events = nil
		_ = json.NewEncoder(w).Encode(records)
	case r.Method == http.MethodDelete:
		c.deleted++
		w.WriteHeader(http.StatusNoContent)
	default:
		t.Errorf("unexpected bridge call %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func kubeconfigAt(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	body := strings.Replace(unreachableKubeconfig, "https://127.0.0.1:1", server, 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// executeBatch runs `adhar batch <args>` and returns what it printed.
func executeBatch(t *testing.T, args ...string) (string, error) {
	t.Helper()
	reset := func() { namespaceFlag, envFlag, projectFlag, outputFlag = "", "", defaultProject, "table" }
	reset()
	t.Cleanup(func() {
		reset()
		BatchCmd.SetArgs(nil)
		BatchCmd.SetOut(nil)
		BatchCmd.SetErr(nil)
		for _, c := range BatchCmd.Commands() {
			c.SilenceUsage = false
		}
	})
	BatchCmd.SetOut(io.Discard)
	BatchCmd.SetErr(io.Discard)
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printed := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		printed <- string(b)
	}()
	BatchCmd.SetArgs(args)
	_, runErr := BatchCmd.ExecuteC()
	os.Stdout = stdout
	_ = w.Close()
	return <-printed, runErr
}

// The events a job's history and dead-letter counts are read from are the
// events of the cluster the job was read from. `adhar batch` connects by
// --kubeconfig or $KUBECONFIG; the bridge used to be reached through the
// default kubeconfig whatever those said, so `history` showed another
// cluster's job of the same name and `dlq --replay` would have started ops
// Jobs here for executions that only exist there.
func TestHistoryAndDeadLettersReadTheEventsOfTheClusterTheyConnectedTo(t *testing.T) {
	prevPath := helpers.KubeConfigPath
	t.Cleanup(func() { helpers.KubeConfigPath = prevPath })
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("HOME", t.TempDir())

	elsewhere := &clusterDouble{bus: true}
	helpers.KubeConfigPath = kubeconfigAt(t, elsewhere.serve(t).URL)

	failed := batchEvent(t, platformevents.TypeBatchRunFailed, "2026-11-01T02:00:00Z", map[string]interface{}{
		"run": map[string]interface{}{"name": "importer-aaaaaaaa"}, "springBatch": map[string]interface{}{"executionId": 7}, "counts": map[string]interface{}{"deadLettered": 5}})
	here := &clusterDouble{bus: true, events: []platformevents.Event{failed}}
	t.Setenv("KUBECONFIG", kubeconfigAt(t, here.serve(t).URL))

	out, err := executeBatch(t, "history", "importer", "-n", "prod", "-o", "json")
	if err != nil || !strings.Contains(out, platformevents.TypeBatchRunFailed) {
		t.Fatalf("history of the connected cluster: %v\n%s", err, out)
	}
	if len(here.subscribed) != 1 || here.subscribed[0] != platformevents.TopicBatch || here.deleted != 1 {
		t.Errorf("the connected cluster's bridge: subscribed %v, consumers deleted %d", here.subscribed, here.deleted)
	}

	here.events = []platformevents.Event{failed}
	out, err = executeBatch(t, "dlq", "importer", "-n", "prod", "-o", "json")
	var report dlqReport
	if jerr := json.Unmarshal([]byte(out), &report); err != nil || jerr != nil || report.Events != "read" || report.Total != 5 {
		t.Fatalf("dlq counts the connected cluster's events: %v %v\n%s", err, jerr, out)
	}
	for _, call := range here.requests {
		if strings.Contains(call, "/consumers/") && !strings.Contains(call, bridgeProxyPath+"/consumers/") {
			t.Errorf("%s left the service proxy: the bridge's base_uri was used as given", call)
		}
	}
	if len(elsewhere.requests) != 0 {
		t.Errorf("the default kubeconfig's cluster was asked for this job's events: %v", elsewhere.requests)
	}

	// The same commands where the bus is not installed (the local profile):
	// history says so and points at what the cluster still holds; dlq counts
	// the runs and says the events were not available, in one line.
	bare := &clusterDouble{}
	t.Setenv("KUBECONFIG", kubeconfigAt(t, bare.serve(t).URL))
	out, err = executeBatch(t, "history", "importer", "-n", "prod", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "no event bridge on this cluster") || !strings.Contains(err.Error(), "`adhar batch runs importer`") || strings.TrimSpace(out) != "" {
		t.Errorf("history without the bus: %v\n%s", err, out)
	}
	out, err = executeBatch(t, "dlq", "importer", "-n", "prod", "-o", "json")
	report = dlqReport{}
	if jerr := json.Unmarshal([]byte(out), &report); err != nil || jerr != nil || !strings.HasPrefix(report.Events, "unavailable: no event bridge on this cluster") || strings.Contains(report.Events, "\n") {
		t.Errorf("dlq without the bus: %v %v\n%s", err, jerr, out)
	}
}

// The query the bus is asked carries the cluster, the one topic and exactly
// the job's subject.
func TestBatchHistoryQueryNamesTheClusterTheTopicAndTheSubject(t *testing.T) {
	cfg := &rest.Config{Host: "https://cluster.example"}
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	q := eventBus{cluster: cfg, bridge: "http://127.0.0.1:8080"}.historyQuery("prod/importer", since, 50)
	if q.Cluster != cfg || q.Bridge != "http://127.0.0.1:8080" || len(q.Topics) != 1 || q.Topics[0] != platformevents.TopicBatch || !q.Since.Equal(since) || q.Limit != 50 {
		t.Errorf("query = %+v", q)
	}
	if !q.Keep(platformevents.Event{Subject: "prod/importer"}) || q.Keep(platformevents.Event{Subject: "prod/importer-eu"}) {
		t.Error("the query keeps exactly the job's subject")
	}
}
