package domain

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
)

func testCert(t *testing.T, names []string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"test"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     names,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})
}

func edgeSecret(certPEM, keyPEM []byte, annotations map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: globals.SelfSignedCertSecretName, Namespace: globals.AdharSystemNamespace, Annotations: annotations, Labels: map[string]string{"controller.cert-manager.io/fao": "true"}},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
	}
}

func edgeClient(t *testing.T, objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

var acmeAnnotations = map[string]string{
	"cert-manager.io/issuer-name":  "adhar-letsencrypt-dns",
	"cert-manager.io/issuer-kind":  "ClusterIssuer",
	"cert-manager.io/issuer-group": "cert-manager.io",
	"cert-manager.io/alt-names":    "*.platform.example,*.dev.platform.example",
}

// Only an ACME-issued certificate is worth caching: the bootstrap self-signed
// Secret has no issuer annotation and would never be adopted anyway.
func TestOnlyAnAcmeIssuedEdgeCertificateIsCached(t *testing.T) {
	t.Setenv(EdgeCertCacheDirEnv, t.TempDir())
	names := []string{"*.platform.example", "*.dev.platform.example"}
	certPEM, keyPEM := testCert(t, names, time.Now().Add(60*24*time.Hour))

	ec, err := SaveEdgeCertificate(context.Background(), edgeClient(t, edgeSecret(certPEM, keyPEM, map[string]string{"cert-manager.io/alt-names": "x"})))
	if err != nil || ec != nil {
		t.Fatalf("the self-signed bootstrap Secret was cached (%v, %v)", ec, err)
	}
	ec, err = SaveEdgeCertificate(context.Background(), edgeClient(t, edgeSecret(certPEM, keyPEM, map[string]string{"cert-manager.io/issuer-name": "adhar-selfsigned"})))
	if err != nil || ec != nil {
		t.Fatalf("a Secret from the self-signed ClusterIssuer was cached (%v, %v)", ec, err)
	}
	ec, err = SaveEdgeCertificate(context.Background(), edgeClient(t, edgeSecret(certPEM, keyPEM, acmeAnnotations)))
	if err != nil {
		t.Fatal(err)
	}
	if ec == nil || ec.Host != "platform.example" || ec.Issuer != "adhar-letsencrypt-dns" {
		t.Fatalf("the ACME-issued Secret was not cached under its host: %+v", ec)
	}
	dir, _ := EdgeCertificateCacheDir()
	st, err := os.Stat(filepath.Join(dir, "platform.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("the cache file holds a private key and is %v, want 0600", st.Mode().Perm())
	}
}

// A restored Secret carries the issuer annotations, or cert-manager reports
// IncorrectIssuer and orders a new certificate — the very thing the cache exists
// to avoid.
func TestARestoredEdgeCertificateIsAdoptableByCertManager(t *testing.T) {
	t.Setenv(EdgeCertCacheDirEnv, t.TempDir())
	names := []string{"*.platform.example", "*.dev.platform.example", "*.test.platform.example", "*.prod.platform.example"}
	certPEM, keyPEM := testCert(t, names, time.Now().Add(60*24*time.Hour))
	if _, err := SaveEdgeCertificate(context.Background(), edgeClient(t, edgeSecret(certPEM, keyPEM, acmeAnnotations))); err != nil {
		t.Fatal(err)
	}

	fresh := edgeClient(t)
	// The Gateway listeners are wildcards; the apex is not among the certificate's names and is not asked for.
	ec, err := RestoreEdgeCertificate(context.Background(), fresh, "platform.example", []string{"*.platform.example", "*.dev.platform.example"})
	if err != nil {
		t.Fatal(err)
	}
	if ec == nil {
		t.Fatal("a valid cached certificate covering the platform names was not restored")
	}
	got := &corev1.Secret{}
	if err := fresh.Get(context.Background(), client.ObjectKey{Name: globals.SelfSignedCertSecretName, Namespace: globals.AdharSystemNamespace}, got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations["cert-manager.io/issuer-name"] != "adhar-letsencrypt-dns" || got.Type != corev1.SecretTypeTLS || string(got.Data[corev1.TLSPrivateKeyKey]) != string(keyPEM) {
		t.Fatalf("the restored Secret is not the issued one with its annotations: %+v", got.ObjectMeta)
	}

	// A cluster that already has the Secret is left alone.
	if ec, err := RestoreEdgeCertificate(context.Background(), fresh, "platform.example", []string{"*.platform.example"}); err != nil || ec != nil {
		t.Fatalf("an existing Secret was overwritten (%v, %v)", ec, err)
	}
	// Names the cache does not cover are not served by it.
	if ec, err := RestoreEdgeCertificate(context.Background(), edgeClient(t), "platform.example", []string{"*.other.example"}); err != nil || ec != nil {
		t.Fatalf("a certificate that does not cover the requested names was restored (%v, %v)", ec, err)
	}
	// No cache for the host: nothing happens, no error.
	if ec, err := RestoreEdgeCertificate(context.Background(), edgeClient(t), "elsewhere.example", []string{"*.elsewhere.example"}); err != nil || ec != nil {
		t.Fatalf("a missing cache produced (%v, %v)", ec, err)
	}
}

// An expiring certificate is neither saved nor restored.
func TestAnExpiringEdgeCertificateIsNotReused(t *testing.T) {
	t.Setenv(EdgeCertCacheDirEnv, t.TempDir())
	certPEM, keyPEM := testCert(t, []string{"*.platform.example"}, time.Now().Add(2*time.Hour))
	if ec, err := SaveEdgeCertificate(context.Background(), edgeClient(t, edgeSecret(certPEM, keyPEM, acmeAnnotations))); err != nil || ec != nil {
		t.Fatalf("a certificate with two hours left was cached (%v, %v)", ec, err)
	}
}
