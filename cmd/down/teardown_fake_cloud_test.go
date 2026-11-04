package down

// The teardown loop driven end to end against a fake cloud.
//
// teardownFromConfig is the code that ends in DeleteCluster, and until now the
// only guards on it read its source text (provided_cluster_test.go,
// shared_cluster_test.go), because running it needs a configured cloud. With
// the lookup behind the findCluster seam the real loop runs here — the
// provided gate, the AKS refusal, the purge refusal, the identity written down
// before the delete, the volume jobs — and then the real volume step on its
// output. Nothing in it can reach a network: the provider is a struct.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"
	ptypes "adhar-io/adhar/platform/types"
)

// fakeCloudProvider is a cloud that can delete a cluster and nothing else. Any
// other Provider method panics (the embedded interface is nil): the teardown
// must not need one.
type fakeCloudProvider struct {
	pfactory.Provider
	*fakeCloud
	region     string
	deletedIDs []string
	deleteErr  error
	// atDelete is what this machine had written down for the cluster at the
	// moment DeleteCluster was called.
	atDelete []pfactory.ClusterInstall
}

func (f *fakeCloudProvider) DeleteCluster(_ context.Context, id string) error {
	f.atDelete, _ = pfactory.LoadClusterInstalls("digitalocean", f.region, "adhar")
	f.deletedIDs = append(f.deletedIDs, id)
	return f.deleteErr
}

// teardownRun is one `adhar down -f <file>` against the fake cloud.
type teardownRun struct {
	file        string
	env         string
	keep, purge bool
	// clusters are what findCluster finds, by name; anything else is absent.
	clusters map[string]*helpers.ResolvedCluster
	// lookupFails are the names whose lookup cannot be completed (the provider
	// could not be queried), so "not found" for them proves nothing.
	lookupFails map[string]bool
	// installID is what the cluster's adhar-cluster-spec says (about this very
	// cluster); spec, when set, replaces the whole answer; readErr fails the
	// read instead.
	installID string
	spec      *clusterSpecIdentity
	readErr   error
}

type teardownResult struct {
	outcome  teardownOutcome
	err      error
	details  []string
	searched []string
}

func (r teardownResult) said(s string) bool {
	return strings.Contains(strings.Join(r.details, "\n"), s)
}

func (tr teardownRun) run(t *testing.T) teardownResult {
	t.Helper()
	prev := struct {
		file, env, name string
		keep, purge     bool
		find            func(context.Context, *config.Config, string, string, map[string]interface{}, func(string)) (*helpers.ResolvedCluster, error)
		read            func(context.Context, *helpers.ResolvedCluster, string) (clusterSpecIdentity, error)
	}{downConfigFile, downEnv, downClusterName, keepVolumes, purgeVolumes, findCluster, readClusterSpec}
	t.Cleanup(func() {
		downConfigFile, downEnv, downClusterName, keepVolumes, purgeVolumes = prev.file, prev.env, prev.name, prev.keep, prev.purge
		findCluster, readClusterSpec = prev.find, prev.read
	})
	downConfigFile, downEnv, downClusterName, keepVolumes, purgeVolumes = tr.file, tr.env, "", tr.keep, tr.purge

	var res teardownResult
	findCluster = func(_ context.Context, _ *config.Config, providerName, name string, _ map[string]interface{}, _ func(string)) (*helpers.ResolvedCluster, error) {
		res.searched = append(res.searched, providerName+"/"+name)
		if tr.lookupFails[name] {
			return nil, &helpers.NotFoundError{Name: name, Failures: []helpers.LookupFailure{{Provider: providerName, Reason: "503 Service Unavailable"}}}
		}
		if c, ok := tr.clusters[name]; ok && c.ProviderName == providerName {
			return c, nil
		}
		return nil, &helpers.NotFoundError{Name: name, Searched: []string{providerName}}
	}
	readClusterSpec = func(_ context.Context, found *helpers.ResolvedCluster, name string) (clusterSpecIdentity, error) {
		spec := tr.spec
		if spec == nil {
			spec = &clusterSpecIdentity{InstallID: tr.installID, Provider: found.ProviderName, Region: "blr1", ClusterName: name}
		}
		return *spec, tr.readErr
	}

	sub := make(chan tea.Msg, 1024)
	teardownFromConfig(func(m tea.Msg) { sub <- m }, func(f string, a ...interface{}) {
		res.details = append(res.details, fmt.Sprintf(f, a...))
	})
	close(sub)
	for m := range sub {
		switch m := m.(type) {
		case teardownOutcome:
			res.outcome = m
		case logger.ErrorMsg:
			res.err = m.Err
		}
	}
	return res
}

