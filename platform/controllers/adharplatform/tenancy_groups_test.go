package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
)

// Organisation membership lives in Keycloak and nowhere else (2026-10-31): the
// console adds and removes people in Keycloak groups and stores no members,
// roles or invitations of its own. Three groups carry it, and the console
// derives the same names on its side (apps/console/app/server/tenancy/
// org-access.ts in adhar-console), so these tests pin both the names and what
// each is granted. A drift between the two is silent: a binding to a group
// nobody is in grants nothing, and nothing reports it.
//
//	org-<org>             members  view on the organisation's home namespace
//	org-<org>-admin       admins   admin on every namespace of the organisation
//	ws-team-<org>.<team>  a team   admin in its project and dev/test, view in prod
//
// The team group used to be `ws-team-<team>`. Team slugs are unique only
// inside one organisation, so every organisation's team called `platform` WAS
// the platform's own team group — which Project/adhar binds as admin of adhar,
// dev and test — and the console seeded a `platform` team in every new
// organisation and put its first visitor in it.

const (
	tenancyTestOrg  = "acme"
	tenancyTestTeam = "platform" // deliberately the platform team's slug
)

// tenancyRequest is the function-go-templating request for one hierarchy XR.
func tenancyRequest(kind, name string, params map[string]any) map[string]any {
	return map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": map[string]any{
		"apiVersion": "platform.adhar.io/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": "adhar-system"},
		"spec":       map[string]any{"parameters": params},
	}}}}
}

func bindingSubjects(rb map[string]any) []string {
	var out []string
	for _, s := range toSlice(rb["subjects"]) {
		if m, ok := s.(map[string]any); ok {
			out = append(out, toString(m["name"]))
		}
	}
	sort.Strings(out)
	return out
}

// requireBinding asserts the composed RoleBinding `name` grants exactly
// `subjects` the ClusterRole `role`.
func requireBinding(t *testing.T, docs []map[string]any, name, role string, subjects ...string) {
	t.Helper()
	rb := composedManifest(docs, "RoleBinding", name)
	if rb == nil {
		t.Errorf("no composed RoleBinding %s", name)
		return
	}
	if got := toString(dig(rb, "roleRef", "name")); got != role {
		t.Errorf("RoleBinding %s grants %q, want %q", name, got, role)
	}
	sort.Strings(subjects)
	if got := bindingSubjects(rb); strings.Join(got, ",") != strings.Join(subjects, ",") {
		t.Errorf("RoleBinding %s binds %v, want %v", name, got, subjects)
	}
}

// refuseSubjects fails for any composed RoleBinding naming a group of
// `never` (group -> why it must not be bound here).
func refuseSubjects(t *testing.T, what string, docs []map[string]any, never map[string]string) {
	t.Helper()
	for _, rb := range manifestsOfKind(docs, "RoleBinding") {
		for _, s := range bindingSubjects(rb) {
			if why, ok := never[s]; ok {
				t.Errorf("%s binds %s in RoleBinding %v (%s): %s",
					what, s, dig(rb, "metadata", "name"), toString(dig(rb, "roleRef", "name")), why)
			}
		}
	}
}

// neverBound is what no project or environment of `org` may bind for `team`.
func neverBound(org, team string) map[string]string {
	return map[string]string{
		"oidc:ws-team-" + team: "the bare team group, shared by every organisation with a team of that name",
		"oidc:org-" + org:      "the organisation's MEMBER group, which only views its home namespace",
	}
}

