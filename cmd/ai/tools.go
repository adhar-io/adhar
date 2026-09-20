/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"
)

// The built-in tool set.
//
// SAFETY IS STRUCTURAL, NOT ADVISORY. Every tool below is a read, and the write
// path is one tool (`propose_change`) that produces a reviewable proposal and
// touches nothing. There is no apply, patch, delete, scale, exec or sync tool
// anywhere in this file, so no prompt — however cleverly worded, however
// convincingly it claims to be the operator — can make the loop mutate the
// cluster. That mirrors the platform's own guarantee: the adhar-ai
// ServiceAccount holds get/list/watch and nothing else.
//
// Reads still run as YOU: the tools use the caller's kubeconfig, so the agent can
// never see more than the person running it can. Secret VALUES are redacted even
// then (redactSecret below) — a model does not need a credential to explain why a
// pod is crash-looping, and anything put into a prompt leaves the cluster.

// toolResultLimit caps what one tool returns. A 9,000-pod list or a 50 MB log
// would blow the context window and the platform token budget in one call, and
// the truncation notice tells the model to narrow its next query rather than
// letting it reason over a silently clipped answer.
const toolResultLimit = 12000

type tool struct {
	name        string
	description string
	params      map[string]interface{} // JSON Schema
	write       bool
	run         func(ctx context.Context, t *toolbox, args map[string]interface{}) (string, error)
}

// toolbox is the execution context shared by every tool.
type toolbox struct {
	p       *platform
	mapper  *restmapper.DeferredDiscoveryRESTMapper
	repoDir string // the adhar checkout, when the CLI is run from one; "" otherwise
	calls   int
	// proposals collects what propose_change produced, so the command can print
	// them together at the end instead of interleaving diffs with reasoning.
	proposals []proposal
}

type proposal struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	Title     string `json:"title"`
	Rationale string `json:"rationale"`
	Content   string `json:"content"`
}

func newToolbox(p *platform) *toolbox {
	tb := &toolbox{p: p}
	if dc, err := discovery.NewDiscoveryClientForConfig(p.rest); err == nil {
		tb.mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
	}
	tb.repoDir = findRepoDir()
	return tb
}

// builtinTools returns the tool set for a run at the given autonomy level.
// Write tools are ABSENT from the list at read-only, not merely refused: a model
// cannot be tempted by a tool it was never shown.
func builtinTools(level string, ac agentConfig) []tool {
	ts := []tool{
		toolKubeGet, toolKubeList, toolPodIssues, toolPodLogs, toolEvents,
		toolArgoApps, toolPlatformStatus, toolDocsSearch, toolPackageInfo,
	}
	if ok, _, _, _ := ac.writesAllowed(level); ok {
		ts = append(ts, toolProposeChange)
	}
	return ts
}

func specsFor(ts []tool) []toolSpec {
	out := make([]toolSpec, 0, len(ts))
	for _, t := range ts {
		var s toolSpec
		s.Type = "function"
		s.Function.Name = t.name
		s.Function.Description = t.description
		s.Function.Parameters = t.params
		out = append(out, s)
	}
	return out
}

func objSchema(required []string, props map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": props, "required": required}
}

func strProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func intProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "integer", "description": desc}
}

// ---------------------------------------------------------------------------
// Cluster reads
// ---------------------------------------------------------------------------

