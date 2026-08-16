/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package migrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const statusPending = "⏳ pending"

var (
	execute       bool
	dataPlaneName string
)

var dataPlaneGVR = schema.GroupVersionResource{
	Group:    "platform.adhar.io",
	Version:  "v1alpha1",
	Resource: "dataplanes",
}

var splitPlanesCmd = &cobra.Command{
	Use:   "split-planes",
	Short: "Migrate a dual-role cluster to control-plane / data-plane separation (ADR-0023)",
	Long: `Migrate a dual-role cluster to the control-plane / data-plane model (ADR-0023).

Today a single cluster runs both platform services and application workloads.
This migration separates the two roles in five reversible, ordered steps:

  1. Stand up a LOCAL data plane (a vcluster on the control plane) via a
     DataPlane CR (mode=vcluster, profile=standard).
  2. Register it with ArgoCD and converge the thin-agent profile
     (metrics-server, kyverno(+policies), alloy, external-secrets).
  3. Re-home application packages onto the data plane by editing placement
     bindings in the environments repo (a Git commit — you review and push).
  4. Wait for ArgoCD to reconcile apps onto the data plane and drain them from
     adhar-system.
  5. Flip the control-plane Kyverno policy (control-plane-no-apps) from Audit to
     Enforce so app workloads can never again land on the control plane.

Defaults to a DRY RUN: it inspects the cluster and prints the plan with each
step's current status. Pass --execute to create the local DataPlane CR (step 1);
the Git-side steps (3, 5) are always left for you to review and push, matching
Adhar's GitOps-first, no-surprise-commits workflow.`,
	RunE: runSplitPlanes,
}

func init() {
	splitPlanesCmd.Flags().BoolVar(&execute, "execute", false,
		"Create the local DataPlane CR (step 1). Without this, dry-run only.")
	splitPlanesCmd.Flags().StringVar(&dataPlaneName, "name", "local", "Name of the local data plane to create")
}

type step struct {
	n      int
	title  string
	status string // ✅ done | ⏳ pending | ⛔ blocked
	detail string
}

func runSplitPlanes(cmd *cobra.Command, args []string) error {
	client, err := getDynamicClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Inspect current state.
	crdInstalled := true
	existing, err := client.Resource(dataPlaneGVR).Get(ctx, dataPlaneName, metav1.GetOptions{})
	dpExists := err == nil
	if err != nil && isNoDataPlaneCRD(err) {
		crdInstalled = false
	}

	steps := []step{
		{1, fmt.Sprintf("Stand up local data plane %q (vcluster)", dataPlaneName), pendingOrDone(dpExists), ""},
		{2, "Register with ArgoCD + converge thin-agent profile", statusPending, "driven by the controller after step 1"},
		{3, "Re-home app packages via environments-repo placement (Git commit)", statusPending, "you review + push"},
		{4, "Wait for ArgoCD to drain apps off the control plane", statusPending, ""},
		{5, "Flip control-plane-no-apps Kyverno policy Audit → Enforce (Git commit)", statusPending, "you review + push"},
	}
	if dpExists {
		if ready := readyCond(existing); ready != "" {
			steps[1].status = statusPending
			steps[0].detail = "DataPlane Ready=" + ready
		}
	}
	if !crdInstalled {
		steps[0].status = "⛔ blocked"
		steps[0].detail = "DataPlane CRD not installed — run `adhar up`/`adhar upgrade` on a build that ships ADR-0023"
	}

	printPlan(steps)

	if !execute {
		logger.Info("Dry run. Re-run with --execute to create the local DataPlane CR (step 1).")
		return nil
	}
	if !crdInstalled {
		return fmt.Errorf("cannot execute: the DataPlane CRD is not installed on this cluster")
	}
	if dpExists {
		logger.Info(fmt.Sprintf(
			"DataPlane %q already exists — nothing to create. Steps 2-5 proceed via the controller and Git.",
			dataPlaneName))
		return nil
	}

	dp := localDataPlane(dataPlaneName)
	if _, err := client.Resource(dataPlaneGVR).Create(ctx, dp, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.Info(fmt.Sprintf("DataPlane %q already exists.", dataPlaneName))
			return nil
		}
		return fmt.Errorf("creating DataPlane %q: %w", dataPlaneName, err)
	}
	logger.Info(fmt.Sprintf("✅ Created DataPlane %q (mode=vcluster, profile=standard).", dataPlaneName))
	logger.Info("The DataPlane controller now drives steps 2 (register + agents). Watch it with `adhar get dataplanes`.")
	logger.Info("Next (Git, yours to push):")
	logger.Info("  • step 3 — add placement bindings under environments/<env>/placement.yaml " +
		"re-homing app packages to the data plane")
	logger.Info("  • step 5 — set control-plane-no-apps validationFailureAction: Enforce once apps have drained")
	return nil
}

// localDataPlane is the CR created for step 1: a vcluster data plane colocated on
// the control plane, giving T1 the same plane separation as a physical fleet.
func localDataPlane(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "platform.adhar.io/v1alpha1",
			"kind":       "DataPlane",
			"metadata": map[string]interface{}{
				"name": name,
				"labels": map[string]interface{}{
					"adhar.io/plane": "data",
				},
			},
			"spec": map[string]interface{}{
				"infrastructure": map[string]interface{}{
					"mode": "vcluster",
				},
				"profile": "standard",
				"placement": map[string]interface{}{
					"labels": map[string]interface{}{
						"tier": "local",
					},
				},
				"observability": map[string]interface{}{
					"hub": "adhar-mgmt",
				},
			},
		},
	}
}

func printPlan(steps []step) {
	fmt.Println()
	fmt.Println("  split-planes migration (ADR-0023)")
	fmt.Println("  " + strings.Repeat("─", 68))
	for _, s := range steps {
		line := fmt.Sprintf("  %d. %-58s %s", s.n, s.title, s.status)
		fmt.Println(line)
		if s.detail != "" {
			fmt.Printf("       └─ %s\n", s.detail)
		}
	}
	fmt.Println()
}

func pendingOrDone(done bool) string {
	if done {
		return "✅ done"
	}
	return statusPending
}

func readyCond(obj *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "Ready" {
			s, _ := m["status"].(string)
			return s
		}
	}
	return ""
}

func isNoDataPlaneCRD(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "could not find the requested resource") ||
		strings.Contains(msg, "the server could not find") ||
		apierrors.IsNotFound(err) && strings.Contains(msg, "dataplanes")
}

func getDynamicClient() (dynamic.Interface, error) {
	kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	return dynamic.NewForConfig(config)
}
