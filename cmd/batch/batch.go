/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package batch is `adhar batch`: the batch jobs of design 0025 from the
// terminal — the same objects the Console's Batch area reads and writes, with
// nothing invented beside them.
//
// A BatchJob (platform.adhar.io/v1alpha1, namespaced) is declared in Git and
// composed by the control plane into a CronJob, a run template and alert
// rules. A RUN is a plain Job labelled adhar.io/batch-job, created from that
// template (`ConfigMap <name>-run`) by the clock, by this command or by the
// Console, and named for its identifying parameters so the same identity can
// never run twice. What a person may do to a run is what the Job API offers:
// suspend, unsuspend, create a restart; and the ONE thing they may change on
// the BatchJob is its hold annotation. Everything else is a pull request.
package batch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// The labels, annotations and variables of the run contract (design 0025 §6.4,
// §6.5), spelled once. They are the Console's `LABEL`, `ANNOTATION` and `ENV`.
const (
	labelBatchJob        = "adhar.io/batch-job"
	labelTrigger         = "adhar.io/batch-trigger"
	labelRole            = "adhar.io/batch-role"
	labelNativeJob       = "batch.kubernetes.io/job-name"
	labelNativeJobLegacy = "job-name"
	labelNativeIndex     = "batch.kubernetes.io/job-completion-index"
	labelVolcanoJob      = "volcano.sh/job-name"
	labelVolcanoIndex    = "volcano.sh/task-index"

	annotationHold      = "adhar.io/batch-hold"
	annotationActor     = "adhar.io/batch-actor"
	annotationScheduled = "batch.kubernetes.io/cronjob-scheduled-timestamp"

	envParams    = "ADHAR_BATCH_PARAMS"
	envTrigger   = "ADHAR_BATCH_TRIGGER"
	envRestartOf = "ADHAR_BATCH_RESTART_OF"
	envTimeZone  = "ADHAR_BATCH_TIME_ZONE"

	// batchContainer is the container the runner lives in (§6.4).
	batchContainer = "batch"

	// defaultProject is the platform's own Project, whose environments are the
	// pinned dev/test/prod namespaces; `adhar apps deploy` defaults to it too.
	defaultProject = "adhar"
)

var (
	batchJobGVR   = helpers.XRGVR("batchjobs")
	projectGVR    = helpers.XRGVR("projects")
	jobGVR        = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	volcanoJobGVR = schema.GroupVersionResource{Group: "batch.volcano.sh", Version: "v1alpha1", Resource: "jobs"}
	commandGVR    = schema.GroupVersionResource{Group: "bus.volcano.sh", Version: "v1alpha1", Resource: "commands"}
	podGroupGVR   = schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}
	queueGVR      = schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "queues"}
	podGVR        = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	configMapGVR  = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
)

var (
	namespaceFlag string
	envFlag       string
	projectFlag   string
	outputFlag    string

	// now is the clock, replaceable by tests so ages and "today" are fixed.
	now = time.Now
)

// BatchCmd is `adhar batch`.
var BatchCmd = &cobra.Command{
	Use:     "batch",
	Aliases: []string{"batchjob", "batchjobs"},
	Short:   "Run and operate the platform's batch jobs",
	Long: `Batch jobs are Spring Batch applications declared in Git as a BatchJob and
run by the platform as one Kubernetes Job per attempt. The schedule, the
image and the parameters a job accepts come from its definition; this command
starts, watches, stops, resumes and restarts runs, and holds a schedule
without a commit — the same operations the Console offers, on the same objects.

A run is named for its identifying parameters, <job>-<hash8>, so starting the
same identity twice shows the existing run instead of running it again; a
restart of a failed run is <job>-<hash8>-r<n>.

Where Volcano is installed, a partitioned job runs as a Volcano Job whose
manager and workers start together or not at all: its runs show the queue they
wait in and their gang, stop and resume become Volcano commands, and
` + "`queues`" + ` shows each team's queues and what they hold.

A BatchJob lives in an environment namespace. Name it with -n, or with --env
and --project, which resolve the namespace from the Project's environments.

Examples:
  adhar batch list --env prod                   # every job in the prod environment
  adhar batch run importer -p businessDate=2026-11-03 --wait --follow
  adhar batch runs importer --status failed     # the runs that failed
  adhar batch logs importer-09932cb3 -f         # the run's pod logs
  adhar batch stop importer-09932cb3            # SIGTERM at the next chunk boundary
  adhar batch restart importer-09932cb3         # the same identity, only the failed steps
  adhar batch hold importer --reason "upstream feed late"
  adhar batch queues --team acme-payments       # the team's Volcano queues and their usage`,
}