var toolKubeGet = tool{
	name: "kube_get",
	description: "Read one Kubernetes object as YAML. Use for a specific named resource. " +
		"Secret values are redacted to key names; never ask for a credential.",
	params: objSchema([]string{"kind", "name"}, map[string]interface{}{
		"kind":       strProp("Kind, e.g. Deployment, Pod, Application, AdharPlatform"),
		"name":       strProp("Object name"),
		"namespace":  strProp("Namespace; omit for cluster-scoped kinds (defaults to adhar-system)"),
		"apiVersion": strProp("Optional apiVersion when the kind is ambiguous, e.g. argoproj.io/v1alpha1"),
	}),
	run: func(ctx context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		kind, name := str(args, "kind"), str(args, "name")
		gvr, namespaced, err := tb.resolve(kind, str(args, "apiVersion"))
		if err != nil {
			return "", err
		}
		ri := tb.p.dyn.Resource(gvr)
		ns := namespaceOr(args, tb.p.ns)
		var obj interface{}
		if namespaced {
			o, err := ri.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			obj = redact(o.Object)
		} else {
			o, err := ri.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			obj = redact(o.Object)
		}
		out, err := yaml.Marshal(obj)
		if err != nil {
			return "", err
		}
		return string(out), nil
	},
}

var toolKubeList = tool{
	name: "kube_list",
	description: "List objects of a kind, returning name, status summary and age. " +
		"Prefer this over kube_get when exploring; then read the interesting one.",
	params: objSchema([]string{"kind"}, map[string]interface{}{
		"kind":          strProp("Kind to list, e.g. Pod, Deployment, Application, Certificate"),
		"namespace":     strProp("Namespace (defaults to adhar-system); pass \"all\" for every namespace"),
		"labelSelector": strProp("Optional label selector, e.g. app.kubernetes.io/name=keycloak"),
		"limit":         intProp("Maximum objects to return (default 60)"),
		"apiVersion":    strProp("Optional apiVersion when the kind is ambiguous"),
	}),
	run: func(ctx context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		gvr, namespaced, err := tb.resolve(str(args, "kind"), str(args, "apiVersion"))
		if err != nil {
			return "", err
		}
		limit := intOr(args, "limit", 60)
		opts := metav1.ListOptions{LabelSelector: str(args, "labelSelector"), Limit: int64(limit)}
		ri := tb.p.dyn.Resource(gvr)
		ns := namespaceOr(args, tb.p.ns)
		var items []map[string]interface{}
		if namespaced && !strings.EqualFold(ns, "all") {
			l, err := ri.Namespace(ns).List(ctx, opts)
			if err != nil {
				return "", err
			}
			for _, it := range l.Items {
				items = append(items, it.Object)
			}
		} else {
			l, err := ri.List(ctx, opts)
			if err != nil {
				return "", err
			}
			for _, it := range l.Items {
				items = append(items, it.Object)
			}
		}
		if len(items) == 0 {
			return "no objects found", nil
		}
		var b strings.Builder
		for _, o := range items {
			fmt.Fprintf(&b, "%s\n", summarize(o))
		}
		return b.String(), nil
	},
}

var toolPodIssues = tool{
	name: "pod_issues",
	description: "List pods that are NOT healthy, with the container state, reason, exit code and restart count. " +
		"This is the fastest first call when investigating anything broken.",
	params: objSchema(nil, map[string]interface{}{
		"namespace": strProp("Namespace (defaults to adhar-system); \"all\" for every namespace"),
		"selector":  strProp("Optional label selector to narrow to one app"),
	}),
	run: func(ctx context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		ns := namespaceOr(args, tb.p.ns)
		if strings.EqualFold(ns, "all") {
			ns = ""
		}
		pods, err := tb.p.clients.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: str(args, "selector")})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		var unhealthy int
		for i := range pods.Items {
			pod := &pods.Items[i]
			if line := podProblem(pod); line != "" {
				unhealthy++
				fmt.Fprintf(&b, "%s\n", line)
			}
		}
		if unhealthy == 0 {
			return fmt.Sprintf("all %d pods are healthy", len(pods.Items)), nil
		}
		return fmt.Sprintf("%d of %d pods unhealthy:\n%s", unhealthy, len(pods.Items), b.String()), nil
	},
}