// writeConfig writes a config whose `digitalocean` provider builds clusters
// in blr1 in the given mode, plus — when providedEnv is set — a second
// environment, `edge`, on a PROVIDED cluster of the same cloud: its
// clusterConfig says clusterMode: provided, which is exactly how `adhar up`
// (ProvisionEnvironment) decides to adopt rather than build.
func writeConfig(t *testing.T, mode string, providedEnv bool) string {
	t.Helper()
	body := `globalSettings:
  adharContext: adhar-mgmt
  defaultHost: platform.example.com
  defaultHttpPort: 80
  defaultHttpsPort: 443
  email: admin@example.com
providers:
  digitalocean:
    type: digitalocean
    region: blr1
    primary: true
    useEnvironment: true
    clusterMode: ` + mode + `
environmentTemplates:
  base:
    clusterConfig:
      - key: autoScale
        value: "false"
    coreServices:
      cilium:
        chart:
          repoURL: https://helm.cilium.io/
          name: cilium
          version: 1.15.7
environments:
  dev:
    isolation: cluster
    type: non-production
    provider: digitalocean
    template: base
    clusterConfig:
      - key: nodeCount
        value: "2"
`
	if providedEnv {
		body += `  edge:
    isolation: cluster
    type: non-production
    provider: digitalocean
    template: base
    clusterConfig:
      - key: clusterMode
        value: provided
`
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func foundCluster(p *fakeCloudProvider, id string) map[string]*helpers.ResolvedCluster {
	return map[string]*helpers.ResolvedCluster{"adhar": {
		Cluster:      &ptypes.Cluster{ID: id, Name: "adhar", Status: ptypes.ClusterStatusRunning},
		Provider:     p,
		ProviderName: "digitalocean",
	}}
}

// volumeStepFor runs the real volume step on a teardown's jobs, answering yes.
func volumeStepFor(cloud *fakeCloud, jobs []volumeJob) (bool, string) {
	step, out := testStep(cloud, stepOpts{yes: true})
	step.managerFor = func(j volumeJob) (pfactory.ClusterVolumeManager, error) {
		if m, ok := j.Provider.(pfactory.ClusterVolumeManager); ok {
			return m, nil
		}
		return cloud, nil
	}
	ok := step.run(jobs)
	return ok, out.String()
}

// A cluster created on ANOTHER machine: this one never recorded its install
// id. The teardown must learn the id from the cluster itself and write it down
// BEFORE deleting the cluster — then delete exactly the volumes carrying it.
func TestTeardownLearnsTheInstallIDFromTheClusterBeforeDeletingIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const clusterID = "jr56way8z4rwjt16"
	cloud := newFakeCloud()
	cloud.add("vol-1", clusterID, false)
	cloud.add("vol-2", clusterID, false)
	cloud.add("vol-other-install", "zzzzzzzzzzzzzzzz", false)
	p := &fakeCloudProvider{fakeCloud: cloud, region: "blr1"}

	r := teardownRun{file: writeConfig(t, "compute", false), env: "dev", clusters: foundCluster(p, "adhar-cluster-adhar"), installID: clusterID}.run(t)
	if r.err != nil {
		t.Fatalf("teardown failed: %v\n%s", r.err, strings.Join(r.details, "\n"))
	}
	if strings.Join(p.deletedIDs, ",") != "adhar-cluster-adhar" {
		t.Fatalf("DeleteCluster calls = %v", p.deletedIDs)
	}
	if len(p.atDelete) != 1 || p.atDelete[0].ID != clusterID || !p.atDelete[0].Active() {
		t.Fatalf("at DeleteCluster this machine had recorded %+v; the cluster's own install id must be written down first", p.atDelete)
	}
	if len(r.outcome.VolumeJobs) != 1 || !r.outcome.VolumeJobs[0].ClusterDeleted {
		t.Fatalf("volume jobs = %+v, want the deleted cluster's", r.outcome.VolumeJobs)
	}

	ok, out := volumeStepFor(cloud, r.outcome.VolumeJobs)
	if !ok {
		t.Fatalf("volume step failed:\n%s", out)
	}
	if got := strings.Join(sortedCopy(cloud.deleted), ","); got != "vol-1,vol-2" {
		t.Fatalf("deleted %s; only the volumes carrying the cluster's own id may go", got)
	}
	if recs, _ := pfactory.LoadClusterInstalls("digitalocean", "blr1", "adhar"); len(recs) != 0 {
		t.Errorf("the record outlived its last volume: %+v", recs)
	}
}

// An id the cluster reports that this machine cannot write down stops THAT
// cluster's deletion: once the cluster is gone, nothing else knows the id.
func TestAnInstallIDThatCannotBeRecordedStopsTheDeletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := pfactory.ClusterInstallPath("digitalocean", "blr1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "blr1"}
	r := teardownRun{file: writeConfig(t, "compute", false), env: "dev", clusters: foundCluster(p, "adhar-cluster-adhar"), installID: "jr56way8z4rwjt16"}.run(t)
	if len(p.deletedIDs) != 0 {
		t.Fatal("the cluster was deleted although the install id its volumes carry could not be recorded")
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "was NOT deleted") {
		t.Fatalf("teardown error = %v; want the refusal reported", r.err)
	}
	if len(r.outcome.Deleted) != 0 || len(r.outcome.VolumeJobs) != 0 {
		t.Fatalf("outcome = %+v; nothing was deleted and nothing may be queued", r.outcome)
	}
}

// A cluster that cannot be asked is still torn down: its volumes fall back to
// this machine's record.
func TestAnUnreachableClusterIsStillTornDown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst, err := pfactory.RecordClusterInstall("digitalocean", "blr1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud()
	cloud.add("vol-1", inst.ID, false)
	p := &fakeCloudProvider{fakeCloud: cloud, region: "blr1"}
	r := teardownRun{file: writeConfig(t, "compute", false), env: "dev", clusters: foundCluster(p, "adhar-cluster-adhar"),
		readErr: errors.New("dial tcp: i/o timeout")}.run(t)
	if r.err != nil || len(p.deletedIDs) != 1 {
		t.Fatalf("err=%v deletes=%v", r.err, p.deletedIDs)
	}
	if !r.said("could not ask the cluster for its install id") {
		t.Errorf("the unreadable cluster was not reported:\n%s", strings.Join(r.details, "\n"))
	}
	if ok, out := volumeStepFor(cloud, r.outcome.VolumeJobs); !ok || strings.Join(cloud.deleted, ",") != "vol-1" {
		t.Fatalf("deleted %v (ok=%v):\n%s", cloud.deleted, ok, out)
	}
}

// A managed cluster's cloud UUID is written down before the delete too.
func TestAManagedClustersCloudIDIsRecordedBeforeTheDelete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const uuid = "0b7a3c3e-1f0e-4b8e-9a6f-2a1d3c4b5e6f"
	p := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "blr1"}
	r := teardownRun{file: writeConfig(t, "managed", false), env: "dev", clusters: foundCluster(p, uuid)}.run(t)
	if r.err != nil {
		t.Fatal(r.err)
	}
	found := false
	for _, rec := range p.atDelete {
		found = found || rec.ProviderClusterID == uuid
	}
	if !found {
		t.Fatalf("at DeleteCluster the record was %+v; the cluster UUID its volumes are stamped with was not written down", p.atDelete)
	}
}