func init() {
	BatchCmd.PersistentFlags().StringVarP(&namespaceFlag, "namespace", "n", "", "Namespace of the job (default: the "+globals.DefaultEnvironment()+" environment)")
	BatchCmd.PersistentFlags().StringVar(&envFlag, "env", "", "Environment (dev, test, prod) whose namespace to use, resolved from the Project")
	BatchCmd.PersistentFlags().StringVar(&projectFlag, "project", defaultProject, "Project whose environments --env names")
	BatchCmd.PersistentFlags().StringVarP(&outputFlag, "output", "o", "table", "Output format (table, json, yaml)")

	BatchCmd.AddCommand(listCmd, describeCmd, runsCmd, runCmd, logsCmd, stopCmd, resumeCmd, restartCmd, holdCmd, releaseCmd, queuesCmd)
}

func ctx(cmd *cobra.Command) context.Context {
	if cmd != nil && cmd.Context() != nil {
		return cmd.Context()
	}
	return context.Background()
}

// clients are the two clients this command needs: the dynamic one for every
// object (BatchJobs, Jobs, pods, ConfigMaps are all read as unstructured, the
// way the Console reads them) and the typed one for pod logs, which the dynamic
// client cannot stream.
type clients struct {
	dc dynamic.Interface
	cs kubernetes.Interface
}

// connect resolves the kubeconfig the way `adhar upgrade` does — --kubeconfig,
// then $KUBECONFIG, then ~/.kube/config — so the context `adhar up` merged and
// an explicit cluster file both work.
func connect(cmd *cobra.Command) (*clients, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kc, _ := cmd.Flags().GetString("kubeconfig"); kc != "" {
		loadingRules.ExplicitPath = kc
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not connect to the cluster (is it running? try `adhar up`): %w", err)
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}
	return &clients{dc: dc, cs: cs}, nil
}

// resolveNamespace is where the command looks. -n names a namespace directly;
// --env names an environment of --project and the Project's status says which
// namespace that is (`status.environments[]`, the single place applications and
// the paved-road Tasks look it up too). With neither, the dev environment, as
// `adhar apps` defaults.
func resolveNamespace(c context.Context, dc dynamic.Interface, cmd *cobra.Command) (string, error) {
	nsSet := cmd.Flags().Changed("namespace") && namespaceFlag != ""
	envSet := cmd.Flags().Changed("env") && envFlag != ""
	if nsSet && envSet {
		return "", fmt.Errorf("-n and --env both name a namespace; use one of them")
	}
	if nsSet {
		return namespaceFlag, nil
	}
	if !envSet {
		return globals.DefaultEnvironment(), nil
	}
	project, err := dc.Resource(projectGVR).Namespace(globals.AdharSystemNamespace).Get(c, projectFlag, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading Project %s to resolve --env %s: %w", projectFlag, envFlag, err)
	}
	return environmentNamespace(project, envFlag)
}

// environmentNamespace reads the namespace of environment env from the
// Project's resolved environments. A project that has not published them yet
// (status still empty) or that has no such environment is an error that names
// what exists, not a guess: a guessed namespace is where the job is NOT.
func environmentNamespace(project *unstructured.Unstructured, env string) (string, error) {
	envs, _, _ := unstructured.NestedSlice(project.Object, "status", "environments")
	var names []string
	for _, e := range envs {
		m, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		ns, _ := m["namespace"].(string)
		names = append(names, name)
		if name == env && ns != "" {
			return ns, nil
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("project %s has not published its environments yet; name the namespace with -n", project.GetName())
	}
	return "", fmt.Errorf("project %s has no environment %q (it has %s)", project.GetName(), env, strings.Join(names, ", "))
}

// parseRef accepts `<name>` or `<namespace>/<name>`, the deep-link form the
// Console uses for a job or a run, so a name copied from there works as is.
func parseRef(ref, defaultNamespace string) (namespace, name string) {
	if ns, n, ok := strings.Cut(ref, "/"); ok && ns != "" && n != "" {
		return ns, n
	}
	return defaultNamespace, ref
}

// getBatchJob reads one BatchJob, with a message that says where it looked.
func getBatchJob(c context.Context, dc dynamic.Interface, namespace, name string) (*unstructured.Unstructured, error) {
	job, err := dc.Resource(batchJobGVR).Namespace(namespace).Get(c, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("BatchJob %s was not found in %s (`adhar batch list -A` shows every job)", name, namespace)
		}
		return nil, fmt.Errorf("reading BatchJob %s/%s: %w", namespace, name, err)
	}
	return job, nil
}

