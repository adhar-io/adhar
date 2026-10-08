package adharplatform

// Adopting a Cilium the platform did not install.
//
// WHAT WENT WRONG, so the shape of this file makes sense. On 2026-10-07 a live
// Civo `clusterMode: managed` bring-up hung at the Argo CD stage with every pod
// in the cluster Pending. Civo's managed k3s runs Cilium — because the Civo
// provider ASKS for it (`CNIPlugin: "cilium"`) — and the platform then applied
// its own Cilium into `adhar-system` on top of it. The two installs share
// cluster-scoped object NAMES, and the platform applies with Server-Side Apply
// and ForceOwnership, so:
//
//   - the ClusterRoleBinding `cilium` ended up with exactly one subject,
//     `adhar-system:cilium`, and the cluster's own agent (running as
//     `kube-system:cilium`) started failing with
//     `namespaces "kube-system" is forbidden`,
//   - every one of the cluster's Cilium agents and its operator crash-looped,
//     so the cluster had NO working CNI,
//   - CoreDNS, the Civo CCM and metrics-server went Pending, the CCM therefore
//     never cleared `node.cloudprovider.kubernetes.io/uninitialized`, a node
//     stayed NotReady, nothing could schedule, Argo CD was never installed, and
//     `adhar up` waited for it forever,
//   - and the platform's own Cilium pods were Pending too ("didn't have free
//     ports for the requested pod ports"), because the cluster's Cilium already
//     held them.
//
// Installing a second Cilium does not give you two Ciliums. It gives you none.
//
// The fix is in ReconcileCilium: never install when a foreign Cilium exists, in
// ANY mode. This file is what happens next — the cluster's Cilium becomes the
// platform's data path, which needs two things to be true, plus one repair for
// clusters an earlier release already broke.

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/globals"
)

const (
	// ciliumConfigMapName is the agent's configuration, in the namespace the
	// cluster's own Cilium runs in.
	ciliumConfigMapName = "cilium-config"

	// restartedAtAnnotation is what `kubectl rollout restart` writes; the same
	// annotation is used here so a human reading the workload sees a familiar
	// reason for the restart.
	restartedAtAnnotation = "adhar.io/restartedAt"
)

// ciliumGatewayAPIConfig is the configuration the platform Gateway needs from
// whichever Cilium owns the data path. These are the same keys the platform's
// own install carries.
//
// `gateway-api-secrets-namespace` points at the platform namespace because the
// Gateway's `certificateRefs` name `adhar-cert` there — cert-manager writes it
// into the namespace the Gateway lives in, and Cilium reads the TLS material
// from the namespace named here.
var ciliumGatewayAPIConfig = map[string]string{
	"enable-gateway-api":              "true",
	"enable-gateway-api-secrets-sync": "true",
	"gateway-api-secrets-namespace":   globals.AdharSystemNamespace,
}

// ciliumSharedBindings are the cluster-scoped RoleBindings both installs name
// identically, mapped to the ServiceAccount each one binds. These are the
// objects a second install silently takes over.
var ciliumSharedBindings = map[string]string{
	"cilium":          "cilium",
	"cilium-operator": "cilium-operator",
}

