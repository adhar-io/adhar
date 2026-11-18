package events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	platformevents "adhar-io/adhar/platform/events"

	"github.com/spf13/cobra"
)

// apiServerWithBridge is an API server whose service proxy leads to a fake
// bridge; every request it serves is counted, so a test can say which cluster
// a command actually asked.
func apiServerWithBridge(t *testing.T, f *fakeBridge) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	asked := 0
	bridge := http.StripPrefix(bridgeProxy, f.handler(t))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked++
		mu.Unlock()
		switch {
		case !strings.HasPrefix(r.URL.Path, bridgeProxy+"/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == bridgeProxy+"/topics":
			_, _ = w.Write([]byte(`["adhar.batch"]`))
		default:
			bridge.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return asked }
}

// `adhar events` reads the cluster the operator pointed it at, by the same
// rules as `adhar batch history`: --kubeconfig when given, else $KUBECONFIG.
// It used to read helpers.KubeConfigPath, which nothing assigns, so it always
// read ~/.kube/config — `adhar batch history --kubeconfig prod` and
// `adhar events list --kubeconfig prod` then showed two different clusters'
// buses side by side.
func TestTheBridgeIsReachedThroughTheKubeconfigTheOperatorNamed(t *testing.T) {
	prevTransport := sharedTransport
	t.Cleanup(func() { sharedTransport = prevTransport })

	env, envAsked := apiServerWithBridge(t, &fakeBridge{})
	flag, flagAsked := apiServerWithBridge(t, &fakeBridge{})
	t.Setenv("KUBECONFIG", kubeconfigFor(t, env.URL))

	base, _, err := portForwardBridge(context.Background(), "")
	if err != nil || !strings.HasPrefix(base, env.URL+bridgeProxy) || envAsked() == 0 {
		t.Fatalf("with no --kubeconfig the bridge must be $KUBECONFIG's: base %q err %v (asked %d)", base, err, envAsked())
	}
	before := envAsked()
	base, _, err = portForwardBridge(context.Background(), kubeconfigFor(t, flag.URL))
	if err != nil || !strings.HasPrefix(base, flag.URL+bridgeProxy) || flagAsked() == 0 || envAsked() != before {
		t.Fatalf("--kubeconfig must win over $KUBECONFIG: base %q err %v (flag asked %d, env asked %d more)", base, err, flagAsked(), envAsked()-before)
	}
}

// The command itself hands --kubeconfig, the root command's persistent flag,
// to the bridge: `adhar events list --kubeconfig <file>` reads that file's
// cluster and never $KUBECONFIG's.
func TestEventsListFollowsTheKubeconfigFlag(t *testing.T) {
	prevTransport, prevOutput, prevFollow := sharedTransport, flagOutput, flagFollow
	reset()
	t.Cleanup(func() { sharedTransport, flagOutput, flagFollow = prevTransport, prevOutput, prevFollow; reset() })

	env, envAsked := apiServerWithBridge(t, &fakeBridge{})
	f := &fakeBridge{records: [][]record{{batchRecord(t, 0, platformevents.TypeBatchRunFailed, "prod/importer", "2026-11-02T01:10:00Z")}}}
	flag, flagAsked := apiServerWithBridge(t, f)
	t.Setenv("KUBECONFIG", kubeconfigFor(t, env.URL))

	root := &cobra.Command{Use: "adhar", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("kubeconfig", "", "")
	root.AddCommand(EventsCmd)
	t.Cleanup(func() { root.RemoveCommand(EventsCmd) })
	root.SetArgs([]string{"events", "list", "--domain", "batch", "-o", "json", "--kubeconfig", kubeconfigFor(t, flag.URL)})
	if err := root.Execute(); err != nil {
		t.Fatalf("events list: %v", err)
	}
	if flagAsked() == 0 || !f.deleted || strings.Join(f.subscribed, ",") != platformevents.TopicBatch {
		t.Errorf("the --kubeconfig cluster's bridge was not read: asked %d, subscribed %v, consumer deleted %v", flagAsked(), f.subscribed, f.deleted)
	}
	if envAsked() != 0 {
		t.Errorf("$KUBECONFIG's cluster was asked %d times although --kubeconfig named another", envAsked())
	}
}
