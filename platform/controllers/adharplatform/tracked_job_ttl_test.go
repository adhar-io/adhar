package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A Job that Argo CD tracks must not delete itself.
//
// ttlSecondsAfterFinished hands a finished Job to the TTL controller, which
// deletes it. Argo CD then finds a resource the Application declares missing,
// marks the app OutOfSync, and selfHeal recreates it — so the Job runs again,
// once per TTL, for as long as the platform runs. OnCall's chart-rendered
// migration Job (20 s TTL) ran `manage.py migrate` every five minutes and held
// the app OutOfSync for good (GCP, 2026-10-26). A Job that should run once per
// sync is an Argo CD hook, which is not compared for drift; a Job that must be
// tracked keeps no TTL.
func TestTrackedJobsDoNotExpireThemselves(t *testing.T) {
	// Tracked Jobs that still carry a TTL and therefore re-run once per TTL on a
	// live cluster (2026-10-27). Each wants the same fix in its own package;
	// they are named here so the list can only shrink. Do not add to it.
	known := map[string]string{
		"adhar-ai-bot-provision":     "ai/adhar-ai: rotates the Gitea bot token on every run, every 30 min",
		"posthog-clickhouse-prepare": "application/posthog",
		"airbyte-airbyte-bootloader": "data/airbyte (chart-rendered)",
		"rustfs-buckets":             "data/rustfs",
	}
	walkProductionManifests(t, func(pkg, file string, obj map[string]interface{}) {
		if obj["kind"] != "Job" || dig(obj, "spec", "ttlSecondsAfterFinished") == nil {
			return
		}
		ann, _ := dig(obj, "metadata", "annotations").(map[string]interface{})
		if ann["argocd.argoproj.io/hook"] != nil || ann["helm.sh/hook"] != nil {
			return
		}
		name, _ := dig(obj, "metadata", "name").(string)
		if _, ok := known[name]; ok {
			return
		}
		t.Errorf("%s: Job %s is tracked by Argo CD and carries ttlSecondsAfterFinished; the TTL deletes it, "+
			"selfHeal recreates it and it runs again every TTL. Make it an Argo CD hook or drop the TTL", file, name)
	})
}

// OnCall's migration runs once per sync, as a Sync hook with a stable name, and
// the generator — not a hand edit — is what keeps it that way.
func TestOnCallMigrationRunsOncePerSync(t *testing.T) {
	var job k8sDoc
	for _, d := range docsOf(t, "observability/oncall/manifests/install.yaml") {
		name, _ := sgDig(d, "metadata", "name").(string)
		if d["kind"] == "Job" && strings.HasPrefix(name, "oncall-engine-migrate") {
			if job != nil {
				t.Fatal("more than one OnCall migrate Job")
			}
			job = d
		}
	}
	if job == nil {
		t.Fatal("no OnCall migrate Job in observability/oncall/manifests/install.yaml")
	}
	if name := sgDig(job, "metadata", "name"); name != "oncall-engine-migrate" {
		t.Errorf("migrate Job is named %v; BeforeHookCreation finds the previous run by name, so a "+
			"render-time name leaves every earlier Job behind", name)
	}
	// Sync, never PreSync: PreSync runs before the MariaDB and the Secrets the
	// Job reads exist, so a fresh install would wait on it forever.
	if hook := sgDig(job, "metadata", "annotations", "argocd.argoproj.io/hook"); hook != "Sync" {
		t.Errorf("migrate Job hook = %v, want Sync", hook)
	}
	if p := sgDig(job, "metadata", "annotations", "argocd.argoproj.io/hook-delete-policy"); p != "BeforeHookCreation" {
		t.Errorf("migrate Job hook-delete-policy = %v, want BeforeHookCreation", p)
	}
	if ttl := sgDig(job, "spec", "ttlSecondsAfterFinished"); ttl != nil {
		t.Errorf("migrate Job keeps ttlSecondsAfterFinished=%v", ttl)
	}
	ann, _ := sgDig(job, "metadata", "annotations").(map[string]any)
	for k := range ann {
		if strings.HasPrefix(k, "helm.sh/hook") {
			t.Errorf("migrate Job carries %s; Argo CD reads a Helm pre-install hook as PreSync", k)
		}
	}

	// The generator inputs that produce the above: a re-render must not undo it.
	dir := filepath.Join(stackPackagesDir(t), "observability", "oncall")
	raw, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if v := sgDig(values, "migrate", "annotations", "argocd.argoproj.io/hook"); v != "Sync" {
		t.Errorf("values.yaml migrate.annotations hook = %v, want Sync", v)
	}
	if v := sgDig(values, "migrate", "ttlSecondsAfterFinished"); v != "" {
		t.Errorf("values.yaml migrate.ttlSecondsAfterFinished = %v, want \"\" (the chart's way to render no TTL)", v)
	}
	if v := sgDig(values, "migrate", "useHook"); v == true {
		t.Error("values.yaml sets migrate.useHook, which renders a Helm pre-install hook Argo CD runs as PreSync")
	}
	post, err := os.ReadFile(filepath.Join(dir, "post-render.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MIGRATE_RENDER_TIME_NAME", "check_migrate_job"} {
		if !strings.Contains(string(post), want) {
			t.Errorf("post-render.py lost %s; the next regeneration ships a render-time name or an unchecked Job", want)
		}
	}
}
