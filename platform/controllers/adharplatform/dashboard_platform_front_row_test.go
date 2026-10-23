package adharplatform

import (
	"encoding/json"
	"testing"

	"sigs.k8s.io/yaml"
)

// The platform dashboard's front row is read at a glance: every status tile
// fills its background with its threshold colour. The rest of the dashboard
// keeps coloured values so it does not turn into a wall of colour.
func TestThePlatformFrontRowTilesAreColourFilled(t *testing.T) {
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := yaml.Unmarshal([]byte(readStackFile(t, "observability/kube-prometheus/manifests/dashboard-adhar-platform.yaml")), &cm); err != nil {
		t.Fatal(err)
	}
	var dash struct {
		Panels []struct {
			Type    string `json:"type"`
			Title   string `json:"title"`
			Options struct {
				ColorMode string `json:"colorMode"`
			} `json:"options"`
		} `json:"panels"`
	}
	for _, v := range cm.Data {
		if err := json.Unmarshal([]byte(v), &dash); err != nil {
			t.Fatal(err)
		}
	}
	row, front, filled := "", 0, 0
	for _, p := range dash.Panels {
		if p.Type == "row" {
			row = p.Title
			continue
		}
		if p.Type != "stat" {
			continue
		}
		if row == "Platform at a glance" {
			front++
			if p.Options.ColorMode != "background" {
				t.Errorf("front-row tile %q uses colorMode %q, want background", p.Title, p.Options.ColorMode)
			}
		} else if p.Options.ColorMode == "background" {
			filled++
		}
	}
	if front < 8 {
		t.Fatalf("found %d stat tiles in the front row; the row was renamed or emptied", front)
	}
	if filled > 0 {
		t.Errorf("%d stat tiles outside the front row are colour-filled; only the front row should be", filled)
	}
}