// adoptForeignCilium makes the cluster's own Cilium serve the platform, and
// undoes what an earlier release did to it.
//
// Only for a cluster whose lifecycle is OURS. On a `provided` cluster the Cilium
// belongs to the operator: the preflight refuses one with Gateway API disabled
// rather than reconfiguring it, and nothing here runs.
//
// Every step is a no-op when it is already satisfied, because this runs on every
// reconcile — and a step that restarted the cluster's CNI on every pass would be
// worse than the bug it fixes.
func (r *AdharPlatformReconciler) adoptForeignCilium(ctx context.Context, ciliumNamespace string) error {
	logger := log.FromContext(ctx)

	repaired, err := r.repairCiliumRoleBindings(ctx, ciliumNamespace)
	if err != nil {
		return fmt.Errorf("repairing the cluster's Cilium RBAC: %w", err)
	}

	configured, err := r.enableGatewayAPIOnForeignCilium(ctx, ciliumNamespace)
	if err != nil {
		return fmt.Errorf("enabling Gateway API on the cluster's Cilium: %w", err)
	}

	// Config and RBAC only take effect on a restart: the agent reads
	// cilium-config at start-up, and a crash-looping agent has to be given the
	// chance to come back now that its permissions exist again.
	if repaired || configured {
		if err := r.restartCilium(ctx, ciliumNamespace); err != nil {
			return fmt.Errorf("restarting the cluster's Cilium: %w", err)
		}
	}

	// Finally, remove the platform's OWN Cilium if a previous run installed it.
	// It can never become ready — the cluster's Cilium holds the host ports — and
	// while it exists it keeps three Pending pods per node and a second operator
	// competing for the same cluster-scoped state.
	removed, err := r.removePlatformCilium(ctx)
	if err != nil {
		return fmt.Errorf("removing the platform's own Cilium: %w", err)
	}
	if len(removed) > 0 {
		logger.Info("Removed the platform's own Cilium; the cluster's own install owns the data path",
			"removed", removed)
	}
	return nil
}

// repairCiliumRoleBindings puts the cluster's own Cilium ServiceAccounts back on
// the ClusterRoleBindings a previous install took over.
//
// It ADDS a subject; it never rewrites the list. The platform's own
// ServiceAccounts may still be there (harmless — those pods are about to be
// removed), and the one thing that must not happen here is a repair that locks
// out whoever else legitimately uses the binding.
func (r *AdharPlatformReconciler) repairCiliumRoleBindings(ctx context.Context, ciliumNamespace string) (bool, error) {
	if ciliumNamespace == "" || ciliumNamespace == globals.AdharSystemNamespace {
		return false, nil
	}
	logger := log.FromContext(ctx)
	changed := false

	for name, serviceAccount := range ciliumSharedBindings {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			binding := &rbacv1.ClusterRoleBinding{}
			if err := r.Get(ctx, client.ObjectKey{Name: name}, binding); err != nil {
				if apierrors.IsNotFound(err) {
					return nil
				}
				return err
			}
			for _, s := range binding.Subjects {
				if s.Kind == rbacv1.ServiceAccountKind &&
					s.Name == serviceAccount && s.Namespace == ciliumNamespace {
					return nil // already bound
				}
			}
			binding.Subjects = append(binding.Subjects, rbacv1.Subject{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      serviceAccount,
				Namespace: ciliumNamespace,
			})
			if err := r.Update(ctx, binding); err != nil {
				return err
			}
			changed = true
			logger.Info("Restored the cluster's Cilium ServiceAccount on a shared ClusterRoleBinding",
				"clusterRoleBinding", name, "serviceAccount", ciliumNamespace+"/"+serviceAccount)
			return nil
		})
		if err != nil {
			return changed, fmt.Errorf("patching ClusterRoleBinding %s: %w", name, err)
		}
	}
	return changed, nil
}

// enableGatewayAPIOnForeignCilium turns Gateway API on in the cluster's own
// Cilium, because without it the platform Gateway is never Programmed and not
// one platform URL answers — and the symptom gives no hint that a ConfigMap key
// in another namespace is the cause.
//
// Civo's managed k3s ships Cilium with Gateway API DISABLED (measured
// 2026-10-07: `cilium-config` carries no `enable-gateway-api` key at all), so
// this is the common case, not an edge one.
func (r *AdharPlatformReconciler) enableGatewayAPIOnForeignCilium(ctx context.Context, ciliumNamespace string) (bool, error) {
	if ciliumNamespace == "" {
		return false, nil
	}
	logger := log.FromContext(ctx)
	changed := false

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm := &corev1.ConfigMap{}
		key := client.ObjectKey{Name: ciliumConfigMapName, Namespace: ciliumNamespace}
		if err := r.Get(ctx, key, cm); err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		needs := false
		for k, v := range ciliumGatewayAPIConfig {
			if cm.Data[k] != v {
				cm.Data[k] = v
				needs = true
			}
		}
		if !needs {
			return nil
		}
		if err := r.Update(ctx, cm); err != nil {
			return err
		}
		changed = true
		logger.Info("Gateway API enabled on the cluster's own Cilium",
			"configMap", ciliumNamespace+"/"+ciliumConfigMapName)
		return nil
	})
	if err != nil {
		return changed, err
	}
	return changed, nil
}

