/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// How a job's work is spread (design 0025 §4.5, §5.10, §5.11, D11). `partitioned`
// and `chunked` are the same run SHAPE — one manager and N workers, an Indexed Job
// natively or a Volcano gang — so the run object's kind cannot tell them apart; the
// definition's `mode` does, and on a run the env the composition puts on every pod,
// ADHAR_BATCH_DISTRIBUTION=chunked.
const (
	modeSingle      = "single"
	modePartitioned = "partitioned"
	modeChunked     = "chunked"
	envDistribution = "ADHAR_BATCH_DISTRIBUTION"
)

// jobModeOf is the definition's mode; the XRD's default is single.
func jobModeOf(job *unstructured.Unstructured) string {
	if m, _, _ := unstructured.NestedString(job.Object, "spec", "parameters", "mode"); m != "" {
		return m
	}
	return modeSingle
}

// runModeOf reads a run object: a single run is one pod; a run of the manager-and-
// workers shape is chunked when its pods say so, partitioned otherwise.
func runModeOf(run *unstructured.Unstructured) string {
	if envOf(run, envDistribution) == modeChunked {
		return modeChunked
	}
	if runKindOf(run) == "VolcanoJob" {
		return modePartitioned
	}
	if cm, _, _ := unstructured.NestedString(run.Object, "spec", "completionMode"); cm == "Indexed" {
		return modePartitioned
	}
	return modeSingle
}

// modeDetail is the describe line: the mode, and what it means for this job.
func modeDetail(job *unstructured.Unstructured) string {
	mode := jobModeOf(job)
	workers, found, _ := unstructured.NestedInt64(job.Object, "spec", "parameters", "workers")
	if !found {
		workers = 2 // the XRD's default (D25)
	}
	switch mode {
	case modePartitioned:
		grid, found, _ := unstructured.NestedInt64(job.Object, "spec", "parameters", "gridSize")
		if !found {
			grid = 4 * workers
		}
		return fmt.Sprintf("partitioned: 1 manager + %d workers, %d partitions over the JobRepository's database", workers, grid)
	case modeChunked:
		req, _, _ := unstructured.NestedString(job.Object, "spec", "parameters", "chunking", "requestQueue")
		rep, _, _ := unstructured.NestedString(job.Object, "spec", "parameters", "chunking", "replyQueue")
		return fmt.Sprintf("chunked: 1 manager + %d workers, remote chunking over RabbitMQ (chunks on Queue %s, replies on Queue %s)", workers, dash(req), dash(rep))
	default:
		return mode
	}
}

// lineageDetail is the describe line for OpenLineage (D11); "" when it is off.
func lineageDetail(job *unstructured.Unstructured) string {
	on, _, _ := unstructured.NestedBool(job.Object, "spec", "parameters", "lineage", "enabled")
	if !on {
		return ""
	}
	url, _, _ := unstructured.NestedString(job.Object, "spec", "parameters", "lineage", "url")
	if url == "" {
		url = "the platform's OpenMetadata"
	}
	parts := []string{"OpenLineage to " + url}
	if secret, _, _ := unstructured.NestedString(job.Object, "spec", "parameters", "lineage", "credentialsSecretRef", "name"); secret != "" {
		parts = append(parts, "token from Secret "+secret)
	}
	return strings.Join(parts, ", ")
}
