/*
Copyright 2026 The Adhar Authors.

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

package domain

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"
)

// The edge certificate cache.
//
// Let's Encrypt issues at most five certificates per EXACT set of names per
// week. Every fresh cloud cluster asked for a new one for the same four
// wildcards, so the sixth cluster of the week (three were built on one day,
// 2026-10-17) was refused with `429 rateLimited` and served the bootstrap
// self-signed certificate for hours. A certificate is not a property of a
// cluster; it is a property of the DOMAIN, and it is valid for ninety days.
//
// So `adhar down` (and the end of a successful `adhar up`) save the issued
// Secret — data AND the cert-manager annotations — under
// ~/.adhar/pki/acme/<host>.json, and the next `adhar up` for the same host
// restores it as the `adhar-cert` Secret before cert-manager ever sees the
// Certificate. cert-manager adopts an existing Secret whose annotations name
// the same issuer and whose certificate covers the requested names, and
// orders nothing until the renewal window; the self-signed bootstrap Secret
// could never be adopted because its issuer annotation is empty
// ("IncorrectIssuer"), which is why every cluster started from zero.

const (
	edgeCertIssuerAnnotation = "cert-manager.io/issuer-name"
	// edgeCertMinRemaining is the least validity a cached certificate must have
	// left to be worth restoring. cert-manager renews a restored certificate
	// inside its own renewal window anyway; this only keeps an expired file
	// from being installed.
	edgeCertMinRemaining = 24 * time.Hour
	// EdgeCertCacheDirEnv overrides the cache directory (tests, and operators
	// who keep ~/.adhar elsewhere).
	EdgeCertCacheDirEnv = "ADHAR_EDGE_CERT_CACHE"
)

// EdgeCertificate is one cached issued edge certificate.
type EdgeCertificate struct {
	Host        string            `json:"host"`
	Issuer      string            `json:"issuer"`
	SavedAt     time.Time         `json:"savedAt"`
	NotAfter    time.Time         `json:"notAfter"`
	DNSNames    []string          `json:"dnsNames"`
	Annotations map[string]string `json:"annotations"`
	Labels      map[string]string `json:"labels,omitempty"`
	Cert        []byte            `json:"cert"`
	Key         []byte            `json:"key"`
}

// EdgeCertificateCacheDir is where issued edge certificates are kept on the
// operator's machine: $HOME/.adhar/pki/acme unless EdgeCertCacheDirEnv is set.
func EdgeCertificateCacheDir() (string, error) {
	if d := os.Getenv(EdgeCertCacheDirEnv); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory for the edge certificate cache: %w", err)
	}
	return filepath.Join(home, ".adhar", "pki", "acme"), nil
}

func edgeCertPath(dir, host string) string { return filepath.Join(dir, host+".json") }

// SaveEdgeCertificate caches the cluster's edge certificate Secret when an
// ACME issuer signed it. It returns (nil, nil) when there is nothing worth
// saving: no Secret, the bootstrap self-signed one, or a certificate about to
// expire. The file is keyed by the host the certificate's names hang off.
func SaveEdgeCertificate(ctx context.Context, c client.Client) (*EdgeCertificate, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Name: globals.SelfSignedCertSecretName, Namespace: globals.AdharSystemNamespace}, secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading Secret %s: %w", globals.SelfSignedCertSecretName, err)
	}
	issuer, ok := acmeIssued(secret.Annotations)
	if !ok {
		return nil, nil
	}
	leaf, err := parseLeaf(secret.Data[corev1.TLSCertKey])
	if err != nil {
		return nil, fmt.Errorf("Secret %s holds no readable certificate: %w", globals.SelfSignedCertSecretName, err)
	}
	if time.Until(leaf.NotAfter) < edgeCertMinRemaining {
		return nil, nil
	}
	host := baseHost(leaf.DNSNames)
	if host == "" {
		return nil, nil
	}
	ec := &EdgeCertificate{
		Host:        host,
		Issuer:      issuer,
		SavedAt:     time.Now().UTC(),
		NotAfter:    leaf.NotAfter,
		DNSNames:    leaf.DNSNames,
		Annotations: secret.Annotations,
		Labels:      secret.Labels,
		Cert:        secret.Data[corev1.TLSCertKey],
		Key:         secret.Data[corev1.TLSPrivateKeyKey],
	}
	dir, err := EdgeCertificateCacheDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	body, err := json.MarshalIndent(ec, "", "  ")
	if err != nil {
		return nil, err
	}
	// The file holds a private key: 0600, written whole or not at all.
	tmp := edgeCertPath(dir, host) + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return nil, fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, edgeCertPath(dir, host)); err != nil {
		return nil, fmt.Errorf("saving %s: %w", edgeCertPath(dir, host), err)
	}
	return ec, nil
}

// RestoreEdgeCertificate installs the cached certificate for host as the
// `adhar-cert` Secret, with the cert-manager annotations it was saved with, so
// cert-manager adopts it instead of ordering a new one. It returns (nil, nil)
// when there is nothing to restore: no cache for the host, a cache that does
// not cover the names the platform needs, one about to expire, or a cluster
// that already has the Secret.
func RestoreEdgeCertificate(ctx context.Context, c client.Client, host string, sans []string) (*EdgeCertificate, error) {
	dir, err := EdgeCertificateCacheDir()
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(edgeCertPath(dir, host))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the edge certificate cache: %w", err)
	}
	ec := &EdgeCertificate{}
	if err := json.Unmarshal(body, ec); err != nil {
		return nil, fmt.Errorf("the edge certificate cache %s is unreadable: %w", edgeCertPath(dir, host), err)
	}
	if _, ok := acmeIssued(ec.Annotations); !ok || len(ec.Key) == 0 {
		return nil, nil
	}
	leaf, err := parseLeaf(ec.Cert)
	if err != nil {
		return nil, fmt.Errorf("the edge certificate cache %s holds no readable certificate: %w", edgeCertPath(dir, host), err)
	}
	if time.Until(leaf.NotAfter) < edgeCertMinRemaining || !certificateCovers(leaf, sans) {
		return nil, nil
	}
	if err := k8s.EnsureNamespace(ctx, c, globals.AdharSystemNamespace); err != nil {
		return nil, err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        globals.SelfSignedCertSecretName,
			Namespace:   globals.AdharSystemNamespace,
			Annotations: ec.Annotations,
			Labels:      ec.Labels,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{corev1.TLSCertKey: ec.Cert, corev1.TLSPrivateKeyKey: ec.Key},
	}
	if err := c.Create(ctx, secret); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("restoring Secret %s: %w", globals.SelfSignedCertSecretName, err)
	}
	return ec, nil
}

// acmeIssued reports whether the Secret's annotations say an issuer other than
// the bootstrap self-signed one produced it. cert-manager adopts a Secret only
// when these annotations match the Certificate's issuerRef, so they are also
// what a restore must carry.
func acmeIssued(annotations map[string]string) (string, bool) {
	issuer := annotations[edgeCertIssuerAnnotation]
	if issuer == "" || strings.Contains(strings.ToLower(issuer), "selfsign") {
		return issuer, false
	}
	return issuer, true
}

func parseLeaf(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

// baseHost is the zone the certificate's names hang off: the shortest name
// with any leading wildcard label removed (`*.platform.adhar.io` →
// `platform.adhar.io`).
func baseHost(names []string) string {
	host := ""
	for _, n := range names {
		n = strings.TrimPrefix(strings.ToLower(n), "*.")
		if n != "" && (host == "" || len(n) < len(host)) {
			host = n
		}
	}
	return host
}

func certificateCovers(cert *x509.Certificate, sans []string) bool {
	for _, want := range sans {
		if !certificateHasName(cert, want) {
			return false
		}
	}
	return true
}

func certificateHasName(cert *x509.Certificate, want string) bool {
	if strings.HasPrefix(want, "*.") {
		for _, have := range cert.DNSNames {
			if strings.EqualFold(have, want) {
				return true
			}
		}
		return false
	}
	return cert.VerifyHostname(want) == nil
}
