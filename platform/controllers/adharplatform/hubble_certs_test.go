package adharplatform

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// testCA returns a fresh self-signed CA shaped like Cilium's `cilium-ca`.
func testCA(t *testing.T, cn string) (*corev1.Secret, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(5 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cilium-ca", Namespace: globals.AdharSystemNamespace},
		Data: map[string][]byte{
			"ca.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			"ca.key": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		},
	}, cert
}

func hubbleTestReconciler(t *testing.T, objs ...client.Object) *AdharPlatformReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &AdharPlatformReconciler{Client: c, Scheme: scheme}
}

func getSecret(t *testing.T, r *AdharPlatformReconciler, name string) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: globals.AdharSystemNamespace}, s); err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return s
}

func leaf(t *testing.T, s *corev1.Secret) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(s.Data["tls.crt"])
	if block == nil {
		t.Fatalf("%s has no certificate", s.Name)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestEnsureHubbleCertsIssuesForTheClusterName(t *testing.T) {
	caSecret, caCert := testCA(t, "Cilium CA")
	r := hubbleTestReconciler(t, caSecret)

	issued, err := r.ensureHubbleCerts(context.Background(), "adhar-prod")
	if err != nil {
		t.Fatal(err)
	}
	if !issued {
		t.Fatal("nothing was issued on a cluster with no Hubble certificates")
	}

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	server := getSecret(t, r, hubbleServerCertSecret)
	if server.Type != corev1.SecretTypeTLS {
		t.Errorf("server Secret type = %q", server.Type)
	}
	if !bytes.Equal(server.Data["ca.crt"], caSecret.Data["ca.crt"]) {
		t.Error("server Secret does not bundle cilium-ca as ca.crt (the agents use it as their client CA)")
	}
	sc := leaf(t, server)
	// What the relay actually verifies: the per-node name the peer service
	// hands out, and the peer service's own name.
	for _, host := range []string{"node-1.adhar-prod.hubble-grpc.cilium.io", "hubble-peer.adhar-prod.hubble-grpc.cilium.io"} {
		if _, err := sc.Verify(x509.VerifyOptions{DNSName: host, Roots: roots,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			t.Errorf("server certificate does not verify for %s: %v", host, err)
		}
	}
	if _, err := sc.Verify(x509.VerifyOptions{DNSName: "node-1.adhar-mgmt.hubble-grpc.cilium.io", Roots: roots}); err == nil {
		t.Error("server certificate is valid for the management cluster's name too")
	}

	relay := getSecret(t, r, hubbleRelayCertSecret)
	rc := leaf(t, relay)
	if rc.Subject.CommonName != hubbleRelayClientName {
		t.Errorf("relay client CN = %q, want %q", rc.Subject.CommonName, hubbleRelayClientName)
	}
	if _, err := rc.Verify(x509.VerifyOptions{Roots: roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("relay client certificate does not verify for clientAuth: %v", err)
	}
	for _, u := range rc.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			t.Error("relay client certificate is also a server certificate")
		}
	}
	if rc.NotAfter.After(caCert.NotAfter) || sc.NotAfter.After(caCert.NotAfter) {
		t.Error("a leaf outlives its CA")
	}
}

func TestEnsureHubbleCertsIsIdempotent(t *testing.T) {
	caSecret, _ := testCA(t, "Cilium CA")
	r := hubbleTestReconciler(t, caSecret)
	ctx := context.Background()

	if _, err := r.ensureHubbleCerts(ctx, "adhar-prod"); err != nil {
		t.Fatal(err)
	}
	before := getSecret(t, r, hubbleServerCertSecret).Data["tls.crt"]

	issued, err := r.ensureHubbleCerts(ctx, "adhar-prod")
	if err != nil {
		t.Fatal(err)
	}
	if issued {
		t.Error("valid certificates were reissued")
	}
	if !bytes.Equal(before, getSecret(t, r, hubbleServerCertSecret).Data["tls.crt"]) {
		t.Error("the server certificate changed on a steady-state pass")
	}
}

func TestEnsureHubbleCertsReissues(t *testing.T) {
	ctx := context.Background()

	t.Run("wrong cluster name (the baked adhar-mgmt certificate)", func(t *testing.T) {
		caSecret, _ := testCA(t, "Cilium CA")
		r := hubbleTestReconciler(t, caSecret)
		if _, err := r.ensureHubbleCerts(ctx, v1alpha1.DefaultClusterMeshName); err != nil {
			t.Fatal(err)
		}
		issued, err := r.ensureHubbleCerts(ctx, "adhar-prod")
		if err != nil || !issued {
			t.Fatalf("issued=%v err=%v; a rename must reissue the server certificate", issued, err)
		}
		if got := leaf(t, getSecret(t, r, hubbleServerCertSecret)).DNSNames; len(got) != 1 || got[0] != "*.adhar-prod.hubble-grpc.cilium.io" {
			t.Errorf("server SANs = %v", got)
		}
	})

	t.Run("right CN, wrong SAN", func(t *testing.T) {
		caSecret, caCert := testCA(t, "Cilium CA")
		_, caKey, err := parseCA(caSecret.Data["ca.crt"], caSecret.Data["ca.key"])
		if err != nil {
			t.Fatal(err)
		}
		name := hubbleServerName("adhar-prod")
		crt, key, err := issueCert(caCert, caKey, name, []string{"*.adhar-mgmt.hubble-grpc.cilium.io"}, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		r := hubbleTestReconciler(t, caSecret, tlsSecret(hubbleServerCertSecret, crt, key, caSecret.Data["ca.crt"]))
		if _, err := r.ensureHubbleCerts(ctx, "adhar-prod"); err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(crt, getSecret(t, r, hubbleServerCertSecret).Data["tls.crt"]) {
			t.Error("a certificate whose SAN does not match was kept (TLS ignores the CN)")
		}
	})

	t.Run("signed by another CA", func(t *testing.T) {
		caSecret, _ := testCA(t, "Cilium CA")
		otherSecret, _ := testCA(t, "Some Other CA")
		other := hubbleTestReconciler(t, otherSecret)
		if _, err := other.ensureHubbleCerts(ctx, "adhar-prod"); err != nil {
			t.Fatal(err)
		}
		foreign := getSecret(t, other, hubbleServerCertSecret)
		foreign.ResourceVersion = ""

		r := hubbleTestReconciler(t, caSecret, foreign)
		issued, err := r.ensureHubbleCerts(ctx, "adhar-prod")
		if err != nil || !issued {
			t.Fatalf("issued=%v err=%v; a certificate from another CA must be replaced", issued, err)
		}
		if !bytes.Equal(getSecret(t, r, hubbleServerCertSecret).Data["ca.crt"], caSecret.Data["ca.crt"]) {
			t.Error("the reissued Secret does not carry the current CA")
		}
	})

	t.Run("near expiry", func(t *testing.T) {
		caSecret, caCert := testCA(t, "Cilium CA")
		_, caKey, _ := parseCA(caSecret.Data["ca.crt"], caSecret.Data["ca.key"])
		crt, key := shortLivedCert(t, caCert, caKey, hubbleServerName("adhar-prod"), 10*24*time.Hour)
		r := hubbleTestReconciler(t, caSecret, tlsSecret(hubbleServerCertSecret, crt, key, caSecret.Data["ca.crt"]))
		if _, err := r.ensureHubbleCerts(ctx, "adhar-prod"); err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(crt, getSecret(t, r, hubbleServerCertSecret).Data["tls.crt"]) {
			t.Error("a certificate ten days from expiry was kept")
		}
	})
}

// The adhar-mgmt cluster keeps the certificate it was installed with: the
// baked one is valid for its name and CA, so the first operator pass after an
// upgrade must not reissue it.
func TestEnsureHubbleCertsKeepsTheBakedCertificateOnTheDefaultCluster(t *testing.T) {
	manifest, err := ciliumFS.ReadFile("resources/cilium/install.yaml")
	if err != nil {
		t.Fatal(err)
	}
	objs := []client.Object{}
	for _, name := range []string{"cilium-ca", hubbleServerCertSecret, hubbleRelayCertSecret} {
		s := manifestSecret(t, manifest, name)
		if s == nil {
			t.Fatalf("the embedded manifest has no %s Secret", name)
		}
		objs = append(objs, s)
	}
	if time.Now().Add(clusterMeshCertRenewAt).After(leaf(t, objs[1].(*corev1.Secret)).NotAfter) {
		t.Skip("the baked Hubble certificate is within its renewal window; reissuing is correct")
	}
	r := hubbleTestReconciler(t, objs...)
	issued, err := r.ensureHubbleCerts(context.Background(), v1alpha1.DefaultClusterMeshName)
	if err != nil {
		t.Fatal(err)
	}
	if issued {
		t.Error("the default cluster's (valid) baked Hubble certificates were reissued")
	}
}

func TestStripHubbleCertSecrets(t *testing.T) {
	manifest, err := ciliumFS.ReadFile("resources/cilium/install.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out := stripHubbleCertSecrets(manifest)

	for _, name := range hubbleCertSecrets {
		if manifestSecret(t, out, name) != nil {
			t.Errorf("%s is still in the applied manifest; every apply would restore the adhar-mgmt certificate", name)
		}
	}
	if manifestSecret(t, out, "cilium-ca") == nil {
		t.Error("cilium-ca was stripped; nothing could issue the certificates")
	}
	// The workloads that MOUNT the Secrets name them too, and must survive.
	for _, kept := range []string{"kind: DaemonSet\nmetadata:\n  name: cilium\n", "name: hubble-relay\n", "name: " + hubbleRelayConfigMap + "\n"} {
		if !strings.Contains(string(out), kept) {
			t.Errorf("stripping dropped a document containing %q", kept)
		}
	}
	if got, want := strings.Count(string(out), "\n---\n"), strings.Count(string(manifest), "\n---\n")-2; got != want {
		t.Errorf("stripping removed %d documents, want exactly 2", strings.Count(string(manifest), "\n---\n")-got)
	}
}

// The clustermesh strip used to match `name: <secret>` anywhere in a
// document, which also matched the apiserver Deployment's volumes.
func TestStripClusterMeshCertSecretsKeepsTheAPIServer(t *testing.T) {
	manifest, err := ciliumFS.ReadFile("resources/cilium/clustermesh.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out := string(stripClusterMeshCertSecrets(manifest))
	for _, name := range clusterMeshCertSecrets {
		if manifestSecret(t, []byte(out), name) != nil {
			t.Errorf("%s survived the strip", name)
		}
	}
	if !strings.Contains(out, "kind: Deployment") {
		t.Error("the clustermesh-apiserver Deployment was stripped along with its certificates")
	}
}

func TestReconcileHubbleTLS(t *testing.T) {
	ctx := context.Background()
	caSecret, _ := testCA(t, "Cilium CA")
	relayCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: hubbleRelayConfigMap, Namespace: globals.AdharSystemNamespace},
		Data:       map[string]string{"config.yaml": "cluster-name: adhar-mgmt\npeer-service: \"hubble-peer.adhar-system.svc.cluster.local.:443\"\n"},
	}
	relay := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: hubbleRelayDeployment, Namespace: globals.AdharSystemNamespace},
	}
	platform := &v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: globals.AdharSystemNamespace},
		Spec:       v1alpha1.AdharPlatformSpec{ClusterMesh: &v1alpha1.ClusterMeshSpec{Name: "adhar-prod", ID: 2}},
	}
	r := hubbleTestReconciler(t, caSecret, relayCM, relay)

	if err := r.reconcileHubbleTLS(ctx, platform); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	_ = r.Get(ctx, types.NamespacedName{Name: hubbleRelayConfigMap, Namespace: globals.AdharSystemNamespace}, cm)
	if !strings.HasPrefix(cm.Data["config.yaml"], "cluster-name: adhar-prod\npeer-service:") {
		t.Errorf("relay config not aligned: %q", cm.Data["config.yaml"])
	}
	d := &appsv1.Deployment{}
	_ = r.Get(ctx, types.NamespacedName{Name: hubbleRelayDeployment, Namespace: globals.AdharSystemNamespace}, d)
	stamp := d.Spec.Template.Annotations[hubblePKIAnnotation]
	if stamp == "" {
		t.Fatal("hubble-relay was not restarted after its identity changed")
	}

	// Steady state: nothing changes, nothing restarts.
	if err := r.reconcileHubbleTLS(ctx, platform); err != nil {
		t.Fatal(err)
	}
	_ = r.Get(ctx, types.NamespacedName{Name: hubbleRelayDeployment, Namespace: globals.AdharSystemNamespace}, d)
	if got := d.Spec.Template.Annotations[hubblePKIAnnotation]; got != stamp {
		t.Errorf("hubble-relay restarted on a steady-state pass (%s -> %s)", stamp, got)
	}
}

