package webhooks

// Registering the validators with a controller-runtime manager.
//
// Until this existed the whole package was dead code: 400 lines of validators
// that nothing imported, no webhook server, and no ValidatingWebhookConfiguration
// — so every constraint they described was documentation. That is worse than
// having no validators, because the constraints read as enforced.
//
// Registration is CONDITIONAL on the TLS material being present. An admission
// webhook that is configured but cannot serve makes the API server reject the
// resources it guards, so a controller that started serving webhooks on a cluster
// with no certificate would take self-service down. Absent certs therefore mean
// "this cluster has not opted in", the controller logs that once, and everything
// else proceeds.

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Paths the ValidatingWebhookConfiguration points at. They are part of the
// contract with the manifest in
// platform/stack/packages/security/adhar-tenant-quotas, so they are named here
// rather than spelled twice.
const (
	PathValidateProject = "/validate-platform-adhar-io-v1alpha1-compositeproject"
	// DefaultCertDir is where cert-manager mounts the serving certificate.
	DefaultCertDir = "/tmp/k8s-webhook-server/serving-certs"
)

// Available reports whether this cluster has the serving certificate, i.e.
// whether the admission webhooks can be served at all.
func Available(certDir string) bool {
	if certDir == "" {
		certDir = DefaultCertDir
	}
	for _, f := range []string{"tls.crt", "tls.key"} {
		if _, err := os.Stat(filepath.Join(certDir, f)); err != nil {
			return false
		}
	}
	return true
}

// Register wires the admission handlers onto the manager's webhook server.
//
// Returns false when the certificate is absent, so the caller can log the reason
// rather than silently running without enforcement.
func Register(mgr manager.Manager, namespace, certDir string) (bool, error) {
	if certDir == "" {
		certDir = DefaultCertDir
	}
	if !Available(certDir) {
		return false, nil
	}

	srv := mgr.GetWebhookServer()
	if s, ok := srv.(*webhook.DefaultServer); ok {
		s.Options.CertDir = certDir
	}

	pv := &ProjectValidator{Client: mgr.GetClient(), Namespace: namespace}
	if err := pv.InjectDecoder(admission.NewDecoder(mgr.GetScheme())); err != nil {
		return false, fmt.Errorf("injecting the decoder: %w", err)
	}
	srv.Register(PathValidateProject, &admission.Webhook{Handler: pv})
	return true, nil
}