// restartCilium rolls the cluster's Cilium agents and operator so they pick up
// the configuration and permissions above.
//
// A crash-looping agent does NOT need this to recover — the kubelet retries it
// anyway — but the operator and a healthy-but-misconfigured agent do, and
// waiting out a CrashLoopBackOff backoff is minutes of a bring-up.
func (r *AdharPlatformReconciler) restartCilium(ctx context.Context, ciliumNamespace string) error {
	stamp := time.Now().UTC().Format(time.RFC3339)

	for _, name := range []string{"cilium", "cilium-envoy"} {
		ds := &appsv1.DaemonSet{}
		key := types.NamespacedName{Name: name, Namespace: ciliumNamespace}
		if err := r.Get(ctx, key, ds); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if ds.Spec.Template.Annotations == nil {
			ds.Spec.Template.Annotations = map[string]string{}
		}
		ds.Spec.Template.Annotations[restartedAtAnnotation] = stamp
		if err := r.Update(ctx, ds); err != nil {
			return fmt.Errorf("restarting DaemonSet %s/%s: %w", ciliumNamespace, name, err)
		}
	}

	operator := &appsv1.Deployment{}
	key := types.NamespacedName{Name: "cilium-operator", Namespace: ciliumNamespace}
	if err := r.Get(ctx, key, operator); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if operator.Spec.Template.Annotations == nil {
		operator.Spec.Template.Annotations = map[string]string{}
	}
	operator.Spec.Template.Annotations[restartedAtAnnotation] = stamp
	if err := r.Update(ctx, operator); err != nil {
		return fmt.Errorf("restarting Deployment %s/cilium-operator: %w", ciliumNamespace, err)
	}
	return nil
}

// platformCiliumWorkloads are the workloads the platform's own Cilium install
// creates in the platform namespace. Only these — nothing in the cluster's own
// namespace is ever deleted here.
var platformCiliumWorkloads = struct {
	daemonSets  []string
	deployments []string
}{
	daemonSets:  []string{"cilium", "cilium-envoy"},
	deployments: []string{"cilium-operator", "hubble-relay", "hubble-ui"},
}

// removePlatformCilium deletes the platform's own Cilium workloads when the
// cluster's Cilium owns the data path.
//
// Workloads only. The RBAC, CRDs and ConfigMaps are left alone on purpose: they
// are shared by name with the cluster's install, and deleting them is how this
// whole incident started.
func (r *AdharPlatformReconciler) removePlatformCilium(ctx context.Context) ([]string, error) {
	var removed []string

	for _, name := range platformCiliumWorkloads.daemonSets {
		ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: globals.AdharSystemNamespace,
		}}
		if err := r.Delete(ctx, ds); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return removed, fmt.Errorf("deleting DaemonSet %s/%s: %w", globals.AdharSystemNamespace, name, err)
		}
		removed = append(removed, "daemonset/"+name)
	}
	for _, name := range platformCiliumWorkloads.deployments {
		deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: globals.AdharSystemNamespace,
		}}
		if err := r.Delete(ctx, deploy); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return removed, fmt.Errorf("deleting Deployment %s/%s: %w", globals.AdharSystemNamespace, name, err)
		}
		removed = append(removed, "deployment/"+name)
	}
	return removed, nil
}
