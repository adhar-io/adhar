package batch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readLocalAppSet(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "platform", "stack", "adhar-appset-local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBatchLocalProfileRunsTheChainWithoutKafkaKargoAlloyOrVolcano(t *testing.T) {
	problems, err := LocalProfileProblems(readLocalAppSet(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// The check itself, on copies of the real file with one flag flipped each way.
func TestBatchLocalProfileCheckSeesAFlippedFlag(t *testing.T) {
	raw := string(readLocalAppSet(t))
	flip := func(name, from, to string) []byte {
		i := strings.Index(raw, `- name: "`+name+`"`)
		if i < 0 {
			t.Fatalf("%s is not in the local ApplicationSet", name)
		}
		j := i + strings.Index(raw[i:], `enabled: "`+from+`"`)
		return []byte(raw[:j] + `enabled: "` + to + `"` + raw[j+len(`enabled: "`+from+`"`):])
	}
	for name, appset := range map[string][]byte{
		"alloy":       flip("alloy", "false", "true"),
		"kargo":       flip("kargo", "false", "true"),
		"adhar-batch": flip("adhar-batch", "true", "false"),
		"cnpg":        flip("cnpg", "true", "false"),
	} {
		problems, err := LocalProfileProblems(appset)
		if err != nil || len(problems) != 1 || !strings.HasPrefix(problems[0], name+" ") {
			t.Errorf("%s flipped: %v %v", name, problems, err)
		}
	}
	if _, err := LocalProfileProblems([]byte("kind: ConfigMap\n")); err == nil {
		t.Error("a file with no elements passed")
	}
}
