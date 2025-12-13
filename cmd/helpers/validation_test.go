package helpers

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateKubernetesYaml(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get current working directory")
	}

	cases := map[string]struct {
		expectErr bool
		inputPath string
	}{
		"invalidPath": {expectErr: true, inputPath: fmt.Sprintf("%s/invalid/path", cwd)},
		"notAbs":      {expectErr: true, inputPath: "invalid/path"},
		"valid":       {expectErr: false, inputPath: fmt.Sprintf("%s/test-data/valid.yaml", cwd)},
		"notYaml":     {expectErr: true, inputPath: fmt.Sprintf("%s/test-data/notyaml.yaml", cwd)},
		"notk8s":      {expectErr: true, inputPath: fmt.Sprintf("%s/test-data/notk8s.yaml", cwd)},
	}

	for k := range cases {
		cErr := ValidateKubernetesYamlFile(cases[k].inputPath)
		if cases[k].expectErr && cErr == nil {
			t.Fatalf("%s expected error but did not receive error", k)
		}
		if !cases[k].expectErr && cErr != nil {
			t.Fatalf("%s did not expect error but received error", k)
		}
	}
}

func TestParsePackageStrings(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "pkg")
	filePath := filepath.Join(tmp, "single.yaml")
	requireErr := os.MkdirAll(dirPath, 0o755)
	assert.NoError(t, requireErr)
	requireErr = os.WriteFile(filePath, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\n"), 0o644)
	assert.NoError(t, requireErr)

	remoteURL := "https://github.com/example/owner//path/to/app?ref=main"

	cases := map[string]struct {
		expectErr   bool
		inputPaths  []string
		wantRemote  int
		wantFiles   int
		wantFolders int
	}{
		"allLocal": {
			inputPaths:  []string{dirPath, filePath},
			wantRemote:  0,
			wantFiles:   1,
			wantFolders: 1,
		},
		"allRemote": {
			inputPaths: []string{
				remoteURL,
				"git@github.com:owner/repo//examples",
			},
			wantRemote:  2,
			wantFiles:   0,
			wantFolders: 0,
		},
		"mixed": {
			inputPaths:  []string{remoteURL, dirPath},
			wantRemote:  1,
			wantFiles:   0,
			wantFolders: 1,
		},
		"invalidLocal": {
			inputPaths: []string{filepath.Join(tmp, "does-not-exist")},
			expectErr:  true,
		},
		"invalidRemote": {
			inputPaths: []string{"https://github.com/example/repo"},
			expectErr:  true,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			remote, files, dirs, err := ParsePackageStrings(c.inputPaths)
			if c.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Len(t, remote, c.wantRemote)
			assert.Len(t, files, c.wantFiles)
			assert.Len(t, dirs, c.wantFolders)
		})
	}
}