// getRun reads one run: a native Job first, then a Volcano Job when that API
// is served. Neither is an error that names both.
func getRun(c context.Context, dc dynamic.Interface, namespace, name string) (*unstructured.Unstructured, error) {
	run, err := dc.Resource(jobGVR).Namespace(namespace).Get(c, name, metav1.GetOptions{})
	if err == nil {
		if run.GetLabels()[labelBatchJob] == "" {
			return nil, fmt.Errorf("%s/%s is a Job, but not a batch run (it carries no %s label)", namespace, name, labelBatchJob)
		}
		return run, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("reading run %s/%s: %w", namespace, name, err)
	}
	vc, verr := dc.Resource(volcanoJobGVR).Namespace(namespace).Get(c, name, metav1.GetOptions{})
	if verr == nil {
		// The same gate as a Job: a Volcano Job that is not a batch run (a
		// team's own training job) must not be stopped, resumed or restarted
		// as one — stop would send it an AbortJob.
		if vc.GetLabels()[labelBatchJob] == "" {
			return nil, fmt.Errorf("%s/%s is a Volcano Job, but not a batch run (it carries no %s label)", namespace, name, labelBatchJob)
		}
		return vc, nil
	}
	return nil, fmt.Errorf("run %s was not found in %s (`adhar batch runs <job>` lists a job's runs)", name, namespace)
}

// listRuns lists every run in a namespace ("" for all), native and — when the
// API is served — Volcano, by the adhar.io/batch-job label, and the pods that
// carry their summaries. Pods that cannot be read are reported as unknown,
// not absent: exit codes then show as missing rather than as success.
func listRuns(c context.Context, dc dynamic.Interface, namespace, selector string) (runs []unstructured.Unstructured, pods []unstructured.Unstructured, podsUnknown bool, err error) {
	jobs, err := dc.Resource(jobGVR).Namespace(namespace).List(c, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, nil, false, fmt.Errorf("listing runs: %w", err)
	}
	runs = append(runs, jobs.Items...)
	// Volcano arrives with Phase 2 and is optional: its absence is a fact, not a failure.
	if vc, verr := dc.Resource(volcanoJobGVR).Namespace(namespace).List(c, metav1.ListOptions{LabelSelector: selector}); verr == nil {
		runs = append(runs, vc.Items...)
	}
	podList, perr := dc.Resource(podGVR).Namespace(namespace).List(c, metav1.ListOptions{LabelSelector: selector})
	if perr != nil {
		return runs, nil, true, nil
	}
	return runs, podList.Items, false, nil
}

// runsOfJob lists one job's runs as rows, newest first.
func runsOfJob(c context.Context, dc dynamic.Interface, namespace, job string) ([]runRow, bool, error) {
	runs, pods, podsUnknown, err := listRuns(c, dc, namespace, labelBatchJob+"="+job)
	if err != nil {
		return nil, false, err
	}
	rows := runRowsOf(runs, pods, now())
	attachGangs(c, dc, namespace, rows)
	return rows, podsUnknown, nil
}

// readRunTemplate reads and checks `ConfigMap <name>-run` (or the name the
// BatchJob's status reports).
func readRunTemplate(c context.Context, dc dynamic.Interface, job *unstructured.Unstructured) (*runTemplate, error) {
	name, _, _ := unstructured.NestedString(job.Object, "status", "runTemplate")
	if name == "" {
		name = job.GetName() + "-run"
	}
	cm, err := dc.Resource(configMapGVR).Namespace(job.GetNamespace()).Get(c, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("the run template %s/%s does not exist yet; the control plane creates it when it reconciles this BatchJob", job.GetNamespace(), name)
		}
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("reading the run template needs get configmaps in %s", job.GetNamespace())
		}
		return nil, fmt.Errorf("reading the run template %s/%s: %w", job.GetNamespace(), name, err)
	}
	return parseRunTemplate(cm)
}

// createRun creates a run and tells an identity that already exists apart
// from any other failure: §3.4 says show it, and never start another.
var errRunExists = errors.New("a run with this identity already exists")

func createRun(c context.Context, dc dynamic.Interface, run *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	created, err := dc.Resource(gvrOfRun(run)).Namespace(run.GetNamespace()).Create(c, run, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, errRunExists
		}
		return nil, fmt.Errorf("creating run %s: %w", run.GetName(), err)
	}
	return created, nil
}

func machineReadable() bool { return outputFlag == "json" || outputFlag == "yaml" }

func printMachine(v interface{}) error {
	if outputFlag == "yaml" {
		return helpers.PrintYAML(v)
	}
	return helpers.PrintJSON(v)
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// phaseCell renders a run phase with the CLI's one icon vocabulary.
func phaseCell(p runPhase) string {
	switch p {
	case phaseSucceeded:
		return helpers.StateReady(p.label())
	case phaseFailed:
		return helpers.StateFailed(p.label())
	case phaseStopped:
		return helpers.StateDisabled(p.label())
	case phaseUnknown:
		return helpers.StateUnknown(p.label())
	default:
		return helpers.StatePending(p.label())
	}
}
