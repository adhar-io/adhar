package adharplatform

import (
	"strings"
	"testing"
)

// Plane's uploads go from the browser straight to the object store on a URL
// the API presigns. Signed against the in-cluster RustFS Service (USE_MINIO=0)
// that URL names a host no browser can resolve, and every cover image and
// attachment failed. Three things must agree: Plane signs for the browser's
// host over https, and the route sends the bucket path to RustFS — directly,
// not through the SSO proxy, which would hand the S3 request to Plane's web app.
func TestPlaneSignsUploadsForTheBrowser(t *testing.T) {
	install := readStackFile(t, "application/plane/manifests/install.yaml")
	for _, want := range []string{`USE_MINIO: "1"`, `MINIO_ENDPOINT_SSL: "1"`, `AWS_S3_BUCKET_NAME: "plane"`} {
		if !strings.Contains(install, want) {
			t.Errorf("plane install.yaml lacks %s", want)
		}
	}
	gen := readStackFile(t, "application/plane/generate-manifests.sh")
	if !strings.Contains(gen, `for key in ("USE_MINIO", "MINIO_ENDPOINT_SSL")`) {
		t.Error("generate-manifests.sh no longer sets USE_MINIO/MINIO_ENDPOINT_SSL; regenerating would break uploads again")
	}
	route := readStackFile(t, "application/plane/manifests/httproute.yaml.tmpl")
	want := "            value: /plane\n      backendRefs:\n        - name: rustfs\n          port: 9000\n"
	if !strings.Contains(route, want) {
		t.Error("plane's HTTPRoute must send /plane (the bucket) straight to rustfs:9000")
	}
}
