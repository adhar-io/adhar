package adharplatform

import (
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// rustfsOIDCDoc is the slice of an object in data/rustfs/manifests/oidc.yaml
// these guards read: enough of a ConfigMap and of a Job's pod template.
type rustfsOIDCDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Command      []string `yaml:"command"`
					Args         []string `yaml:"args"`
					VolumeMounts []struct {
						Name      string `yaml:"name"`
						MountPath string `yaml:"mountPath"`
					} `yaml:"volumeMounts"`
				} `yaml:"containers"`
				Volumes []struct {
					Name      string `yaml:"name"`
					ConfigMap *struct {
						Name string `yaml:"name"`
					} `yaml:"configMap"`
				} `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// rustfsActivation returns the rustfs-oidc-activate Job and the ConfigMap that
// holds the script it runs, plus that script's key. It fails the test when the
// Job carries its script inline, or runs a file no ConfigMap in the package
// provides.
func rustfsActivation(t *testing.T) (job, script rustfsOIDCDoc, key string) {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(readStackFile(t, "data/rustfs/manifests/oidc.yaml")))
	configMaps := map[string]rustfsOIDCDoc{}
	found := false
	for {
		var d rustfsOIDCDoc
		if err := dec.Decode(&d); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("decoding data/rustfs/manifests/oidc.yaml: %v", err)
			}
			break
		}
		switch {
		case d.Kind == "ConfigMap":
			configMaps[d.Metadata.Name] = d
		case d.Kind == "Job" && d.Metadata.Name == "rustfs-oidc-activate":
			job, found = d, true
		}
	}
	if !found {
		t.Fatal("no rustfs-oidc-activate Job in data/rustfs/manifests/oidc.yaml")
	}
	cs := job.Spec.Template.Spec.Containers
	if len(cs) != 1 {
		t.Fatalf("the rustfs-oidc-activate Job has %d containers; these guards read one", len(cs))
	}
	argv := append(append([]string{}, cs[0].Command...), cs[0].Args...)
	for _, a := range argv {
		if strings.Contains(a, "\n") {
			t.Fatal("the rustfs-oidc-activate Job carries its script inline. Argo CD leaves hooks out " +
				"of the sync status, so a push that changes only an inline hook script leaves the app " +
				"Synced and the new script never runs; keep it in a tracked ConfigMap the Job mounts")
		}
	}
	for _, a := range argv {
		for _, m := range cs[0].VolumeMounts {
			prefix := strings.TrimSuffix(m.MountPath, "/") + "/"
			if !strings.HasPrefix(a, prefix) {
				continue
			}
			for _, v := range job.Spec.Template.Spec.Volumes {
				if v.Name != m.Name || v.ConfigMap == nil {
					continue
				}
				cm, ok := configMaps[v.ConfigMap.Name]
				if !ok {
					t.Fatalf("the rustfs-oidc-activate Job mounts ConfigMap %q, which oidc.yaml does not "+
						"declare", v.ConfigMap.Name)
				}
				key = strings.TrimPrefix(a, prefix)
				if strings.TrimSpace(cm.Data[key]) == "" {
					t.Fatalf("the rustfs-oidc-activate Job runs %q, but ConfigMap %q has no %q key",
						a, cm.Metadata.Name, key)
				}
				return job, cm, key
			}
		}
	}
	t.Fatalf("the rustfs-oidc-activate Job runs %q, which is not a file from a mounted ConfigMap", argv)
	return job, script, key
}

// rustfsActivationScript returns the shell script the rustfs-oidc-activate Job
// runs, with whole-line comments removed so a guard cannot be satisfied by the
// script's own explanation.
func rustfsActivationScript(t *testing.T) string {
	t.Helper()
	_, cm, key := rustfsActivation(t)
	return withoutComments(cm.Data[key])
}

// The activation script must re-run when it changes, and that takes it being
// part of the app's TRACKED state.
//
// The Job is a PostSync hook, and Argo CD leaves hooks out of the sync status:
// a push that changed only its inline script left the app Synced at the new
// revision, so no automated sync started and the new script never ran. The
// half-hourly self-heal does not rescue it either — rustfs-buckets' TTL expires,
// the app goes OutOfSync, and Argo CD heals a revision it has already synced
// with a SELECTIVE sync of only the drifted objects, which runs no hooks (argo-cd
// v3.5.1 controller/appcontroller.go autoSync; live on 2026-10-26 every heal was
// "Partial sync operation" of Job/rustfs-buckets). With the script in a
// ConfigMap, editing it is a diff on a tracked object, the next automated sync is
// a FULL sync of the new revision, and the hook runs with the new script.
// BeforeHookCreation is what lets that same-named Job be created again.
func TestRustFSActivationReRunsWhenItsScriptChanges(t *testing.T) {
	job, cm, key := rustfsActivation(t)

	if hook := job.Metadata.Annotations["argocd.argoproj.io/hook"]; hook == "" {
		t.Error("rustfs-oidc-activate is no longer an Argo CD hook; as a plain resource its immutable " +
			"Job template cannot be updated in place, and its wait for the Keycloak client secret " +
			"would hold the app's own sync")
	}
	if p := job.Metadata.Annotations["argocd.argoproj.io/hook-delete-policy"]; !strings.Contains(p, "BeforeHookCreation") {
		t.Errorf("rustfs-oidc-activate has hook-delete-policy %q; without BeforeHookCreation the Job "+
			"from the previous sync blocks a new one of the same name", p)
	}
	if hook := cm.Metadata.Annotations["argocd.argoproj.io/hook"]; hook != "" {
		t.Errorf("ConfigMap %q holds the activation script but is itself a %s hook; hooks are left "+
			"out of the sync status, so editing the script no longer triggers the sync that runs it",
			cm.Metadata.Name, hook)
	}
	if !strings.Contains(cm.Data[key], "set -euo pipefail") {
		t.Errorf("the activation script %s/%s does not run with `set -euo pipefail`; a failed step "+
			"would let the Job report success with the login still broken", cm.Metadata.Name, key)
	}
}

// The RustFS console login must resolve through RustFS GROUPS named after the
// platform's Keycloak groups, not through policies alone.
//
// RustFS 1.0.1 turns every value of the `groups` claim into a policy name and
// refuses the login unless ALL of them exist
// (all_oidc_policies_resolved, rustfs/src/admin/service/federated_identity.rs)
// — unless its IAM group mappings resolve first (sts_policy_db_get), which
// skip any claim value that is not a RustFS group. With only the policies
// written, the platform admin, who is also in `ws-team-platform`, was refused
// on every callback with "OIDC policy mapping did not resolve to current
// policies" (400, logged only in /logs/rustfs.log; live 2026-10-25/26), while a
// user in a single platform group signed in fine — so it looked like it
// worked. Every team member has a ws-team-* group and every tenant an org-*
// one, so this is everyone sooner or later. Reproduced against the 1.0.1 image
// (the live digest) with a mock IdP and this script run in the Job's own image:
// same groups, 400 before, 302 after, and each session scoped to its tier.
func TestRustFSLoginResolvesThroughPlatformGroups(t *testing.T) {
	script := rustfsActivationScript(t)

	// Policies the Job writes.
	written := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*admin_policy\s+(\S+)\s`).FindAllStringSubmatch(script, -1) {
		written[m[1]] = true
	}

	// The shell function that attaches a policy to a GROUP, found by what it
	// calls rather than by its name.
	attach := strings.Index(script, "/rustfs/admin/v3/set-user-or-group-policy")
	if attach < 0 || !strings.Contains(script[attach:], "isGroup=true") {
		t.Fatal("the activation Job attaches no policy to a RustFS group (set-user-or-group-policy ... " +
			"isGroup=true); without group mappings RustFS needs EVERY group claim value to name a " +
			"policy, so anyone also in a ws-team-* or org-* group is refused with \"OIDC policy " +
			"mapping did not resolve to current policies\"")
	}
	defs := regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\(\)\s*\{`).FindAllStringSubmatchIndex(script[:attach], -1)
	if len(defs) == 0 {
		t.Fatal("the group policy attachment is not inside a shell function; this guard reads the " +
			"group -> policy pairs from that function's calls")
	}
	last := defs[len(defs)-1]
	fn, body := script[last[2]:last[3]], script[last[0]:]

	// A policy can only be attached to a group that exists: the admin API
	// answers 404 NoSuchResource otherwise, and the mapping never lands.
	create := strings.Index(body, "/rustfs/admin/v3/update-group-members")
	if create < 0 || create > strings.Index(body, "/rustfs/admin/v3/set-user-or-group-policy") {
		t.Errorf("%s() does not create the group (update-group-members) before attaching its policy; "+
			"RustFS refuses to attach a policy to a group that does not exist", fn)
	}

	mapped := map[string]string{}
	first := -1
	for _, m := range regexp.MustCompile(`(?m)^\s*`+regexp.QuoteMeta(fn)+`\s+(\S+)\s+(\S+)\s*$`).FindAllStringSubmatchIndex(script, -1) {
		group, policy := script[m[2]:m[3]], script[m[4]:m[5]]
		mapped[group] = policy
		if first < 0 {
			first = m[0]
		}
		if !written[policy] {
			t.Errorf("group %q is mapped to policy %q, which the Job never writes; RustFS refuses the "+
				"mapping with NoSuchPolicy and the group's members cannot sign in", group, policy)
		}
	}

	// The groups must be ones the realm actually has, spelt exactly as the
	// claim carries them.
	realm := map[string]bool{}
	for _, m := range regexp.MustCompile(`group-[a-z0-9-]+-payload\.json:\s*\|\s*\n\s*\{"name":"([^"]+)"\}`).
		FindAllStringSubmatch(readStackFile(t, "security/keycloak/manifests/keycloak-config.yaml"), -1) {
		realm[m[1]] = true
	}
	if len(realm) == 0 {
		t.Fatal("found no group payloads in security/keycloak/manifests/keycloak-config.yaml; this " +
			"guard can no longer check the mapped groups against the realm")
	}
	for _, g := range []string{"platform-admin", "platform-developer", "platform-viewer"} {
		if mapped[g] == "" {
			t.Errorf("platform group %q is not mapped to a RustFS group; its members are refused the "+
				"console login as soon as they also belong to any group without a policy", g)
		}
	}
	for g := range mapped {
		if !realm[g] {
			t.Errorf("RustFS group %q matches no Keycloak group the realm creates; the claim never "+
				"carries it, so the mapping is dead", g)
		}
	}

	// The provider check ends the Job early with `exit 0` once the provider is
	// served, which on a converged cluster is every run; the mapping must come
	// before it or it is never written there.
	if exit := strings.Index(script, "exit 0"); first >= 0 && exit >= 0 && exit < first {
		t.Error("the group mappings are written after the Job's first `exit 0`; on a cluster whose " +
			"provider is already served the Job exits before reaching them")
	}
}