// A provided cluster is never looked up, never deleted and queues no volume
// job — driven through the real loop, not read from its source.
func TestTheTeardownLoopNeverTouchesAProvidedCluster(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "blr1"}
	clusters := foundCluster(p, "adhar-cluster-adhar")
	r := teardownRun{file: writeConfig(t, "provided", false), env: "dev", clusters: clusters, installID: "jr56way8z4rwjt16"}.run(t)
	if len(r.searched) != 0 || len(p.deletedIDs) != 0 || len(r.outcome.VolumeJobs) != 0 {
		t.Fatalf("a provided cluster was touched: searched=%v deleted=%v jobs=%+v", r.searched, p.deletedIDs, r.outcome.VolumeJobs)
	}
	if strings.Join(r.outcome.Preserved, ",") != "dev" {
		t.Fatalf("preserved = %v", r.outcome.Preserved)
	}
}

// A provided cluster in the same file may run where this one did, and its
// volumes carry no id: no evidence-based deletion may happen there. The purge
// is refused before anything is deleted; the legacy offer is never made; the
// cluster's OWN id-tagged volumes still go.
func TestAProvidedNeighbourStopsEveryEvidenceBasedDeletion(t *testing.T) {
	t.Run("--purge-orphaned-volumes is refused before anything is deleted", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		p := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "blr1"}
		r := teardownRun{file: writeConfig(t, "compute", true), env: "dev", purge: true, clusters: foundCluster(p, "adhar-cluster-adhar")}.run(t)
		if r.err == nil || !strings.Contains(r.err.Error(), "refusing --purge-orphaned-volumes") || !strings.Contains(r.err.Error(), "edge") {
			t.Fatalf("err = %v; want the purge refused, naming the provided environment", r.err)
		}
		if len(r.searched) != 0 || len(p.deletedIDs) != 0 {
			t.Fatalf("something was looked up or deleted before the refusal: %v %v", r.searched, p.deletedIDs)
		}
	})

	t.Run("the legacy offer is not made; the cluster's own volumes still go", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		inst, err := pfactory.RecordClusterInstall("digitalocean", "blr1", "adhar")
		if err != nil {
			t.Fatal(err)
		}
		cloud := newFakeCloud()
		cloud.add("vol-mine", inst.ID, false)
		cloud.add("vol-maybe-the-provided-clusters", "", true)
		p := &fakeCloudProvider{fakeCloud: cloud, region: "blr1"}
		r := teardownRun{file: writeConfig(t, "compute", true), env: "dev", clusters: foundCluster(p, "adhar-cluster-adhar")}.run(t)
		if r.err != nil {
			t.Fatal(r.err)
		}
		if len(r.outcome.VolumeJobs) != 1 || strings.Join(r.outcome.VolumeJobs[0].ProvidedNeighbours, ",") != "edge" {
			t.Fatalf("jobs = %+v; the provided neighbour must travel with the job", r.outcome.VolumeJobs)
		}
		// Typed yes at a terminal: the strongest consent there is.
		step, out := testStep(cloud, stepOpts{interactive: true, input: "yes\nyes\n"})
		step.managerFor = func(j volumeJob) (pfactory.ClusterVolumeManager, error) {
			return j.Provider.(pfactory.ClusterVolumeManager), nil
		}
		step.run(r.outcome.VolumeJobs)
		if got := strings.Join(cloud.deleted, ","); got != "vol-mine" {
			t.Fatalf("deleted %q; only the id-tagged volume may go while a provided cluster may own the rest", got)
		}
		if !strings.Contains(out.String(), "edge uses a provided cluster") {
			t.Errorf("the reason nothing else was offered is not said:\n%s", out)
		}
	})
}

