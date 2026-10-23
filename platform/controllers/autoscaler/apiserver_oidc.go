package autoscaler

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/globals"
	pfactory "adhar-io/adhar/platform/providers"
)

// oidcRetryInterval spaces out attempts while Keycloak is not serving yet (or
// the master cannot be reached). One attempt costs a few seconds of SSH.
const oidcRetryInterval = 10 * time.Minute

// oidcAttemptTimeout bounds one attempt: the script waits up to ~3 minutes for
// the API server to come back after the manifest change.
const oidcAttemptTimeout = 8 * time.Minute

// ensureAPIServerOIDC makes a self-managed control plane trust Keycloak, once.
//
// `adhar up` tries this at the end of a cloud bring-up, but only ONCE and only
// as a warning: when Keycloak (or its public certificate and DNS) is not ready
// at that moment, the cluster is left with no OIDC trust and every Headlamp
// login fails on its first API call (DigitalOcean, 2026-10-23). The in-cluster
// manager has everything the step needs — the cluster SSH key mirrored into
// adhar-cluster-ssh, the provider, the API — so it finishes the job here and
// keeps retrying until it succeeds. Success is recorded in adhar-cluster-spec
// (pfactory.APIServerOIDCMarkerKey), after which this is a single cached read.
//
// Never fatal and never in the way of autoscaling: failures are logged and
// retried on the next interval.
func (r *Reconciler) ensureAPIServerOIDC(ctx context.Context) {
	if r.RESTConfig == nil {
		return
	}
	logger := log.FromContext(ctx).WithName("apiserver-oidc")

	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: globals.ClusterSpecConfigMapName, Namespace: r.Namespace}, cm); err != nil {
		return // not bootstrapped (or not a cluster the CLI built)
	}
	if cm.Data[pfactory.APIServerOIDCMarkerKey] != "" {
		return // done
	}
	if mode := cm.Data["clusterMode"]; mode != "" && mode != "compute" {
		return // managed or provided: the control plane is not ours to configure
	}

	now := r.Now()
	r.mu.Lock()
	if now.Before(r.oidcNextAttempt) {
		r.mu.Unlock()
		return
	}
	r.oidcNextAttempt = now.Add(oidcRetryInterval)
	r.mu.Unlock()

	spec, err := loadClusterSpec(ctx, r.Client, r.Namespace)
	if err != nil {
		logger.V(1).Info("cluster spec unreadable; will retry", "error", err.Error())
		return
	}
	providerFor := r.ProviderFor
	if providerFor == nil {
		providerFor = r.newProvider
	}
	prov, err := providerFor(ctx, spec)
	if err != nil {
		logger.Info("cannot build the provider for the OIDC trust yet; will retry", "error", err.Error())
		return
	}
	enable := r.EnableOIDC
	if enable == nil {
		enable = pfactory.EnableClusterOIDCFromREST
	}
	actx, cancel := context.WithTimeout(ctx, oidcAttemptTimeout)
	defer cancel()
	line, err := enable(actx, prov, spec.ClusterName, r.RESTConfig)
	if err != nil {
		logger.Info("API server does not trust Keycloak yet; will retry", "in", oidcRetryInterval.String(), "error", err.Error())
		return
	}
	logger.Info("API server OIDC trust applied", "result", line)
	if platform, perr := r.findPlatform(ctx); perr == nil && platform != nil {
		r.event(platform, corev1.EventTypeNormal, "APIServerOIDCConfigured", line)
	}
}