var toolPodLogs = tool{
	name:        "pod_logs",
	description: "Read the tail of a pod's logs. Pass previous=true to read the log of a container that already crashed.",
	params: objSchema(nil, map[string]interface{}{
		"pod":       strProp("Pod name; or use selector instead"),
		"selector":  strProp("Label selector, when the exact pod name is unknown"),
		"namespace": strProp("Namespace (defaults to adhar-system)"),
		"container": strProp("Container name, for a multi-container pod"),
		"tailLines": intProp("Lines from the end (default 80, max 400)"),
		"previous":  map[string]interface{}{"type": "boolean", "description": "Read the previous, crashed container instead of the running one"},
	}),
	run: func(ctx context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		ns := namespaceOr(args, tb.p.ns)
		name := str(args, "pod")
		if name == "" {
			sel := str(args, "selector")
			if sel == "" {
				return "", fmt.Errorf("pass either pod or selector")
			}
			pod, err := tb.p.pickPod(ctx, sel)
			if err != nil {
				// A crash-looping pod is not Running, and that is exactly the pod
				// whose logs are wanted — fall back to the first match.
				pods, lerr := tb.p.clients.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
				if lerr != nil || len(pods.Items) == 0 {
					return "", err
				}
				name = pods.Items[0].Name
			} else {
				name = pod.Name
			}
		}
		tail := int64(intOr(args, "tailLines", 80))
		if tail > 400 {
			tail = 400
		}
		req := tb.p.clients.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{
			Container: str(args, "container"),
			TailLines: &tail,
			Previous:  boolOr(args, "previous", false),
		})
		rc, err := req.Stream(ctx)
		if err != nil {
			return "", err
		}
		defer func() { _ = rc.Close() }()
		buf := make([]byte, toolResultLimit)
		n, _ := rc.Read(buf)
		if n == 0 {
			return "(no log output)", nil
		}
		return string(buf[:n]), nil
	},
}

var toolEvents = tool{
	name:        "k8s_events",
	description: "Recent Kubernetes events for a namespace or one object. Events explain scheduling, image-pull, probe and volume failures that a pod's own status does not.",
	params: objSchema(nil, map[string]interface{}{
		"namespace": strProp("Namespace (defaults to adhar-system)"),
		"name":      strProp("Optional object name to filter on"),
		"limit":     intProp("Maximum events (default 40)"),
	}),
	run: func(ctx context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		ns := namespaceOr(args, tb.p.ns)
		opts := metav1.ListOptions{Limit: int64(intOr(args, "limit", 40))}
		if n := str(args, "name"); n != "" {
			opts.FieldSelector = "involvedObject.name=" + n
		}
		evs, err := tb.p.clients.CoreV1().Events(ns).List(ctx, opts)
		if err != nil {
			return "", err
		}
		if len(evs.Items) == 0 {
			return "no events", nil
		}
		sort.Slice(evs.Items, func(i, j int) bool {
			return evs.Items[i].LastTimestamp.Before(&evs.Items[j].LastTimestamp)
		})
		var b strings.Builder
		for _, e := range evs.Items {
			fmt.Fprintf(&b, "%s %s %s/%s: %s (x%d)\n", e.LastTimestamp.Format("15:04:05"), e.Type,
				e.InvolvedObject.Kind, e.InvolvedObject.Name, strings.TrimSpace(e.Message), e.Count)
		}
		return b.String(), nil
	},
}