// AKS deletes its node resource group — every disk in it — with the cluster,
// so --keep-volumes cannot be honoured by deleting the cluster: refused, and
// nothing is deleted.
func TestKeepVolumesIsRefusedWhereTheVolumesGoWithTheCluster(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw := strings.NewReplacer("type: digitalocean", "type: azure", "  digitalocean:", "  azure:", "provider: digitalocean", "provider: azure").
		Replace(mustRead(t, writeConfig(t, "managed", false)))
	path := filepath.Join(t.TempDir(), "aks.yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "blr1"}
	clusters := map[string]*helpers.ResolvedCluster{"adhar": {Cluster: &ptypes.Cluster{ID: "adhar"}, Provider: p, ProviderName: "azure"}}
	r := teardownRun{file: path, env: "dev", keep: true, clusters: clusters}.run(t)
	if len(p.deletedIDs) != 0 || len(r.searched) != 0 {
		t.Fatalf("an AKS cluster was looked up or deleted under --keep-volumes: %v %v", r.searched, p.deletedIDs)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "--keep-volumes cannot be honoured on AKS") {
		t.Fatalf("err = %v", r.err)
	}

	// Without --keep-volumes it is deleted, and the confirmation said what happens.
	r = teardownRun{file: path, env: "dev", clusters: clusters}.run(t)
	if len(p.deletedIDs) != 1 {
		t.Fatalf("without --keep-volumes the AKS cluster was not deleted: %v %v", r.err, p.deletedIDs)
	}
	plan := buildTeardownPlan(path, "dev", "")
	if out := RenderTeardownConfirmation(plan); !strings.Contains(out, "Deleted WITH the cluster") {
		t.Errorf("the confirmation still promises a list of AKS volumes:\n%s", out)
	}
}

// A cluster shared by environments whose deletion FAILED is not reported as
// removed for the second environment.
func TestASharedClusterThatWasNotDeletedIsNotReportedRemoved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw := strings.Replace(mustRead(t, writeConfig(t, "compute", false)), "    isolation: cluster\n", "    isolation: namespace\n", 1) +
		"  test:\n    isolation: namespace\n    type: non-production\n    provider: digitalocean\n    template: base\n" +
		"    clusterConfig:\n      - key: nodeCount\n        value: \"2\"\n"
	path := filepath.Join(t.TempDir(), "shared.yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "blr1", deleteErr: errors.New("403 account suspended")}
	r := teardownRun{file: path, clusters: foundCluster(p, "adhar-cluster-adhar")}.run(t)
	if len(r.outcome.Deleted) != 0 {
		t.Fatalf("Deleted = %v; a cluster whose deletion failed was reported removed for an environment sharing it", r.outcome.Deleted)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "which was not deleted") {
		t.Fatalf("err = %v", r.err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// When the environment's cloud cluster is already gone, a cluster of the same
// name in ANOTHER provider (the developer's local Kind cluster) is not it: it is
// left alone, and the gone cluster's volumes are still dealt with.
func TestAClusterOfTheSameNameElsewhereIsNotTheEnvironments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst, err := pfactory.RecordClusterInstall("digitalocean", "blr1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud()
	cloud.add("vol-1", inst.ID, false)
	local := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "local"}
	clusters := map[string]*helpers.ResolvedCluster{"adhar": {Cluster: &ptypes.Cluster{ID: "adhar", Name: "adhar"}, Provider: local, ProviderName: "kind"}}
	r := teardownRun{file: writeConfig(t, "compute", false), env: "dev", clusters: clusters}.run(t)
	if len(local.deletedIDs) != 0 {
		t.Fatal("the local Kind cluster was deleted by a teardown of a DigitalOcean environment")
	}
	if strings.Join(r.searched, ",") != "digitalocean/adhar,digitalocean/dev" {
		t.Fatalf("searched %v; only the environment's provider may be searched", r.searched)
	}
	if len(r.outcome.VolumeJobs) != 1 || r.outcome.VolumeJobs[0].ProviderName != "digitalocean" {
		t.Fatalf("jobs = %+v; the gone cluster's volumes must still be dealt with", r.outcome.VolumeJobs)
	}
	// This machine never saw that cluster deleted — its record still says it
	// is running — so one listing is all the evidence that it is gone: --yes
	// does not delete its volumes, and its record is left as it was.
	if _, out := volumeStepFor(cloud, r.outcome.VolumeJobs); len(cloud.deleted) != 0 || !strings.Contains(out, "confirm at a terminal") {
		t.Fatalf("--yes deleted %v of a cluster nobody saw deleted:\n%s", cloud.deleted, out)
	}
	if a, err := pfactory.ActiveClusterInstall("digitalocean", "blr1", "adhar"); err != nil || a == nil || a.ID != inst.ID {
		t.Fatalf("record = %+v (%v); it must be left exactly as it was", a, err)
	}
	// A person at a terminal can say it is gone.
	step, out := testStep(cloud, stepOpts{interactive: true, input: "yes\n"})
	if !step.run(r.outcome.VolumeJobs) || strings.Join(cloud.deleted, ",") != "vol-1" {
		t.Fatalf("deleted %v:\n%s", cloud.deleted, out)
	}
	if recs, _ := pfactory.LoadClusterInstalls("digitalocean", "blr1", "adhar"); len(recs) != 0 {
		t.Fatalf("the record outlived its last volume: %+v", recs)
	}
}

// `adhar up` ADOPTS an environment whose clusterConfig says clusterMode:
// provided, whatever its provider block says (ProvisionEnvironment parses the
// block AND the clusterConfig). The teardown used to read the block alone, see
// compute, and go on to locate and delete the cluster the platform never made.
func TestAProvidedModeSetInAnEnvironmentsClusterConfigIsHonoured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw := strings.Replace(mustRead(t, writeConfig(t, "compute", false)),
		"      - key: nodeCount\n        value: \"2\"\n",
		"      - key: clusterMode\n        value: provided\n", 1)
	path := filepath.Join(t.TempDir(), "adopted.yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "blr1"}
	r := teardownRun{file: path, env: "dev", clusters: foundCluster(p, "adhar-cluster-adhar")}.run(t)
	if len(r.searched) != 0 || len(p.deletedIDs) != 0 || len(r.outcome.VolumeJobs) != 0 {
		t.Fatalf("an adopted (provided) cluster was touched: searched=%v deleted=%v jobs=%+v", r.searched, p.deletedIDs, r.outcome.VolumeJobs)
	}
	if strings.Join(r.outcome.Preserved, ",") != "dev" {
		t.Fatalf("preserved = %v; want dev reported as left running", r.outcome.Preserved)
	}
}

// ~/.adhar/clusters/<name>/kubeconfig is keyed by name alone, so it may reach a
// DIFFERENT cluster of the same name — another cloud, another region. That
// cluster's install id must not be adopted for this one.
func TestAnInstallIDFromAnotherClusterOfTheSameNameIsIgnored(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := &fakeCloudProvider{fakeCloud: newFakeCloud(), region: "blr1"}
	for name, spec := range map[string]clusterSpecIdentity{
		"another region": {InstallID: "zzzzzzzzzzzzzzzz", Provider: "digitalocean", Region: "nyc1", ClusterName: "adhar"},
		"another cloud":  {InstallID: "zzzzzzzzzzzzzzzz", Provider: "aws", Region: "blr1", ClusterName: "adhar"},
		"another name":   {InstallID: "zzzzzzzzzzzzzzzz", Provider: "digitalocean", Region: "blr1", ClusterName: "other"},
	} {
		spec := spec
		r := teardownRun{file: writeConfig(t, "compute", false), env: "dev", clusters: foundCluster(p, "adhar-cluster-adhar"), spec: &spec}.run(t)
		if r.err != nil {
			t.Fatalf("%s: %v", name, r.err)
		}
		for _, rec := range p.atDelete {
			if rec.ID == "zzzzzzzzzzzzzzzz" {
				t.Fatalf("%s: the install id of a different cluster was adopted: %+v", name, p.atDelete)
			}
		}
		if !r.said("not this one; its install id is ignored") {
			t.Errorf("%s: the mismatch was not reported:\n%s", name, strings.Join(r.details, "\n"))
		}
	}
	// The same spelling of the same cloud is the same cluster.
	if !sameCluster(clusterSpecIdentity{Provider: "do", Region: "BLR1", ClusterName: "adhar"}, "digitalocean", "blr1", "adhar") {
		t.Error("do/digitalocean and BLR1/blr1 were treated as different clusters")
	}
}

// A lookup that could not be completed for ONE of an environment's candidate
// names proves nothing about that name, whatever the lookup of another name
// said. The loop kept only the LAST candidate's error, so when the provider
// failed to answer for "adhar" (a 503, a rate limit, a network blip) and then
// answered for the legacy name "dev", the environment was reported as already
// gone — and the volume step went on to list the volumes carrying the RUNNING
// cluster's install id, delete the detached ones (under --yes, without a
// question) and mark the cluster's record deleted.
func TestAnInconclusiveLookupIsNeverReadAsAGoneCluster(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst, err := pfactory.RecordClusterInstall("digitalocean", "blr1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud()
	cloud.add("vol-attached", inst.ID, false)
	cloud.vols["vol-attached"].AttachedTo = []string{"droplet 1"}
	// Detached, and still the running cluster's: a StatefulSet scaled to zero.
	cloud.add("vol-scaled-to-zero", inst.ID, false)

	r := teardownRun{file: writeConfig(t, "compute", false), env: "dev", lookupFails: map[string]bool{"adhar": true}}.run(t)
	if strings.Join(r.searched, ",") != "digitalocean/adhar,digitalocean/dev" {
		t.Fatalf("searched %v", r.searched)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "could not be consulted") {
		t.Fatalf("err = %v; a lookup that could not be completed must fail the teardown, not read as \"already gone\"", r.err)
	}
	if len(r.outcome.Absent) != 0 || len(r.outcome.VolumeJobs) != 0 {
		t.Fatalf("absent=%v jobs=%+v; nothing may be queued for a cluster nobody could look for", r.outcome.Absent, r.outcome.VolumeJobs)
	}
	if ok, out := volumeStepFor(cloud, r.outcome.VolumeJobs); !ok || len(cloud.deleted) != 0 {
		t.Fatalf("deleted %v (ok=%v) — volumes of a cluster that may still be running:\n%s", cloud.deleted, ok, out)
	}
	if a, err := pfactory.ActiveClusterInstall("digitalocean", "blr1", "adhar"); err != nil || a == nil || a.ID != inst.ID {
		t.Fatalf("the running cluster's record = %+v (%v); it must stay active", a, err)
	}

	// The other order already failed safe, and still does: the legacy name's
	// lookup failing after "adhar" was conclusively absent is a failure too.
	r = teardownRun{file: writeConfig(t, "compute", false), env: "dev", lookupFails: map[string]bool{"dev": true}}.run(t)
	if r.err == nil || len(r.outcome.VolumeJobs) != 0 {
		t.Fatalf("err=%v jobs=%+v", r.err, r.outcome.VolumeJobs)
	}
}