// The console mirrored each member's organisation role into REALM-WIDE
// Keycloak groups — ws-role-owner, ws-role-admin, ws-role-billing — held by
// ClusterRoleBindings, so an owner or admin of ONE organisation administered
// the members, teams, tokens and billing of EVERY organisation. Those checks
// are organisation-scoped now and the console answers them itself from
// org-<slug>-admin (and the platform admin groups); RBAC must not grant them
// realm-wide again, under the old names or any other.
func TestConsoleRBACGrantsNothingToRealmWideWorkspaceRoleGroups(t *testing.T) {
	const file = "core/adhar-console/manifests/console-rbac.yaml"
	docs := readYAMLDocs(t, filepath.Join(stackPackagesDir(t), filepath.FromSlash(file)))
	roles := map[string]map[string]any{}
	var bindings []map[string]any
	for _, d := range docs {
		switch d["kind"] {
		case "ClusterRole":
			roles[toString(dig(d, "metadata", "name"))] = d
		case "ClusterRoleBinding", "RoleBinding":
			bindings = append(bindings, d)
		}
	}
	// Self-check: the guard below reads these two; if the file moved or
	// stopped parsing into them it would pass by seeing nothing.
	if roles["adhar-console-admin"] == nil || roles["adhar-console-member"] == nil || len(bindings) < 2 {
		t.Fatalf("%s no longer holds adhar-console-admin/-member and their bindings; update this guard", file)
	}

	for _, retired := range []string{"adhar-console-workspace-admin", "adhar-console-workspace-owner", "adhar-console-billing"} {
		if roles[retired] != nil {
			t.Errorf("%s declares the ClusterRole %s again; organisation administration is answered by the "+
				"console from org-<slug>-admin, never by a realm-wide role", file, retired)
		}
		for _, b := range bindings {
			if toString(dig(b, "metadata", "name")) == retired || toString(dig(b, "roleRef", "name")) == retired {
				t.Errorf("%s binds %s again", file, retired)
			}
		}
	}

	// Organisation-scoped administration: every workspace-* resource, plus the
	// verbs of these that change an organisation. `organizations create` is
	// not one — creating an organisation is a platform decision and stays RBAC.
	changesAnOrganisation := map[string][]string{
		"organizations": {"update", "patch", "delete"}, "billing": {"update", "patch"},
		"notifications-broadcast": {"create"},
	}
	grantsOrgAdmin := func(role map[string]any) []string {
		var hits []string
		for _, raw := range toSlice(role["rules"]) {
			rule, _ := raw.(map[string]any)
			groups := toStrings(rule["apiGroups"])
			if !slices.Contains(groups, "console.adhar.io") && !slices.Contains(groups, "*") {
				continue
			}
			for _, res := range toStrings(rule["resources"]) {
				changing, scoped := changesAnOrganisation[res]
				for _, v := range toStrings(rule["verbs"]) {
					if res == "*" || strings.HasPrefix(res, "workspace-") || (scoped && (v == "*" || slices.Contains(changing, v))) {
						hits = append(hits, res+":"+v)
					}
				}
			}
		}
		sort.Strings(hits)
		return hits
	}
	platformAdmins := map[string]bool{"oidc:platform-admin": true, "oidc:platform-engineer": true, "oidc:super-admin": true}
	for _, b := range bindings {
		name := toString(dig(b, "metadata", "name"))
		for _, s := range bindingSubjects(b) {
			if strings.HasPrefix(s, "oidc:ws-role-") {
				t.Errorf("%s: %s binds %s — a realm-wide workspace-role group", file, name, s)
			}
		}
		role := roles[toString(dig(b, "roleRef", "name"))]
		if role == nil {
			continue
		}
		hits := grantsOrgAdmin(role)
		if len(hits) == 0 {
			continue
		}
		for _, s := range bindingSubjects(b) {
			if !platformAdmins[s] {
				t.Errorf("%s: %s grants %s organisation administration (%s) in EVERY organisation; only "+
					"platform admins hold that — an organisation's own admins are org-<slug>-admin, which the console reads",
					file, name, s, strings.Join(hits, " "))
			}
		}
	}

	// Nor may any other package bind a ws-role-* group.
	err := filepath.WalkDir(stackPackagesDir(t), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".tmpl")) {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(withoutComments(string(b)), "ws-role-") {
			rel, _ := filepath.Rel(stackPackagesDir(t), path)
			t.Errorf("%s names a ws-role-* group; those realm-wide mirrors of organisation roles are retired", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A team IS the Keycloak group `ws-team-<organisation>.<team>`, published on
// its status. `.` separates the two because neither slug may contain one
// (both XRDs: `^[a-z]([-a-z0-9]*[a-z0-9])?$`), so no other (organisation,
// team) pair spells the same group — `<org>-<team>` would not do: (acme,
// platform-x) and (acme-platform, x) collide.
func TestTeamPublishesAnOrganisationQualifiedGroup(t *testing.T) {
	for _, c := range []struct{ xr, org, team, want string }{
		// The console's Team XR is named <org>-<team>; the group follows the
		// parameters, not the object name.
		{tenancyTestOrg + "-" + tenancyTestTeam, tenancyTestOrg, tenancyTestTeam, "ws-team-acme.platform"},
		// The platform's own team (application/adhar-supply-chain 95-default-tenancy.yaml).
		{"platform", "adhar", "platform", "ws-team-adhar.platform"},
	} {
		docs := renderGoTemplate(t, tenancyRequest("Team", c.xr, map[string]any{"name": c.team, "organisation": c.org}),
			"compositions", "team", "local.yaml")
		st := docOfKind(docs, "Team")
		if st == nil {
			t.Fatalf("the Team composition renders no status for %s", c.xr)
		}
		if got := toString(dig(st, "status", "group")); got != c.want {
			t.Errorf("team %s of %s publishes group %q, want %q", c.team, c.org, got, c.want)
		}
	}
}

// The project binds its own organisation's team group and admin group — and
// nothing else. On the old spelling a project of team `platform` in acme bound
// `ws-team-platform`, the platform team's group.
func TestProjectBindsTheTeamGroupOfItsOwnOrganisation(t *testing.T) {
	for _, c := range []struct{ project, org, team string }{
		{"shop", tenancyTestOrg, tenancyTestTeam},
		// The platform's own project, named after its organisation, which
		// composes no namespace of its own but still its team's access.
		{"adhar", "adhar", "platform"},
	} {
		req := tenancyRequest("Project", c.project, map[string]any{"name": c.project, "organisation": c.org, "team": c.team})
		req["extraResources"] = map[string]any{"applications": map[string]any{"items": []any{}}}
		docs := renderGoTemplate(t, req, "compositions", "project", "local.yaml")

		requireBinding(t, docs, c.project+"-team-admin", "admin",
			"oidc:ws-team-"+c.org+"."+c.team, "oidc:org-"+c.org+"-admin")
		refuseSubjects(t, "project "+c.project, docs, neverBound(c.org, c.team))

		// Its environments are told whose they are, so they bind the same groups.
		n := 0
		for _, d := range docs {
			if d["kind"] != "Environment" || d["spec"] == nil {
				continue
			}
			n++
			p, _ := dig(d, "spec", "parameters").(map[string]any)
			if toString(p["organisation"]) != c.org || toString(p["team"]) != c.team {
				t.Errorf("project %s composes Environment %v for %v/%v, want %s/%s",
					c.project, dig(d, "metadata", "name"), p["organisation"], p["team"], c.org, c.team)
			}
		}
		if n != 3 {
			t.Errorf("project %s composes %d Environments, want dev, test and prod", c.project, n)
		}
	}
}

// Each environment binds the owning team's group of ITS organisation with the
// environment's access, and the organisation's admins as admin.
func TestEnvironmentBindsTheTeamGroupOfItsOwnOrganisation(t *testing.T) {
	for _, c := range []struct{ env, access string }{{"dev", "admin"}, {"prod", "view"}} {
		ns := "shop-" + c.env
		docs := renderGoTemplate(t, tenancyRequest("Environment", ns, map[string]any{
			"name": ns, "tier": c.env, "environment": c.env, "organisation": tenancyTestOrg,
			"team": tenancyTestTeam, "project": "shop", "access": c.access,
		}), "compositions", "env", "local.yaml")

		requireBinding(t, docs, ns+"-team-"+c.access, c.access, "oidc:ws-team-acme.platform")
		requireBinding(t, docs, ns+"-org-admin", "admin", "oidc:org-acme-admin")
		refuseSubjects(t, "environment "+ns, docs, neverBound(tenancyTestOrg, tenancyTestTeam))
	}
}

// Membership of an organisation is `view` on its home namespace and nothing
// more: every member of org-<slug> is in it by being added to the
// organisation, and administration is the separate org-<slug>-admin group
// (whose members are also in org-<slug>). Nothing in the hierarchy may bind
// the member group above view.
func TestOrganisationMemberGroupOnlyViews(t *testing.T) {
	docs := renderGoTemplate(t, tenancyRequest("Organisation", tenancyTestOrg, map[string]any{"name": tenancyTestOrg}),
		"compositions", "organisation", "local.yaml")
	requireBinding(t, docs, "acme-org-view", "view", "oidc:org-acme")
	requireBinding(t, docs, "acme-org-admin", "admin", "oidc:org-acme-admin")
	st := docOfKind(docs, "Organisation")
	if toString(dig(st, "status", "memberGroup")) != "org-acme" || toString(dig(st, "status", "adminGroup")) != "org-acme-admin" {
		t.Errorf("the Organisation status publishes memberGroup=%v adminGroup=%v, want org-acme / org-acme-admin",
			dig(st, "status", "memberGroup"), dig(st, "status", "adminGroup"))
	}

	// Every level that binds organisation groups, rendered for acme.
	project := tenancyRequest("Project", "shop", map[string]any{"name": "shop", "organisation": tenancyTestOrg, "team": tenancyTestTeam})
	project["extraResources"] = map[string]any{"applications": map[string]any{"items": []any{}}}
	rendered := map[string][]map[string]any{
		"organisation": docs,
		"project":      renderGoTemplate(t, project, "compositions", "project", "local.yaml"),
		"environment": renderGoTemplate(t, tenancyRequest("Environment", "shop-dev", map[string]any{
			"name": "shop-dev", "tier": "dev", "organisation": tenancyTestOrg, "team": tenancyTestTeam, "project": "shop",
		}), "compositions", "env", "local.yaml"),
	}
	for level, out := range rendered {
		for _, rb := range manifestsOfKind(out, "RoleBinding") {
			for _, s := range bindingSubjects(rb) {
				if s == "oidc:org-acme" && toString(dig(rb, "roleRef", "name")) != "view" {
					t.Errorf("the %s composition binds the member group oidc:org-acme to %v in %v",
						level, dig(rb, "roleRef", "name"), dig(rb, "metadata", "name"))
				}
			}
		}
	}

	// And no other composition spells the member group for a binding at all:
	// adding one is a decision this guard should be extended to render.
	adminSuffix := regexp.MustCompile(`^(%s|\{\{[^}]*\}\})-admin`)
	walkConfiguration(t, "compositions", func(rel, text string) {
		for _, i := range indexesOf(text, "oidc:org-") {
			if !adminSuffix.MatchString(text[i+len("oidc:org-"):]) {
				t.Errorf("%s:%d binds the organisation MEMBER group; bind it to `view` only and render it in "+
					"TestOrganisationMemberGroupOnlyViews", rel, lineOf(text, i))
			}
		}
	})
}

// No manifest the platform ships names a team group without its organisation.
// The compositions are the binding half; the rest of the stack and the
// examples are what people copy.
func TestNoCompositionBindsAnUnqualifiedTeamGroup(t *testing.T) {
	// What may follow `ws-team-`: <org>.<team> as a printf, as template
	// actions, as a literal, or as prose placeholders — or a `*` pattern.
	qualified := regexp.MustCompile(`^(%s\.%s|\*|(\{\{[^}]*\}\}|[a-z0-9][a-z0-9-]*|<[^>]+>)\.(\{\{|[a-z0-9]|<))`)
	check := func(rel, text string) {
		for _, i := range indexesOf(text, "ws-team-") {
			if !qualified.MatchString(text[i+len("ws-team-"):]) {
				t.Errorf("%s:%d names a team group without its organisation (want ws-team-<org>.<team>): %s",
					rel, lineOf(text, i), strings.TrimSpace(lineAt(text, i)))
			}
		}
	}
	// Self-check: the three compositions that build the group still do, in
	// the qualified spelling — if they stopped matching, the walk below
	// would have nothing to find.
	seen := map[string]bool{}
	walkConfiguration(t, "", func(rel, text string) {
		check("controlplane/configuration/"+rel, text)
		if strings.Contains(text, `ws-team-%s.%s`) {
			seen[rel] = true
		}
	})
	for _, want := range []string{"compositions/team/local.yaml", "compositions/project/local.yaml", "compositions/env/local.yaml"} {
		if !seen[filepath.FromSlash(want)] {
			t.Errorf("%s does not build the team group as ws-team-%%s.%%s (organisation, team)", want)
		}
	}
	for _, root := range []string{filepath.Join(stackPackagesDir(t), ".."), examplesDir(t)} {
		base := filepath.Base(root)
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".tmpl")) {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			check(filepath.Join(base, rel), blankComments(string(b)))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The console mirrors this name (PLATFORM_ORG_SLUG = 'adhar', the default
// workspace's organisation): it is where the default workspace's people land,
// whose admin group is admin of adhar, dev, test and prod, and which the
// console refuses to delete. Bootstrap's Gitea organisation is the same one.
func TestThePlatformOrganisationIsAdhar(t *testing.T) {
	const platformOrg = "adhar"
	byKind := map[string]map[string]any{}
	for _, d := range readYAMLDocs(t, filepath.Join(stackPackagesDir(t), "application", "adhar-supply-chain", "manifests", "95-default-tenancy.yaml")) {
		byKind[toString(d["kind"])] = d
	}
	org, team, project := byKind["Organisation"], byKind["Team"], byKind["Project"]
	if org == nil || team == nil || project == nil {
		t.Fatal("95-default-tenancy.yaml no longer seeds an Organisation, a Team and a Project")
	}
	if toString(dig(org, "metadata", "name")) != platformOrg || toString(dig(org, "spec", "parameters", "name")) != platformOrg {
		t.Errorf("the platform's own Organisation is %v (%v), want %s — the console's PLATFORM_ORG_SLUG mirrors it",
			dig(org, "metadata", "name"), dig(org, "spec", "parameters", "name"), platformOrg)
	}
	if toString(dig(team, "spec", "parameters", "organisation")) != platformOrg ||
		toString(dig(project, "spec", "parameters", "organisation")) != platformOrg ||
		toString(dig(project, "spec", "parameters", "team")) != toString(dig(team, "spec", "parameters", "name")) {
		t.Errorf("the platform's team and project do not belong to organisation %s", platformOrg)
	}
	// An XR that names no organisation is the platform's.
	for _, f := range []string{"team", "project", "env"} {
		if !strings.Contains(readConfigurationFile(t, "compositions", f, "local.yaml"), `dig "organisation" "`+platformOrg+`" $p`) {
			t.Errorf("compositions/%s/local.yaml does not default the organisation to %s", f, platformOrg)
		}
	}
	envXRD := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd", "env.xrd.yaml"))[0]
	versions := toSlice(dig(envXRD, "spec", "versions"))
	if len(versions) == 0 || toString(dig(versions[0].(map[string]any), "schema", "openAPIV3Schema", "properties", "spec",
		"properties", "parameters", "properties", "organisation", "default")) != platformOrg {
		t.Errorf("the Environment XRD does not default organisation to %s", platformOrg)
	}
	if globals.GiteaPlatformOrg != platformOrg {
		t.Errorf("bootstrap creates Gitea organisation %q, but the platform's Organisation is %q", globals.GiteaPlatformOrg, platformOrg)
	}
}

// An organisation named `<x>-admin` would have the member group
// `org-<x>-admin` — the ADMIN group of organisation <x> — so whoever joined
// it would administer <x>. The XRD refuses the name (ratcheting keeps any
// existing object valid); the console refuses it before creating anything.
func TestAnOrganisationNameCannotSpellAnotherOrganisationsAdminGroup(t *testing.T) {
	xrd := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd", "organisation.xrd.yaml"))[0]
	versions := toSlice(dig(xrd, "spec", "versions"))
	if len(versions) == 0 {
		t.Fatal("organisation.xrd.yaml has no versions")
	}
	name, _ := dig(versions[0].(map[string]any), "schema", "openAPIV3Schema", "properties", "spec",
		"properties", "parameters", "properties", "name").(map[string]any)
	if name == nil {
		t.Fatal("organisation.xrd.yaml has no spec.parameters.name")
	}
	for _, raw := range toSlice(name["x-kubernetes-validations"]) {
		rule, _ := raw.(map[string]any)
		if strings.ReplaceAll(toString(rule["rule"]), " ", "") == `!self.endsWith('-admin')` {
			return
		}
	}
	t.Error("the Organisation XRD accepts a name ending in -admin; its member group would be another organisation's admin group")
}

// walkConfiguration calls fn with every YAML file under the control-plane
// configuration (or one subdirectory of it), comments blanked.
// The console's service account writes groups, memberships and accounts
// (manage-users), looks people up (view-users, query-users, query-groups) and
// reads whether the realm has an SMTP server (view-realm). It never changes the
// realm itself — identity providers, clients, flows, keys — so it must not hold
// manage-realm: with the membership pages now writing to Keycloak on every
// add and remove, a bug in those paths would otherwise put the whole realm at
// risk. An older release granted it, so the job must also take it away.
func TestConsoleServiceAccountNeverHoldsManageRealm(t *testing.T) {
	src := readStackFile(t, "security/keycloak/manifests/keycloak-config.yaml")
	start := strings.Index(src, "Grant the adhar-console client's SERVICE ACCOUNT")
	if start < 0 {
		t.Fatal("keycloak-config.yaml no longer grants the adhar-console service account its roles; re-check this test")
	}
	end := strings.Index(src[start:], "publish_directory")
	if end < 0 {
		t.Fatal("cannot find the end of the console service-account block")
	}
	block := blankComments(src[start : start+end])
	grant := regexp.MustCompile(`rm_roles=\$\(kc [^\n]*\n[^\n]*select\(([^']*)\)\]'\)`).FindStringSubmatch(block)
	if grant == nil {
		t.Fatal("cannot find the realm-management roles granted to the console service account")
	}
	if strings.Contains(grant[1], "manage-realm") {
		t.Errorf("the console service account is granted manage-realm: %s", grant[1])
	}
	for _, need := range []string{"manage-users", "view-users", "query-users", "query-groups", "view-realm"} {
		if !strings.Contains(grant[1], `"`+need+`"`) {
			t.Errorf("the console service account is not granted %s, which it needs", need)
		}
	}
	if !regexp.MustCompile(`(?s)select\(\.name=="manage-realm"\).*kc -X DELETE --data "\$\{excess\}" "\$\{REALM_URL\}/users/\$\{sa_uid\}/role-mappings/clients/\$\{rm_id\}"`).MatchString(block) {
		t.Error("the job does not revoke manage-realm from a console service account an older release granted it to")
	}
}

func walkConfiguration(t *testing.T, sub string, fn func(rel, text string)) {
	t.Helper()
	root := controlPlaneDir(t)
	err := filepath.WalkDir(filepath.Join(root, sub), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fn(rel, blankComments(string(b)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// blankComments empties whole-line `#` comments, like withoutComments, but
// keeps the line so a finding can name the file's own line number.
func blankComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

func indexesOf(s, sub string) []int {
	var out []int
	for off := 0; ; {
		i := strings.Index(s[off:], sub)
		if i < 0 {
			return out
		}
		out = append(out, off+i)
		off += i + len(sub)
	}
}

func lineOf(s string, i int) int { return strings.Count(s[:i], "\n") + 1 }

func lineAt(s string, i int) string {
	start := strings.LastIndex(s[:i], "\n") + 1
	end := strings.Index(s[i:], "\n")
	if end < 0 {
		return s[start:]
	}
	return s[start : i+end]
}