var toolArgoApps = tool{
	name:        "argo_apps",
	description: "Sync and health state of the platform's Argo CD Applications — the authoritative view of which packages are converged. Omit name for the whole list.",
	params: objSchema(nil, map[string]interface{}{
		"name":      strProp("Optional application name, e.g. keycloak"),
		"unhealthy": map[string]interface{}{"type": "boolean", "description": "Only return applications that are not Synced+Healthy"},
	}),
	run: func(ctx context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
		if name := str(args, "name"); name != "" {
			o, err := tb.p.dyn.Resource(gvr).Namespace(tb.p.ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return appLine(o.Object, true), nil
		}
		l, err := tb.p.dyn.Resource(gvr).Namespace(tb.p.ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		onlyBad := boolOr(args, "unhealthy", false)
		var b strings.Builder
		var shown int
		for _, it := range l.Items {
			sync, _, _ := nestedString(it.Object, "status", "sync", "status")
			health, _, _ := nestedString(it.Object, "status", "health", "status")
			if onlyBad && sync == "Synced" && health == "Healthy" {
				continue
			}
			shown++
			b.WriteString(appLine(it.Object, false) + "\n")
		}
		if shown == 0 {
			return fmt.Sprintf("all %d applications are Synced and Healthy", len(l.Items)), nil
		}
		return fmt.Sprintf("%d of %d applications:\n%s", shown, len(l.Items), b.String()), nil
	},
}

var toolPlatformStatus = tool{
	name:        "platform_status",
	description: "The AdharPlatform resource's conditions and fleet rollup: the platform's own opinion of whether its foundation (Cilium, Gateway, Argo CD, Gitea, Crossplane, GitOps) is ready.",
	params:      objSchema(nil, map[string]interface{}{"name": strProp("Platform name (default adhar)")}),
	run: func(ctx context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		gvr := schema.GroupVersionResource{Group: "platform.adhar.io", Version: "v1alpha1", Resource: "adharplatforms"}
		name := str(args, "name")
		ri := tb.p.dyn.Resource(gvr).Namespace(tb.p.ns)
		if name == "" {
			l, err := ri.List(ctx, metav1.ListOptions{Limit: 1})
			if err != nil || len(l.Items) == 0 {
				return "", fmt.Errorf("no AdharPlatform found in %s", tb.p.ns)
			}
			out, _ := yaml.Marshal(map[string]interface{}{"status": l.Items[0].Object["status"]})
			return string(out), nil
		}
		o, err := ri.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		out, _ := yaml.Marshal(map[string]interface{}{"status": o.Object["status"]})
		return string(out), nil
	},
}

// ---------------------------------------------------------------------------
// Grounding: the platform's own documentation and package contracts
// ---------------------------------------------------------------------------

var toolDocsSearch = tool{
	name:        "docs_search",
	description: "Search Adhar's own documentation, ADRs and package notes for a term. Use this before proposing a change so the answer matches this platform's conventions rather than generic Kubernetes advice.",
	params: objSchema([]string{"query"}, map[string]interface{}{
		"query": strProp("Words to look for, e.g. \"oauth2-proxy cookie\" or \"sync wave\""),
		"limit": intProp("Maximum matching lines (default 30)"),
	}),
	run: func(_ context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		if tb.repoDir == "" {
			return "", fmt.Errorf("documentation grounding needs the adhar checkout: run this from the repository, or rely on cluster tools only")
		}
		terms := strings.Fields(strings.ToLower(str(args, "query")))
		if len(terms) == 0 {
			return "", fmt.Errorf("query is empty")
		}
		limit := intOr(args, "limit", 30)
		var hits []string
		roots := []string{filepath.Join(tb.repoDir, "docs"), filepath.Join(tb.repoDir, "platform", "stack", "packages")}
		for _, root := range roots {
			_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || len(hits) >= limit {
					return nil
				}
				if !strings.HasSuffix(path, ".md") {
					return nil
				}
				data, err := os.ReadFile(path) // #nosec G304 -- paths come from walking the repo's own docs tree
				if err != nil {
					return nil
				}
				rel, _ := filepath.Rel(tb.repoDir, path)
				for i, line := range strings.Split(string(data), "\n") {
					low := strings.ToLower(line)
					matched := true
					for _, t := range terms {
						if !strings.Contains(low, t) {
							matched = false
							break
						}
					}
					if matched {
						hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
						if len(hits) >= limit {
							return filepath.SkipAll
						}
					}
				}
				return nil
			})
		}
		if len(hits) == 0 {
			return "no documentation matches", nil
		}
		return strings.Join(hits, "\n"), nil
	},
}

