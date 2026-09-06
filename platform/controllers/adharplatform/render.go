package adharplatform

import "adhar-io/adhar/platform/utils/files"

// renderEmbedded executes the Go-template directives in an embedded foundation
// manifest against the platform's BuildCustomization (Host, PortSuffix, Port,
// Protocol, …). Every foundation manifest MUST pass through this before it is
// applied: the resources are fully templated (no hardcoded host/port), so
// applying the raw bytes ships literal `{{ .Host }}` into the cluster — which
// is exactly what broke argocd-server's URL/OIDC parsing on the first cloud
// bootstrap. gateway.go already did this inline; argocd/gitea now share it.
func (r *AdharPlatformReconciler) renderEmbedded(manifest []byte) ([]byte, error) {
	return files.ApplyTemplate(manifest, r.Config)
}