// A cluster whose Cilium the platform did not install has no cilium-ca in the
// platform namespace; its Hubble is not ours to touch.
func TestReconcileHubbleTLSLeavesForeignCiliumAlone(t *testing.T) {
	r := hubbleTestReconciler(t)
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: globals.AdharSystemNamespace}}
	if err := r.reconcileHubbleTLS(context.Background(), platform); err != nil {
		t.Fatalf("expected a quiet no-op, got %v", err)
	}
	s := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: hubbleServerCertSecret, Namespace: globals.AdharSystemNamespace}, s); err == nil {
		t.Error("issued Hubble certificates without a platform cilium-ca")
	}
}

func tlsSecret(name string, crt, key, ca []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: globals.AdharSystemNamespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": crt, "tls.key": key, "ca.crt": ca},
	}
}

func shortLivedCert(t *testing.T, ca *x509.Certificate, caKey *rsa.PrivateKey, name string, life time.Duration) ([]byte, []byte) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(life),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// manifestSecret returns the named Secret from a multi-document manifest, or
// nil.
func manifestSecret(t *testing.T, manifest []byte, name string) *corev1.Secret {
	t.Helper()
	for _, doc := range bytes.Split(manifest, []byte("\n---\n")) {
		s := &corev1.Secret{}
		if err := yaml.Unmarshal(doc, s); err != nil {
			continue
		}
		if s.Kind == "Secret" && s.Name == name {
			s.TypeMeta = metav1.TypeMeta{}
			return s
		}
	}
	return nil
}