var toolPackageInfo = tool{
	name:        "package_info",
	description: "The marketplace contract of one platform package (category, version, stability, dependencies, provenance) from its adhar-package.yaml.",
	params:      objSchema([]string{"name"}, map[string]interface{}{"name": strProp("Package name, e.g. keycloak, nexus, karmada")}),
	run: func(_ context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		if tb.repoDir == "" {
			return "", fmt.Errorf("package contracts need the adhar checkout; run this from the repository")
		}
		name := str(args, "name")
		var found string
		_ = filepath.WalkDir(filepath.Join(tb.repoDir, "platform", "stack", "packages"), func(path string, d os.DirEntry, err error) error {
			if err != nil || found != "" {
				return nil
			}
			if !d.IsDir() && d.Name() == "adhar-package.yaml" && filepath.Base(filepath.Dir(path)) == name {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found == "" {
			return "", fmt.Errorf("no package named %q", name)
		}
		data, err := os.ReadFile(found) // #nosec G304 -- resolved by walking the repo's own package tree
		if err != nil {
			return "", err
		}
		return string(data), nil
	},
}

// ---------------------------------------------------------------------------
// The one write tool
// ---------------------------------------------------------------------------

var toolProposeChange = tool{
	name:        "propose_change",
	description: "Propose a change to the platform as a reviewable file edit. This does NOT apply anything: it records a proposal the human will read, and can only target the GitOps repositories and paths the platform's write policy allows. Use it to answer \"fix it\" — never claim to have applied a change.",
	write:       true,
	params: objSchema([]string{"repo", "path", "content", "title", "rationale"}, map[string]interface{}{
		"repo":      strProp("GitOps repository: packages or environments"),
		"path":      strProp("Path within the repository, e.g. packages/data/metabase/manifests/install.yaml"),
		"content":   strProp("The complete proposed file content"),
		"title":     strProp("One-line summary, as a commit subject would read"),
		"rationale": strProp("Why this change fixes the problem, citing what you observed"),
	}),
	run: func(_ context.Context, tb *toolbox, args map[string]interface{}) (string, error) {
		pr := proposal{
			Repo:      str(args, "repo"),
			Path:      str(args, "path"),
			Title:     str(args, "title"),
			Rationale: str(args, "rationale"),
			Content:   str(args, "content"),
		}
		tb.proposals = append(tb.proposals, pr)
		return fmt.Sprintf("proposal recorded for %s/%s (%q). It has NOT been applied: the human running this will review it, and nothing reaches the cluster until they push it and Argo CD reconciles.", pr.Repo, pr.Path, pr.Title), nil
	},
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

// resolve maps a kind to a resource, preferring an explicit apiVersion. This is
// discovery-driven rather than a hardcoded table because the platform installs
// ~40 CRD groups and a fixed table would be wrong the moment a package is added.
func (tb *toolbox) resolve(kind, apiVersion string) (schema.GroupVersionResource, bool, error) {
	if tb.mapper == nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("cannot discover cluster APIs")
	}
	if kind == "" {
		return schema.GroupVersionResource{}, false, fmt.Errorf("kind is required")
	}
	gk := schema.GroupKind{Kind: kind}
	var versions []string
	if apiVersion != "" {
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return schema.GroupVersionResource{}, false, err
		}
		gk.Group, versions = gv.Group, []string{gv.Version}
	}
	m, err := tb.mapper.RESTMapping(gk, versions...)
	if err != nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("unknown kind %q: %w", kind, err)
	}
	return m.Resource, m.Scope.Name() == "namespace", nil
}

// redact strips Secret values. It keeps the key names and value lengths, which is
// what an investigation actually needs ("the secret exists but openaiApiKey is
// empty"), and leaves no credential to leak into a prompt.
func redact(obj map[string]interface{}) map[string]interface{} {
	kind, _ := obj["kind"].(string)
	if kind != "Secret" {
		return obj
	}
	return redactSecret(obj)
}

func redactSecret(obj map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range obj {
		out[k] = v
	}
	for _, field := range []string{"data", "stringData"} {
		raw, ok := out[field].(map[string]interface{})
		if !ok {
			continue
		}
		keys := make(map[string]interface{}, len(raw))
		for k, v := range raw {
			s, _ := v.(string)
			keys[k] = fmt.Sprintf("<redacted, %d bytes encoded>", len(s))
		}
		out[field] = keys
	}
	return out
}

// summarize renders one object as a single line: name, the status fields that
// exist, and age. Kinds differ wildly, so this reads the few paths that are
// conventional rather than pretending to understand every CRD.
func summarize(o map[string]interface{}) string {
	name, _, _ := nestedString(o, "metadata", "name")
	ns, _, _ := nestedString(o, "metadata", "namespace")
	kind, _ := o["kind"].(string)
	parts := []string{fmt.Sprintf("%s/%s", strings.ToLower(kind), name)}
	if ns != "" {
		parts[0] = ns + " " + parts[0]
	}
	if phase, ok, _ := nestedString(o, "status", "phase"); ok {
		parts = append(parts, "phase="+phase)
	}
	if sync, ok, _ := nestedString(o, "status", "sync", "status"); ok {
		parts = append(parts, "sync="+sync)
	}
	if health, ok, _ := nestedString(o, "status", "health", "status"); ok {
		parts = append(parts, "health="+health)
	}
	if ready, ok := readyCondition(o); ok {
		parts = append(parts, "ready="+ready)
	}
	if ts, ok, _ := nestedString(o, "metadata", "creationTimestamp"); ok {
		parts = append(parts, "created="+ts)
	}
	return strings.Join(parts, " ")
}

func readyCondition(o map[string]interface{}) (string, bool) {
	status, ok := o["status"].(map[string]interface{})
	if !ok {
		return "", false
	}
	conds, ok := status["conditions"].([]interface{})
	if !ok {
		return "", false
	}
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == "Ready" || t == "Available" {
			s, _ := cm["status"].(string)
			if reason, _ := cm["reason"].(string); reason != "" && s != "True" {
				return s + "(" + reason + ")", true
			}
			return s, true
		}
	}
	return "", false
}

