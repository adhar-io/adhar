//go:build e2e

package batch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"adhar-io/adhar/tests/e2e"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The package's two suites share one cluster and one Phase-1 application:
// Test_BatchOnKind (ADHAR_E2E_BATCH) and Test_BatchOnVolcano
// (ADHAR_E2E_VOLCANO). `make e2e-batch-volcano` runs both (A2.9: the batch
// suite on Kind with Volcano), so the platform is brought up once, with the
// staged stack when the canary is on, and torn down once, after both.

type env struct {
	cfg Config
	vc  VolcanoConfig
	c   *Cluster
	// staged is the stack copy `adhar up` seeded, when the harness installed
	// Volcano itself; "" on a platform it did not bring up.
	staged string
	up     bool
}

var shared struct {
	once     sync.Once
	env      *env
	err      error
	cleanups []func()
	mu       sync.Mutex
	apps     map[string]*scaffolded
}

type scaffolded struct {
	once sync.Once
	tpl  *RunTemplate
	err  error
}

// atExit runs after every test of the package, last registered first.
func atExit(f func()) {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	shared.cleanups = append(shared.cleanups, f)
}

func TestMain(m *testing.M) {
	code := m.Run()
	shared.mu.Lock()
	cleanups := shared.cleanups
	shared.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
	os.Exit(code)
}

// loadEnv reads both suites' knobs; a knob that cannot be read fails both.
func loadEnv() (Config, VolcanoConfig, error) {
	cfg, err := LoadConfig(os.Getenv, time.Now())
	if err != nil {
		return cfg, VolcanoConfig{}, err
	}
	vc, err := LoadVolcanoConfig(os.Getenv, cfg)
	return cfg, vc, err
}

var knobs struct {
	once sync.Once
	cfg  Config
	vc   VolcanoConfig
	err  error
}

// settings are the knobs, read once: both suites name things after one tag.
func settings(t *testing.T) (Config, VolcanoConfig) {
	t.Helper()
	knobs.once.Do(func() { knobs.cfg, knobs.vc, knobs.err = loadEnv() })
	if knobs.err != nil {
		t.Fatal(knobs.err)
	}
	return knobs.cfg, knobs.vc
}

// platform brings the platform up once — with Volcano when the canary is on
// — and connects to it. A failed bring-up fails every suite that asks.
func platform(t *testing.T) *env {
	t.Helper()
	cfg, vc := settings(t)
	shared.once.Do(func() { shared.env, shared.err = bringUp(t, cfg, vc) })
	if shared.err != nil {
		t.Fatal(shared.err)
	}
	return shared.env
}

func bringUp(t *testing.T, cfg Config, vc VolcanoConfig) (*env, error) {
	e := &env{cfg: cfg, vc: vc}
	if !cfg.SkipUp {
		if vc.Enabled {
			dir := vc.Stack
			if dir == "" {
				tmp, err := os.MkdirTemp("", "adhar-volcano-stack-")
				if err != nil {
					return nil, err
				}
				dir = tmp
				atExit(func() { _ = os.RemoveAll(tmp) })
			}
			src := e2e.RepoRoot + "/platform/stack"
			names, err := VolcanoStackElements(src)
			if err != nil {
				return nil, err
			}
			changed, err := StageVolcanoStack(src, dir, names)
			if err != nil {
				return nil, err
			}
			staged, err := os.ReadFile(dir + "/" + localProfileFiles[0])
			if err != nil {
				return nil, err
			}
			if p, err := VolcanoProfileProblems(staged, names); err != nil || len(p) > 0 {
				return nil, fmt.Errorf("the staged profile is not the canary's: %v %v", p, err)
			}
			t.Logf("staged %s with %v switched on (the shipped profile is untouched)", dir, changed)
			e.staged = dir
		}
		args := vc.UpArgs(cfg.Apps, e.staged)
		t.Logf("adhar %s (budget %s)", strings.Join(args, " "), cfg.Up)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Up+time.Minute)
		out, err := e2e.RunAdhar(ctx, cfg.Up, args...)
		cancel()
		if err != nil {
			diag, dc := context.WithTimeout(context.Background(), 5*time.Minute)
			t.Logf("cluster state when adhar up gave up:%s", e2e.DumpPlatformState(diag))
			dc()
			return nil, fmt.Errorf("adhar up: %w\n%s", err, out)
		}
		e.up = true
		if !cfg.Keep {
			atExit(func() {
				if out, err := e2e.RunAdhar(context.Background(), 5*time.Minute, "down"); err != nil {
					fmt.Fprintf(os.Stderr, "adhar down failed (cluster needs manual cleanup): %v\n%s\n", err, out)
				}
			})
		}
	}
	c, err := Connect()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if err := CheckCLITarget(c.REST.Host, os.Getenv("KUBECONFIG"), home); err != nil {
		return nil, err
	}
	e.c = c
	return e, nil
}

// appSpec is one scaffolded application.
type appSpec struct {
	Name      string
	Templates []string
	Params    []string
	// Kinds are the run-template kinds accepted (Phase 1: Job only).
	Phase2 bool
	Reuse  bool
}

