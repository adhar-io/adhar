package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Platform admins — and the console's Plane key — belong to every Plane project.
//
// A Plane workspace admin is not a member of the projects other people create,
// and project-level reads check ProjectMember, not the workspace role. The
// console works through one API key, minted by plane-instance-setup for a
// workspace admin, so every project created in Plane itself answered it with
// 403: on Azure (2026-10-28) project IBNG, created by a platform admin, had one
// member, and the Define phase read "The console's Plane key can't read this
// project". plane-project-access is the CronJob that closes it; this pins what
// makes it work.
func TestPlaneAdminsAndTheConsoleKeyReachEveryProject(t *testing.T) {
	dir := filepath.Join(stackPackagesDir(t), "application/plane/manifests")
	body, err := os.ReadFile(filepath.Join(dir, "project-access.yaml"))
	if err != nil {
		t.Fatalf("the plane package no longer declares project-access.yaml (%v); projects created in Plane "+
			"stay unreadable to the console's key and to platform admins who did not create them", err)
	}
	var cron map[string]interface{}
	configMaps := map[string]map[string]interface{}{}
	for _, doc := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(body), -1) {
		var obj map[string]interface{}
		if yaml.Unmarshal([]byte(doc), &obj) != nil || obj == nil {
			continue
		}
		name, _ := dig(obj, "metadata", "name").(string)
		switch obj["kind"] {
		case "CronJob":
			cron = obj
		case "ConfigMap":
			data, _ := obj["data"].(map[string]interface{})
			configMaps[name] = data
		}
	}
	if cron == nil {
		t.Fatal("project-access.yaml declares no CronJob; a one-shot hook never sees projects created later")
	}

	// Often enough to keep the console's "within five minutes" true, and never
	// stacking runs: under Forbid, a run with no deadline that hangs skips
	// every later schedule.
	schedule, _ := dig(cron, "spec", "schedule").(string)
	minute := strings.Fields(schedule + " ")[0]
	every := 1
	if strings.HasPrefix(minute, "*/") {
		every, _ = strconv.Atoi(strings.TrimPrefix(minute, "*/"))
	} else if minute != "*" {
		every = 60
	}
	if every < 1 || every > 5 {
		t.Errorf("plane-project-access runs on %q; a project created in Plane must reach the console within five minutes", schedule)
	}
	if dig(cron, "spec", "concurrencyPolicy") != "Forbid" {
		t.Error("plane-project-access must use concurrencyPolicy: Forbid")
	}
	deadline, _ := dig(cron, "spec", "jobTemplate", "spec", "activeDeadlineSeconds").(float64)
	if deadline <= 0 || deadline > float64(every*60) {
		t.Errorf("plane-project-access needs an activeDeadlineSeconds inside its %d-minute interval, got %v", every, deadline)
	}

	// The script is a tracked ConfigMap in the same file, run through Plane's
	// own Django models.
	jobSpec, _ := dig(cron, "spec", "jobTemplate", "spec").(map[string]interface{})
	podSpec, _ := dig(jobSpec, "template", "spec").(map[string]interface{})
	var script, slug, label string
	for _, v := range podSpec["volumes"].([]interface{}) {
		cmName, _ := dig(v.(map[string]interface{}), "configMap", "name").(string)
		for key, value := range configMaps[cmName] {
			if strings.HasSuffix(key, ".py") {
				script, _ = value.(string)
			}
		}
	}
	if script == "" {
		t.Fatal("plane-project-access mounts no script from a ConfigMap declared beside it")
	}
	for _, c := range containersOf(jobSpec) {
		cmd := asYAML(t, c["command"])
		if !strings.Contains(cmd, "manage.py shell") {
			t.Errorf("container %v does not run the script through manage.py shell: %s", c["name"], cmd)
		}
		for _, e := range c["env"].([]interface{}) {
			env := e.(map[string]interface{})
			switch env["name"] {
			case "WORKSPACE_SLUG":
				slug, _ = env["value"].(string)
			case "CONSOLE_KEY_LABEL":
				label, _ = env["value"].(string)
			}
		}
	}

	// It must name the same workspace and key plane-instance-setup creates, or
	// it reconciles a workspace nobody uses for an identity that does not exist.
	setup := readStackFile(t, "application/plane/manifests/instance-setup.yaml")
	if m := regexp.MustCompile(`name: WORKSPACE_SLUG\n\s+value: (\S+)`).FindStringSubmatch(setup); m == nil || m[1] != slug {
		t.Errorf("plane-project-access reconciles workspace %q, plane-instance-setup creates %v", slug, m)
	}
	if m := regexp.MustCompile(`label = "([^"]+)"`).FindStringSubmatch(setup); m == nil || m[1] != label {
		t.Errorf("plane-project-access looks for the console key labelled %q, plane-instance-setup mints %v", label, m)
	}

	for _, want := range []struct{ needle, why string }{
		{"wm.role == ADMIN", "every workspace admin (Keycloak platform-admin) must be a target"},
		{"key.user_id", "the console key's user must be a target"},
		{"ADMIN, MEMBER = 20, 15", "Plane's admin role is 20; it refuses a workspace admin a lower project role"},
		// Plane's own add-member path. ProjectMember.save() inserts a second
		// ProjectUserProperty and violates its unique constraint for anyone
		// whose earlier membership was soft-deleted.
		{"ProjectMember.objects.bulk_create(", "memberships are created the way Plane's endpoint creates them"},
		{"ProjectUserProperty.objects.bulk_create(", "a member without a ProjectUserProperty has no view of the project"},
		{"ignore_conflicts=True", "a concurrent sign-in or a second run must not fail the batch"},
		{"is_active", "a removed admin membership must be re-activated, not duplicated"},
		{`X-API-Key`, "a changed project is proven through the console's own key"},
	} {
		if !strings.Contains(script, want.needle) {
			t.Errorf("plane-project-access script lacks %q: %s", want.needle, want.why)
		}
	}
	if strings.Contains(script, "ProjectMember.objects.create(") || strings.Contains(script, ".save()") {
		t.Error("plane-project-access creates memberships through save(); use bulk_create with ignore_conflicts like Plane's endpoint")
	}
}