func appLine(o map[string]interface{}, full bool) string {
	name, _, _ := nestedString(o, "metadata", "name")
	sync, _, _ := nestedString(o, "status", "sync", "status")
	health, _, _ := nestedString(o, "status", "health", "status")
	rev, _, _ := nestedString(o, "status", "sync", "revision")
	line := fmt.Sprintf("%-28s sync=%-12s health=%s", name, orDash(sync), orDash(health))
	if msg, ok, _ := nestedString(o, "status", "health", "message"); ok && msg != "" {
		line += " · " + strings.TrimSpace(msg)
	}
	if full {
		if len(rev) > 7 {
			rev = rev[:7]
		}
		line += fmt.Sprintf("\nrevision=%s", rev)
		if conds, ok := o["status"].(map[string]interface{}); ok {
			if cl, ok := conds["conditions"].([]interface{}); ok {
				for _, c := range cl {
					cm, _ := c.(map[string]interface{})
					line += fmt.Sprintf("\ncondition %v: %v", cm["type"], cm["message"])
				}
			}
		}
		if op, ok, _ := nestedString(o, "status", "operationState", "phase"); ok {
			msg, _, _ := nestedString(o, "status", "operationState", "message")
			line += fmt.Sprintf("\noperation=%s %s", op, msg)
		}
	}
	return line
}