// Policy names stay bare, and so do the claim values.
//
// RustFS never resolves a claim policy name containing `/`
// (is_safe_claim_policy_name allows [A-Za-z0-9_:.-] only). And a
// `/platform-admin` policy is worse than useless: `add-canned-policy?name=/x`
// answers 200 and keeps a separate in-memory entry, but the store collapses the
// slash and writes the SAME object as `x`, so `x` is silently replaced on disk
// and the change surfaces only at the next restart (verified on 1.0.1). The Job
// wrote both spellings for a while.
//
// A path-form claim value does still sign in today, but only by accident: the
// group lookup goes through the same path cleaning, so `/platform-admin` finds
// the `platform-admin` group (verified on 1.0.1). Nothing documents that, and the
// policy-name fallback refuses such a value outright, so the realm keeps sending
// bare names.
func TestRustFSClaimPolicyNamesStayBare(t *testing.T) {
	script := rustfsActivationScript(t)
	for _, m := range regexp.MustCompile(`(?m)^\s*admin_policy\s+(\S+)\s`).FindAllStringSubmatch(script, -1) {
		if strings.Contains(m[1], "/") {
			t.Errorf("the activation Job writes policy %q; RustFS stores it over %q (visible after the "+
				"next restart) and never resolves a claim value containing '/'", m[1], strings.TrimLeft(m[1], "/"))
		}
	}

	kc := readStackFile(t, "security/keycloak/manifests/keycloak-config.yaml")
	mappers := regexp.MustCompile(`"protocolMapper":\s*"oidc-group-membership-mapper"[^}]*\}`).FindAllString(kc, -1)
	if len(mappers) == 0 {
		t.Fatal("no oidc-group-membership-mapper in security/keycloak/manifests/keycloak-config.yaml; " +
			"this guard no longer sees what the groups claim carries")
	}
	for _, m := range mappers {
		if !regexp.MustCompile(`"full\.path":\s*"false"`).MatchString(m) {
			t.Errorf("a Keycloak groups mapper does not set \"full.path\": \"false\". A path-form value "+
				"(/platform-admin) is never a legal RustFS claim policy name and reaches the RustFS "+
				"group only because RustFS 1.0.1 collapses '//' in its storage paths, so the console "+
				"login would rest on that accident:\n%s", m)
		}
	}
}
