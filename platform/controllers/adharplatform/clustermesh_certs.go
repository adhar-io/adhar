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

package adharplatform

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// Cluster Mesh certificate lifetimes.
const (
	clusterMeshCertValidity = 3 * 365 * 24 * time.Hour
	clusterMeshCertRenewAt  = 30 * 24 * time.Hour
)

// clusterMeshCertSecrets are the four TLS Secrets the clustermesh-apiserver
// consumes. They are deliberately NOT taken from the rendered manifest: Helm
// stamps the cluster name into the etcd usernames it issues them for
// (`admin-<cluster>`, `local-<cluster>`), so the shipped copies are valid for
// exactly one cluster name. The apiserver's etcd-init container derives the
// admin username from `cilium-config`'s cluster-name at runtime, so on any
// cluster not called adhar-mgmt the baked admin certificate authenticates as a
// user that does not exist and the apiserver cannot write its own state.
var clusterMeshCertSecrets = []string{
	"clustermesh-apiserver-server-cert",
	"clustermesh-apiserver-admin-cert",
	"clustermesh-apiserver-local-cert",
	"clustermesh-apiserver-remote-cert",
}

// ensureClusterMeshCerts issues the clustermesh-apiserver's TLS material from
// the cluster's own `cilium-ca` — the CA every cilium agent already trusts and
// the one thing all members of a mesh must share. Certificates are reissued
// only when missing, not signed by the current CA, carrying the wrong common
// name, or within a month of expiry, so a steady-state reconcile does not churn
// the Secrets (and restart the apiserver) every pass.
func (r *AdharPlatformReconciler) ensureClusterMeshCerts(ctx context.Context, clusterName string) error {
	logger := log.FromContext(ctx)

	caSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: "cilium-ca", Namespace: globals.AdharSystemNamespace}, caSecret); err != nil {
		return fmt.Errorf("reading cilium-ca (Cilium must be installed before the cluster mesh): %w", err)
	}
	caCert, caKey, err := parseCA(caSecret.Data["ca.crt"], caSecret.Data["ca.key"])
	if err != nil {
		return err
	}
	caPEM := caSecret.Data["ca.crt"]

	svcDNS := "clustermesh-apiserver." + globals.AdharSystemNamespace + ".svc"
	specs := []struct {
		secret   string
		cn       string
		dnsNames []string
		ips      []net.IP
		server   bool
	}{
		{
			secret: "clustermesh-apiserver-server-cert",
			cn:     svcDNS,
			// `*.mesh.cilium.io` is how peers address this apiserver: the cilium
			// agents resolve `<cluster>.mesh.cilium.io` through a host alias to
			// whatever address the connect step recorded.
			dnsNames: []string{svcDNS, "*.mesh.cilium.io"},
			ips:      []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
			server:   true,
		},
		{secret: "clustermesh-apiserver-admin-cert", cn: "admin-" + clusterName},
		{secret: "clustermesh-apiserver-local-cert", cn: "local-" + clusterName},
		// The common "remote" user, which authMode "migration" (the chart
		// default) accepts from every peer regardless of its name.
		{secret: "clustermesh-apiserver-remote-cert", cn: "remote"},
	}

	for _, spec := range specs {
		existing := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: spec.secret, Namespace: globals.AdharSystemNamespace}, existing)
		if err == nil && certStillValid(existing, caCert, spec.cn) {
			continue
		}
		crtPEM, keyPEM, err := issueCert(caCert, caKey, spec.cn, spec.dnsNames, spec.ips, spec.server)
		if err != nil {
			return fmt.Errorf("issuing %s: %w", spec.secret, err)
		}
		secret := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      spec.secret,
				Namespace: globals.AdharSystemNamespace,
				Labels:    map[string]string{"adhar.io/component": "clustermesh-apiserver"},
			},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{"tls.crt": crtPEM, "tls.key": keyPEM, "ca.crt": caPEM},
		}
		if err := r.Patch(ctx, secret, client.Apply,
			client.FieldOwner(v1alpha1.FieldManager), client.ForceOwnership); err != nil {
			return fmt.Errorf("applying %s: %w", spec.secret, err)
		}
		logger.Info("Issued clustermesh certificate from cilium-ca", "secret", spec.secret, "commonName", spec.cn)
	}
	return nil
}

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *rsa.PrivateKey, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("cilium-ca has no PEM certificate")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing cilium-ca certificate: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("cilium-ca has no PEM private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err == nil {
		return cert, key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing cilium-ca private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("cilium-ca private key is %T, expected RSA", parsed)
	}
	return cert, key, nil
}

// certStillValid reports whether a Secret already holds a usable certificate:
// issued by the current CA, for the expected common name, and not close to
// expiry.
func certStillValid(secret *corev1.Secret, ca *x509.Certificate, commonName string) bool {
	block, _ := pem.Decode(secret.Data["tls.crt"])
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	if cert.Subject.CommonName != commonName {
		return false
	}
	if time.Now().Add(clusterMeshCertRenewAt).After(cert.NotAfter) {
		return false
	}
	return cert.CheckSignatureFrom(ca) == nil
}

func issueCert(ca *x509.Certificate, caKey *rsa.PrivateKey, commonName string, dnsNames []string, ips []net.IP, server bool) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	usages := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if server {
		usages = append(usages, x509.ExtKeyUsageServerAuth)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(clusterMeshCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usages,
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	crtPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return crtPEM, keyPEM, nil
}
