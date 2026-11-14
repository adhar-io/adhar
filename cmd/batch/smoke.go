/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// The smoke run (design 0025 D6): the run a promotion into a test environment
// waits for. The Project composition's Kargo analysis runs exactly
// `adhar batch run <job> -n <ns> --smoke --wait -o json` and passes the build
// when the CLI exits 0, so everything the gate means is decided here:
//
//   - the parameters are the BatchJob's spec.parameters.smoke.parameters, the
//     rest take their declared defaults, and the trigger is `smoke`;
//   - the run is `<job>-smoke-<sha7>`, sha7 the first 7 hex characters of the
//     image digest IN THE RUN TEMPLATE, so the same build verified again finds
//     its run (D2: a completed one passes, a failed one is retried as -r<n>);
//   - the template must already carry the BatchJob's image. Kargo changes the
//     image in Git, the composition rewrites the template a reconcile later,
//     and a gate that named its run from the OLD template would find the old
//     build's successful smoke run and pass the new build unverified.

var (
	// smokeTemplateWait bounds the wait for the template to carry the new
	// image; smokePoll is how often it is re-read. Tests shorten both.
	smokeTemplateWait = 5 * time.Minute
	smokePoll         = 5 * time.Second

	imageDigest = regexp.MustCompile(`@sha256:([0-9a-f]{64})$`)
)

// smokeRunName is `<job>-smoke-<sha7>` for the template's image. An image that
// is not pinned to a digest has no identity to name a build by, so the smoke
// run is refused rather than named for a tag that moves.
func smokeRunName(batchJob, image string) (string, error) {
	m := imageDigest.FindStringSubmatch(image)
	if m == nil {
		return "", fmt.Errorf("the run template's image %q is not pinned to a digest; a smoke run is named for the build's digest (D6), so promote an image@sha256 reference", image)
	}
	return batchJob + "-smoke-" + m[1][:7], nil
}

// smokeValues reads spec.parameters.smoke.parameters; false when the BatchJob
// declares no smoke run at all.
func smokeValues(job *unstructured.Unstructured) (map[string]string, bool) {
	smoke, ok, _ := unstructured.NestedMap(job.Object, "spec", "parameters", "smoke")
	if !ok {
		return nil, false
	}
	values := map[string]string{}
	params, _ := smoke["parameters"].(map[string]interface{})
	for k, v := range params {
		if v != nil {
			values[k] = fmt.Sprint(v)
		}
	}
	return values, true
}

// currentRunTemplate reads the run template once it carries the BatchJob's
// image (spec.parameters.image, which the composition copies verbatim), and
// fails after smokeTemplateWait rather than verify a template that is behind.
func currentRunTemplate(c context.Context, dc dynamic.Interface, job *unstructured.Unstructured) (*runTemplate, error) {
	want, _, _ := unstructured.NestedString(job.Object, "spec", "parameters", "image")
	deadline := time.Now().Add(smokeTemplateWait)
	for {
		t, err := readRunTemplate(c, dc, job)
		if err != nil {
			return nil, err
		}
		have := runImage(t.Manifest)
		if want == "" || have == want {
			return t, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the run template of %s still carries %s, not the BatchJob's %s, after %s: the control plane has not reconciled the new build, and a smoke run named from the old template would verify the old build", job.GetName(), have, want, smokeTemplateWait)
		}
		select {
		case <-c.Done():
			return nil, c.Err()
		case <-time.After(smokePoll):
		}
	}
}

// startSmokeRun is `adhar batch run --smoke`.
func startSmokeRun(c context.Context, dc dynamic.Interface, logs logSource, ns, name string, wait bool, timeout time.Duration) (runOutcome, error) {
	job, err := getBatchJob(c, dc, ns, name)
	if err != nil {
		return runOutcome{}, err
	}
	values, declared := smokeValues(job)
	if !declared {
		return runOutcome{}, fmt.Errorf("BatchJob %s/%s declares no smoke run (spec.parameters.smoke)", ns, name)
	}
	t, err := currentRunTemplate(c, dc, job)
	if err != nil {
		return runOutcome{}, err
	}
	if t.ParamsError != "" {
		return runOutcome{}, fmt.Errorf("the run template's parameters cannot be read: %s", t.ParamsError)
	}
	resolved := resolveParams(t.Fields, values, envOf(t.Manifest, envTimeZone), now())
	if len(resolved.Errors) > 0 {
		return runOutcome{}, fmt.Errorf("the smoke parameters are refused (spec.parameters.smoke.parameters): %s", strings.Join(resolved.problems(), "; "))
	}
	base, err := smokeRunName(name, runImage(t.Manifest))
	if err != nil {
		return runOutcome{}, err
	}
	spec := runSpec{Base: base, Trigger: triggerSmoke, RestartTrigger: triggerSmoke,
		build: func(n, trigger, restartOf string) *unstructured.Unstructured {
			return buildRun(t, n, ns, trigger, resolved.Params, restartOf)
		}}
	return executeRun(c, dc, logs, job, spec, wait, timeout)
}