// podProblem returns a one-line description of what is wrong with a pod, or ""
// when it is healthy. Container state is where the real reason lives —
// CrashLoopBackOff, OOMKilled, ImagePullBackOff, a failing probe — and none of
// it appears in the pod phase.
func podProblem(pod *corev1.Pod) string {
	if pod.DeletionTimestamp != nil {
		return fmt.Sprintf("%s: terminating", pod.Name)
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return ""
	case corev1.PodRunning:
		ready := true
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status != corev1.ConditionTrue {
				ready = false
			}
		}
		if ready {
			return ""
		}
	}
	var details []string
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		switch {
		case cs.State.Waiting != nil:
			details = append(details, fmt.Sprintf("%s waiting %s: %s", cs.Name, cs.State.Waiting.Reason, strings.TrimSpace(cs.State.Waiting.Message)))
		case cs.State.Terminated != nil:
			details = append(details, fmt.Sprintf("%s terminated %s exit=%d", cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode))
		case !cs.Ready:
			details = append(details, fmt.Sprintf("%s running but not ready", cs.Name))
		}
		if cs.RestartCount > 0 {
			details = append(details, fmt.Sprintf("%s restarts=%d", cs.Name, cs.RestartCount))
		}
		if cs.LastTerminationState.Terminated != nil {
			t := cs.LastTerminationState.Terminated
			details = append(details, fmt.Sprintf("%s last exit=%d %s", cs.Name, t.ExitCode, t.Reason))
		}
	}
	if len(details) == 0 {
		details = append(details, string(pod.Status.Phase))
	}
	return fmt.Sprintf("%s/%s [%s]: %s", pod.Namespace, pod.Name, pod.Status.Phase, strings.Join(details, "; "))
}

// findRepoDir locates the adhar checkout by walking up from the working
// directory, so docs grounding works from any subdirectory and degrades to
// cluster-only tools when the CLI is installed rather than run from source.
func findRepoDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "platform", "stack", "packages")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func str(args map[string]interface{}, k string) string {
	s, _ := args[k].(string)
	return strings.TrimSpace(s)
}

func intOr(args map[string]interface{}, k string, def int) int {
	switch v := args[k].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func boolOr(args map[string]interface{}, k string, def bool) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return def
}

func namespaceOr(args map[string]interface{}, def string) string {
	if ns := str(args, "namespace"); ns != "" {
		return ns
	}
	return def
}

func nestedString(o map[string]interface{}, path ...string) (string, bool, error) {
	cur := interface{}(o)
	for _, p := range path {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur, ok = m[p]
		if !ok {
			return "", false, nil
		}
	}
	s, ok := cur.(string)
	return s, ok, nil
}

// ---------------------------------------------------------------------------
// `adhar ai tools`
// ---------------------------------------------------------------------------

var toolsCmd = &cobra.Command{
	Use:     "tools",
	Aliases: []string{"capabilities"},
	Short:   "List the tools the agent can call, and which are writes",
	Long: `List the tools an agentic run can call.

The built-in tools run locally against your kubeconfig, so the agent sees exactly
what you can see and no more. Federated MCP tools are listed too when the
platform's MCP servers are installed and reachable — those are the same tools the
Console chat and any external agent (an IDE, Claude Code) drive Adhar through.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		p, err := newPlatform()
		if err != nil {
			return err
		}
		ac := p.readAgentConfig(ctx)
		ts := builtinTools(ac.Level, ac)

		if flagJSON {
			type row struct {
				Name, Description string
				Write             bool
			}
			out := make([]row, 0, len(ts))
			for _, t := range ts {
				out = append(out, row{t.name, t.description, t.write})
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Println()
		fmt.Println(helpers.SectionHeading("🧰", "Agent tools"))
		fmt.Println()
		t := helpers.NewTable("TOOL", "KIND", "WHAT IT DOES")
		for _, tl := range ts {
			kind := helpers.StateReady("read")
			if tl.write {
				kind = helpers.StateDegraded("proposal")
			}
			t.Row(tl.name, kind, firstSentence(tl.description))
		}
		fmt.Println(t.Render())
		fmt.Printf("\n  autonomy %s (%s)\n", ac.Level, ac.source)
		if _, _, _, why := ac.writesAllowed(ac.Level); why != "" {
			fmt.Printf("  %s\n", why)
		}
		fmt.Println()
		hint("Federated MCP tools (when installed):  adhar ai mcp list")
		fmt.Println()
		return nil
	},
	SilenceUsage: true,
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}
