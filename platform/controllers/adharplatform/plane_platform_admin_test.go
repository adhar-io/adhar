package adharplatform

import (
	"strings"
	"testing"
)

// Plane's workspace, and the API key the console acts through, belong to the
// Keycloak platform admin — not to the local plane-admin@adhar.local account,
// which made every console-created project and work item read "Adhar Admin".
//
// The local account stays, for /god-mode/ only: the fork's god-mode sign-in is
// an email/password form checked against InstanceAdmin rows on its own admin
// session cookie, so a Keycloak identity cannot reach it.
//
// Guards, with comments stripped so the prose cannot satisfy them:
//   - the platform admin comes from the keycloak-directory ConfigMap (named in
//     the Role, read-only), never a hardcoded user,
//   - the account is pre-created by email with no usable password, so the
//     fork's email lookup links the first Keycloak sign-in to it,
//   - ownership moves with queryset updates (BaseModel.save() outside a request
//     blanks created_by/updated_by),
//   - the old console key is retired only after the Secret carries the new one,
//     and the Secret is stamped so plane-console-reload restarts the console.
func TestPlaneWorkspaceAndConsoleKeyBelongToThePlatformAdmin(t *testing.T) {
	const rel = "application/plane/manifests/instance-setup.yaml"
	body := readStackFile(t, rel)
	code := withoutComments(body)

	env, _ := containerEnv(t, body, "Job", "setup")
	dir := env["KEYCLOAK_DIRECTORY_CONFIGMAP"]
	if dir != "keycloak-directory" {
		t.Fatalf("KEYCLOAK_DIRECTORY_CONFIGMAP = %q; the keycloak package publishes the realm directory as keycloak-directory", dir)
	}
	granted := false
	role := stackObject(t, rel, "Role", "plane-instance-setup")
	for _, rule := range sliceOfMaps(role["rules"]) {
		if !listHas(toSlice(rule["resources"]), "configmaps") {
			continue
		}
		names, verbs := toSlice(rule["resourceNames"]), toSlice(rule["verbs"])
		if len(names) != 1 || toString(names[0]) != dir {
			t.Errorf("Role plane-instance-setup grants %v on ConfigMaps %v; it needs get on %q only", verbs, names, dir)
		}
		for _, v := range verbs {
			if toString(v) != "get" {
				t.Errorf("Role plane-instance-setup grants %s on ConfigMap %s; the script only reads it", toString(v), dir)
			}
		}
		granted = true
	}
	if !granted {
		t.Errorf("Role plane-instance-setup cannot read ConfigMap %s, where the platform admin is resolved from", dir)
	}

	for _, want := range []struct{ needle, why string }{
		{`f"{api}/configmaps/{name}"`, "the platform admin is read from the directory ConfigMap"},
		{`ADMIN_GROUP = "platform-admin"`, "the platform admin is the group KEYCLOAK_ADMIN_GROUPS maps to workspace admin"},
		{"u.set_unusable_password()", "the pre-created account has no password: Keycloak is its only way in"},
		{`last_login_medium="keycloak"`, "the pre-created account is a Keycloak account"},
		{"User.objects.filter(email=mail).first()", "an account that already signed in is reused, never duplicated"},
		{"Workspace.objects.filter(pk=ws.pk).update(owner=platform_admin)", "ownership moves with an update, keeping history fields"},
		{"InstanceAdmin.objects.get_or_create(instance=instance, user=local_admin", "the break-glass account stays the god-mode admin"},
		{"token = APIToken.objects.create(user=key_user", "the console key is minted for the platform admin"},
		{`"adhar.io/token-created-at": minted`, "the Secret is stamped so a rotated key restarts the console"},
		{"owner = ws.owner", "a failed lookup keeps the current owner instead of migrating back"},
	} {
		if !strings.Contains(code, want.needle) {
			t.Errorf("plane instance-setup lacks %q: %s", want.needle, want.why)
		}
	}
	for _, gone := range []string{`"owner": user}`, "APIToken.objects.create(user=user"} {
		if strings.Contains(code, gone) {
			t.Errorf("plane instance-setup still gives the workspace or the console key to the local admin (%q)", gone)
		}
	}

	write := strings.Index(code, `kube("PATCH", f"{base}/{name}`)
	retire := strings.Index(code, ".exclude(pk=token.pk).update(is_active=False)")
	if retire < 0 {
		t.Error("plane instance-setup never deactivates the superseded console key")
	} else if write < 0 || retire < write {
		t.Error("plane instance-setup deactivates the old console key before the Secret carries the new one: a failed write would leave the console with a dead key")
	}

	reload := withoutComments(readStackFile(t, "application/plane/manifests/console-reload.yaml"))
	if !strings.Contains(reload, `{.metadata.annotations.adhar\.io/token-created-at}`) {
		t.Error("plane-console-reload compares only the Secret's creationTimestamp: a rotated key leaves it unchanged, so the console keeps the deactivated key")
	}
}
