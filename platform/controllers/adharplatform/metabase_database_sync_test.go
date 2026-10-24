package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Metabase (and the console's Decide pages) showed only its Sample Database.
// metabase-database-sync enrols application databases — and the console's SQL
// Editor runs through Metabase's ADMIN session, so every registered database is
// queryable by every console user. These pin the properties that make that safe.
func TestMetabaseDatabaseSyncIsScopedAndReadOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "data", "metabase", "manifests", "database-sync.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for what, want := range map[string]string{
		"platform databases (Keycloak, console) are out unless opted in":   `return ns != PLATFORM_NS or labels.get("adhar.io/bi") == "true"`,
		"the role is read-only by construction":                            `"inRoles": ["pg_read_all_data"]`,
		"a team's own managed roles are never overwritten":                 "it declares managed roles of its own",
		"entries are keyed on the connection, not the dropped description": `if det.get("user") != ROLE or not host.endswith(".svc.cluster.local")`,
		"duplicates are cleaned up":                                        `log(f"removed duplicate entry`,
		"the job cannot list Secrets":                                      `verbs: ["get", "create"]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("database-sync: %s (missing %q)", what, want)
		}
	}
	if strings.Contains(s, `resources: ["secrets"]
    verbs: ["get", "list"`) {
		t.Error("database-sync must not list Secrets cluster-wide")
	}
}

// The lakehouse reaches Metabase through Trino. Three things must agree, and
// each fails silently on its own: the sync registers Trino's iceberg catalog as
// the `metabase` user; Trino makes that user read-only (the console's SQL
// Editor runs as Metabase's admin, so a writable lakehouse would be writable by
// every console user); and Trino accepts the presto-jdbc driver's X-Presto-*
// headers. Checked in values.yaml AND the rendered install.yaml, because the
// rendered file is what Argo CD applies and regenerating it is how a hand-edit
// disappears.
func TestTheLakehouseReachesMetabaseReadOnly(t *testing.T) {
	read := func(parts ...string) string {
		raw, err := os.ReadFile(filepath.Join(append([]string{"..", "..", "stack", "packages", "data"}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	sync := read("metabase", "manifests", "database-sync.yaml")
	for what, want := range map[string]string{
		"registers through the bundled presto-jdbc driver": `"engine": "presto-jdbc"`,
		"points at Trino's iceberg catalog":                `"catalog": "iceberg"`,
		"connects as the read-only Trino user":             `TRINO_USER = "metabase"`,
		"can tell whether Trino is installed":              `resourceNames: ["trino"]`,
	} {
		if !strings.Contains(sync, want) {
			t.Errorf("database-sync: %s (missing %q)", what, want)
		}
	}
	readOnly := `{"user": "metabase", "catalog": ".*", "allow": "read-only"}`
	for _, f := range [][]string{{"trino", "values.yaml"}, {"trino", "manifests", "install.yaml"}} {
		s := read(f...)
		if !strings.Contains(s, "protocol.v1.alternate-header-name=Presto") {
			t.Errorf("%s: Trino must accept presto-jdbc's X-Presto-* headers", filepath.Join(f...))
		}
		// Behind the Gateway and the oauth2-proxy every request carries
		// X-Forwarded-*; without this Trino answers 406 to the whole UI.
		if !strings.Contains(s, "http-server.process-forwarded=true") {
			t.Errorf("%s: Trino must process X-Forwarded-* headers or it rejects every proxied request (406)", filepath.Join(f...))
		}
		if !strings.Contains(strings.ReplaceAll(s, `\"`, `"`), readOnly) {
			t.Errorf("%s: the metabase user must be read-only on every catalog", filepath.Join(f...))
		}
	}
}
