package adharplatform

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Argo CD's health checks for Volcano's kinds (design 0025 §6.7, PLAT-2.2), run
// rather than read. TestArgoCDKnowsVolcanoInEveryRendering proves each check is
// present and names the right words; these prove what each one DECIDES for every
// phase Volcano has, in all four files Argo CD is configured from, so a check
// that reads the wrong field (a Volcano Job's phase is status.state.phase, a
// PodGroup's is status.phase) cannot hide behind a string match.

// argocdCMFiles are the four places argocd-cm is defined: the generator values
// and the rendered manifests the controller installs.
var argocdCMFiles = []string{
	"../../../hack/argocd/values.yaml",
	"../../../hack/argocd/values-ha.yaml",
	"resources/argocd/install.yaml",
	"resources/argocd/install-ha.yaml",
}

var volcanoHealthKeys = []string{
	"resource.customizations.health.batch.volcano.sh_Job",
	"resource.customizations.health.batch.volcano.sh_CronJob",
	"resource.customizations.health.scheduling.volcano.sh_PodGroup",
	"resource.customizations.health.scheduling.volcano.sh_Queue",
}

// argocdCM returns argocd-cm's data from one of the four files.
func argocdCM(t *testing.T, file string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	if strings.HasPrefix(filepath.Base(file), "values") {
		var v struct {
			Configs struct {
				CM map[string]any `json:"cm"`
			} `json:"configs"`
		}
		if err := yaml.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for k, val := range v.Configs.CM {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	for _, chunk := range strings.Split(string(b), "\n---\n") {
		if !strings.Contains(chunk, "name: argocd-cm\n") {
			continue
		}
		var cm struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(chunk), &cm); err == nil && cm.Kind == "ConfigMap" {
			return cm.Data
		}
	}
	t.Fatalf("%s has no argocd-cm ConfigMap", file)
	return nil
}

func normaliseLua(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// The same check in all four files: the values reach a cluster only through a
// regeneration, and the rendered manifests are what runs until one happens.
func TestArgoCDVolcanoHealthChecksAreOneScriptInEveryFile(t *testing.T) {
	first := map[string]string{}
	for _, file := range argocdCMFiles {
		cm := argocdCM(t, file)
		for _, key := range volcanoHealthKeys {
			body, ok := cm[key]
			if !ok {
				t.Errorf("%s has no %s", file, key)
				continue
			}
			// Argo CD runs gopher-lua, which is Lua 5.1: nothing from 5.2 or later.
			for _, newer := range []string{"goto ", "//", "<const>", "<close>", " & ", " | ", "<<", ">>"} {
				if strings.Contains(body, newer) {
					t.Errorf("%s %s uses %q, which Argo CD's Lua 5.1 does not have", file, key, newer)
				}
			}
			if f, seen := first[key]; !seen {
				first[key] = normaliseLua(body)
			} else if normaliseLua(body) != f {
				t.Errorf("%s %s differs from %s's; whichever is right, one of them is not running", file, key, argocdCMFiles[0])
			}
		}
	}
}

// luaLiteral writes a JSON-shaped value as a Lua table constructor.
func luaLiteral(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case bool:
		return fmt.Sprint(x)
	case float64:
		return fmt.Sprint(x)
	case string:
		b, _ := json.Marshal(x)
		return string(b)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = luaLiteral(e)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			b, _ := json.Marshal(k)
			parts[i] = "[" + string(b) + "] = " + luaLiteral(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	panic(fmt.Sprintf("no Lua literal for %T", v))
}

type luaHealthCase struct {
	name string
	obj  string // JSON
	want string // Healthy | Progressing | Degraded
}

// runLuaHealth evaluates one health script against objects, the way Argo CD
// does: the script's body with `obj` bound, returning a table with status and
// message.
func runLuaHealth(t *testing.T, lua, body string, cases []luaHealthCase) map[string][2]string {
	t.Helper()
	var script strings.Builder
	script.WriteString("local function health(obj)\n" + body + "\nend\n")
	for _, c := range cases {
		var obj any
		if err := json.Unmarshal([]byte(c.obj), &obj); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		fmt.Fprintf(&script, "do local hs = health(%s); io.write(%q, '\\t', tostring(hs.status), '\\t', tostring(hs.message), '\\n') end\n",
			luaLiteral(obj), c.name)
	}
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(script.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lua: %v\n%s", err, out)
	}
	got := map[string][2]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 {
			got[parts[0]] = [2]string{parts[1], parts[2]}
		}
	}
	return got
}

// Each check against every phase Volcano has. The Job and PodGroup phases are
// the lists kube-state-metrics publishes (one vocabulary for the dashboard and
// Argo CD); the Queue states are the CRD's own enum.
func TestArgoCDVolcanoHealthChecksJudgeEveryPhase(t *testing.T) {
	// Argo CD runs gopher-lua (Lua 5.1); Debian/Ubuntu install 5.1 as lua5.1.
	lua := ruleTool(t, "TestArgoCDVolcanoHealthChecksAreOneScriptInEveryFile still holds the four files together",
		"lua", "lua5.1", "lua5.4", "luajit")
	_, cfg := ksmCRSValuesFile(t)
	phases := map[string][]string{}
	for _, r := range cfg.Spec.Resources {
		for _, m := range r.Metrics {
			if s := m.Each.StateSet; s != nil {
				phases[r.GVK.Kind] = s.List
			}
		}
	}
	if len(phases["Job"]) == 0 || len(phases["PodGroup"]) == 0 || len(phases["Queue"]) == 0 {
		t.Fatalf("custom-resource-state.yaml lists no Job, PodGroup or Queue phases: %v", phases)
	}
	verdict := func(healthy, degraded []string) func(string) string {
		return func(p string) string {
			switch {
			case containsString(healthy, p):
				return "Healthy"
			case containsString(degraded, p):
				return "Degraded"
			}
			return "Progressing"
		}
	}
	// design 0025 §6.7
	table := map[string]struct {
		build   func(phase string) string
		phases  []string
		judge   func(string) string
		unknown string // an object the controller has not written status for
	}{
		"resource.customizations.health.batch.volcano.sh_Job": {
			build:   func(p string) string { return `{"status":{"state":{"phase":"` + p + `"},"running":1}}` },
			phases:  phases["Job"],
			judge:   verdict([]string{"Completed"}, []string{"Failed", "Terminated", "Aborted"}),
			unknown: `{"spec":{"minAvailable":5}}`,
		},
		"resource.customizations.health.scheduling.volcano.sh_PodGroup": {
			build:   func(p string) string { return `{"status":{"phase":"` + p + `","running":0}}` },
			phases:  phases["PodGroup"],
			judge:   verdict([]string{"Running", "Completed"}, nil), // a waiting gang is never Degraded
			unknown: `{"spec":{"minMember":5}}`,
		},
		"resource.customizations.health.scheduling.volcano.sh_Queue": {
			build:   func(p string) string { return `{"status":{"state":"` + p + `","running":2}}` },
			phases:  phases["Queue"],
			judge:   verdict([]string{"Open"}, []string{"Closed", "Closing"}),
			unknown: `{"spec":{"parent":"adhar-batch"}}`,
		},
	}
	for _, file := range argocdCMFiles {
		cm := argocdCM(t, file)
		for key, row := range table {
			var cases []luaHealthCase
			for _, p := range row.phases {
				cases = append(cases, luaHealthCase{name: p, obj: row.build(p), want: row.judge(p)})
			}
			cases = append(cases,
				luaHealthCase{name: "no status", obj: row.unknown, want: "Progressing"},
				// An object Argo CD has only from Git (never reconciled) has no status at all.
				luaHealthCase{name: "bare", obj: `{}`, want: "Progressing"})
			got := runLuaHealth(t, lua, cm[key], cases)
			for _, c := range cases {
				g, ok := got[c.name]
				switch {
				case !ok:
					t.Errorf("%s %s: the check returned nothing for %s", file, key, c.name)
				case g[0] != c.want:
					t.Errorf("%s %s: %s is %s, want %s (design 0025 §6.7)", file, key, c.name, g[0], c.want)
				case g[1] == "" || g[1] == "nil":
					t.Errorf("%s %s: %s has no message", file, key, c.name)
				}
			}
		}
		// A Volcano CronJob is a definition: a failed run is an alert
		// (BatchRunFailed), never the CronJob's health, or one bad night would
		// block the Application that owns it (design 0025 §6.6).
		got := runLuaHealth(t, lua, cm["resource.customizations.health.batch.volcano.sh_CronJob"], []luaHealthCase{
			{name: "fresh", obj: `{"spec":{"schedule":"0 2 * * *"}}`},
			{name: "after a failed run", obj: `{"spec":{"schedule":"0 2 * * *","suspend":true},"status":{"lastScheduleTime":"2026-11-14T02:00:00Z","active":[]}}`},
		})
		for _, name := range []string{"fresh", "after a failed run"} {
			if got[name][0] != "Healthy" {
				t.Errorf("%s: a Volcano CronJob %s is %q, want Healthy", file, name, got[name][0])
			}
		}
	}
}

// Volcano rewrites status all the time (a Queue's allocation every scheduling
// cycle, a running Job's counts per pod), and none of it is in Git. Argo CD must
// not refresh an Application for that, and must still watch the kinds it judges.
func TestArgoCDIgnoresVolcanoStatusChurnButWatchesItsKinds(t *testing.T) {
	for _, file := range argocdCMFiles {
		cm := argocdCM(t, file)
		if cm["resource.ignoreResourceUpdatesEnabled"] != "true" {
			t.Errorf("%s: resource.ignoreResourceUpdatesEnabled is %q; without it every ignoreResourceUpdates rule is inert",
				file, cm["resource.ignoreResourceUpdatesEnabled"])
		}
		var all struct {
			JSONPointers []string `json:"jsonPointers"`
		}
		if err := yaml.Unmarshal([]byte(cm["resource.customizations.ignoreResourceUpdates.all"]), &all); err != nil ||
			!containsString(all.JSONPointers, "/status") {
			t.Errorf("%s: ignoreResourceUpdates.all no longer ignores /status (%v); every Volcano status write would refresh its Application", file, all.JSONPointers)
		}
		for _, key := range []string{
			"resource.customizations.ignoreResourceUpdates.batch.volcano.sh_Job",
			"resource.customizations.ignoreResourceUpdates.scheduling.volcano.sh_Queue",
		} {
			if _, overridden := cm[key]; overridden {
				t.Errorf("%s sets %s, which replaces the /status rule for that kind; add to it instead", file, key)
			}
		}
		var exclusions []struct {
			APIGroups []string `json:"apiGroups"`
			Kinds     []string `json:"kinds"`
		}
		if err := yaml.Unmarshal([]byte(cm["resource.exclusions"]), &exclusions); err != nil {
			t.Fatalf("%s: resource.exclusions: %v", file, err)
		}
		excluded := map[string]bool{}
		for _, e := range exclusions {
			for _, g := range e.APIGroups {
				for _, k := range e.Kinds {
					excluded[g+"/"+k] = true
				}
			}
		}
		for _, gk := range []string{"scheduling.volcano.sh/PodGroup", "bus.volcano.sh/Command"} {
			if !excluded[gk] {
				t.Errorf("%s does not exclude %s (design 0025 §6.7)", file, gk)
			}
		}
		// The root queue is in Git and the CronJob and its runs are in the
		// resource tree: excluded, Argo CD could neither judge nor show them.
		for _, gk := range []string{"scheduling.volcano.sh/Queue", "batch.volcano.sh/Job", "batch.volcano.sh/CronJob"} {
			group, kind, _ := strings.Cut(gk, "/")
			if excluded[gk] || excluded[group+"/*"] || excluded["*/"+kind] || excluded["*/*"] {
				t.Errorf("%s excludes %s, so its health check never runs", file, gk)
			}
		}
	}
}