// ensureApp scaffolds an application once per process (both suites use the
// Phase-1 one) and returns dev's run template. On a platform the suite did not
// bring up, the app is deleted at exit unless it was reused or kept.
func ensureApp(t *testing.T, e *env, spec appSpec) (*RunTemplate, error) {
	t.Helper()
	shared.mu.Lock()
	if shared.apps == nil {
		shared.apps = map[string]*scaffolded{}
	}
	s, ok := shared.apps[spec.Name]
	if !ok {
		s = &scaffolded{}
		shared.apps[spec.Name] = s
	}
	shared.mu.Unlock()
	s.once.Do(func() { s.tpl, s.err = scaffoldApp(t, e, spec) })
	return s.tpl, s.err
}

// errNoTemplate is a scaffold whose template the repository does not offer.
type errNoTemplate struct{ ids []string }

func (e errNoTemplate) Error() string {
	return fmt.Sprintf("none of the templates %v is in the platform's template repository", e.ids)
}

func scaffoldApp(t *testing.T, e *env, spec appSpec) (*RunTemplate, error) {
	ctx := context.Background()
	app := spec.Name
	if spec.Reuse {
		t.Logf("reusing %s", app)
	} else {
		template := ""
		for _, id := range spec.Templates {
			if r, err := Adhar(ctx, 2*time.Minute, "application", "templates", id); err == nil && r.ExitCode == 0 {
				template = id
				break
			}
		}
		if template == "" {
			return nil, errNoTemplate{spec.Templates}
		}
		args := append([]string{"application", "deploy", app, "--template", template}, spec.Params...)
		r, err := Adhar(ctx, 10*time.Minute, args...)
		if err != nil {
			return nil, err
		}
		if r.ExitCode != 0 {
			return nil, fmt.Errorf("adhar %s: %s", strings.Join(args, " "), r)
		}
		if e.cfg.SkipUp && !e.cfg.Keep {
			atExit(func() {
				if r, err := Adhar(context.Background(), 5*time.Minute, "application", "delete", app, "--force"); err != nil || r.ExitCode != 0 {
					fmt.Fprintf(os.Stderr, "cleanup: adhar application delete %s: %v %s\n", app, err, r)
				}
			})
		}
	}

	// CLI-1.2: the scaffold registers a BATCH application.
	appXR, err := e.c.DC.Resource(applicationGVR).Namespace(e2e.PlatformNamespace).Get(ctx, app, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("the Application XR the scaffold registers: %w", err)
	}
	if typ, _, _ := unstructured.NestedString(appXR.Object, "spec", "parameters", "type"); typ != "batch" || appXR.GetLabels()["adhar.io/workload"] != "batch" {
		return nil, fmt.Errorf("Application %s is type %q, workload %q: not a batch application", app, typ, appXR.GetLabels()["adhar.io/workload"])
	}

	bctx, cancel := context.WithTimeout(ctx, e.cfg.Build)
	defer cancel()
	if err := Eventually(bctx, 15*time.Second, "app-ci to build "+app, func(ctx context.Context) (bool, string, error) {
		list, err := e.c.DC.Resource(pipelineRunGVR).List(ctx, metav1.ListOptions{LabelSelector: "adhar.io/app=" + app})
		if err != nil {
			return false, err.Error(), nil
		}
		var seen []string
		for i := range list.Items {
			if ok, why := ConditionTrue(&list.Items[i], "Succeeded"); ok {
				return true, "", nil
			} else {
				seen = append(seen, list.Items[i].GetName()+" "+why)
			}
		}
		return false, fmt.Sprintf("%d PipelineRuns: %s", len(seen), strings.Join(seen, "; ")), nil
	}); err != nil {
		return nil, err
	}
	for _, ns := range []string{devNS, testNS} {
		if err := Eventually(bctx, 10*time.Second, "BatchJob "+ns+"/"+app+" Ready", func(ctx context.Context) (bool, string, error) {
			bj, err := e.c.DC.Resource(batchJobGVR).Namespace(ns).Get(ctx, app, metav1.GetOptions{})
			if err != nil {
				return false, err.Error(), nil
			}
			ok, why := ConditionTrue(bj, "Ready")
			return ok, why, nil
		}); err != nil {
			return nil, err
		}
	}
	cm, err := e.c.CS.CoreV1().ConfigMaps(devNS).Get(ctx, app+"-run", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("ConfigMap %s/%s-run: %w", devNS, app, err)
	}
	var tpl *RunTemplate
	if spec.Phase2 {
		tpl, err = ParseRunTemplateKinds(cm.Data)
	} else {
		tpl, err = ParseRunTemplate(cm.Data)
	}
	if err != nil {
		return nil, fmt.Errorf("ConfigMap %s/%s-run: %w", devNS, app, err)
	}
	if tpl.Manifest.GetLabels()["adhar.io/workload"] != "batch" {
		return nil, fmt.Errorf("the run template's workload label is %q", tpl.Manifest.GetLabels()["adhar.io/workload"])
	}
	return tpl, nil
}

// phaseOneApp is the Phase-1 suite's application, as both suites scaffold it.
func phaseOneApp(cfg Config) appSpec {
	return appSpec{
		Name: cfg.App, Templates: cfg.Templates, Reuse: cfg.Reuse,
		Params: []string{"--param", "batchMode=cron", "--param", "schedule=" + cfg.Schedule, "--param", "timeZone=UTC",
			"--param", "description=adhar batch e2e"},
	}
}
