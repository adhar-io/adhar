package up

import (
	"os"
	"strings"
	"testing"
)

// The cached edge certificate must be restored BEFORE the self-signed bootstrap
// writes `adhar-cert` (a Secret that exists is never replaced), and saved after
// the verdict; otherwise every fresh cluster orders a new Let's Encrypt
// certificate and the sixth of the week is refused (429 rateLimited).
func TestACloudBringUpReusesTheCachedEdgeCertificate(t *testing.T) {
	b, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	restore := strings.Index(src, "domain.RestoreEdgeCertificate(")
	selfSigned := strings.Index(src, "kind.SetupSelfSignedCertificate(")
	save := strings.Index(src, "domain.SaveEdgeCertificate(")
	verdict := strings.Index(src, "verifyConsoleReady(context.Background(), kubeClient)")
	if restore < 0 || selfSigned < 0 || restore > selfSigned {
		t.Error("bootstrap.go does not restore the cached edge certificate before the self-signed bootstrap Secret is written")
	}
	if save < 0 || save < verdict {
		t.Error("bootstrap.go does not cache the issued edge certificate after the console verdict")
	}
}
