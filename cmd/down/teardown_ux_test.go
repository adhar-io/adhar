package down

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"
)

// The standard logger must not reach the terminal while Bubble Tea owns it.
// Providers log progress with log.Printf — the Azure provider prints its parsed
// config, every discovered resource and every deleted VM — and those writes
// interleaved mid-frame with the spinner box, tearing the border in half and
// stacking duplicate "Elapsed time" lines.
func TestTeardownLogsAreCapturedNotPrinted(t *testing.T) {
	var terminal bytes.Buffer
	log.SetOutput(&terminal)
	log.SetFlags(0)

	sub := make(chan tea.Msg, 8)
	capture := captureTeardownLogs(sub)

	log.Printf("Deleted VM dev-master-0")

	if terminal.Len() != 0 {
		t.Errorf("log reached the terminal while the UI owned it: %q", terminal.String())
	}
	if got := capture.Contents(); !strings.Contains(got, "Deleted VM dev-master-0") {
		t.Errorf("line was not retained for --verbose/failure output: %q", got)
	}

	// and it is offered to the detail pane
	select {
	case msg := <-sub:
		out, ok := msg.(logger.ExtraOutputMsg)
		if !ok {
			t.Fatalf("detail pane got %T, want logger.ExtraOutputMsg", msg)
		}
		if !strings.Contains(string(out), "Deleted VM dev-master-0") {
			t.Errorf("detail line = %q", out)
		}
	default:
		t.Error("nothing was published to the detail pane")
	}

	capture.Restore()
	log.Printf("after restore")
	if !strings.Contains(terminal.String(), "after restore") {
		t.Error("Restore did not put the previous log writer back")
	}
}

// `sub` is unbuffered in the real program and drained by the Update loop, so a
// blocking write from the teardown goroutine could deadlock the UI it is trying
// to inform. Writing must never block, even with nobody reading.
func TestLogCaptureNeverBlocksWithNoReader(t *testing.T) {
	log.SetOutput(&bytes.Buffer{})
	capture := captureTeardownLogs(make(chan tea.Msg)) // unbuffered, no reader
	defer capture.Restore()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			log.Printf("discovered untracked resource dev-worker-%d-nic", i)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writing to the capture blocked with no reader draining sub")
	}
	// Nothing may be lost even when the pane cannot keep up.
	if n := strings.Count(capture.Contents(), "discovered untracked resource"); n != 200 {
		t.Errorf("retained %d of 200 lines", n)
	}
}

func TestLogCaptureIsSafeForConcurrentWriters(t *testing.T) {
	log.SetOutput(&bytes.Buffer{})
	capture := captureTeardownLogs(make(chan tea.Msg, 256))
	defer capture.Restore()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				log.Printf("worker %d line %d", n, j)
			}
		}(i)
	}
	wg.Wait()
	if n := strings.Count(capture.Contents(), "line"); n != 400 {
		t.Errorf("retained %d of 400 lines", n)
	}
}

// The confirmation screen's whole job is informed consent, so it must name the
// resources the target cloud actually has. It printed DigitalOcean's list
// ("droplets… firewall, VPC") for every provider, which on Azure warned about
// objects that do not exist and omitted the resource group, NICs and public IPs
// that teardown does remove.
func TestConfirmationNamesTheTargetCloudsResources(t *testing.T) {
	// Exercised against the repository's real configuration files rather than a
	// synthetic one: the value comes from ResolvedEnvironments, so a fixture that
	// omits whatever ResolveEnvironments needs silently falls back to the generic
	// wording and the test would pass while proving nothing.
	t.Run("azure names Azure resources", func(t *testing.T) {
		got := teardownResourceSummary("../../config.azure.yaml", "dev")
		for _, want := range []string{"virtual machines", "managed disks", "network security group", "resource group"} {
			if !strings.Contains(got, want) {
				t.Errorf("Azure summary is missing %q: %s", want, got)
			}
		}
		// The bug being fixed: DigitalOcean's vocabulary on every cloud.
		for _, unwanted := range []string{"droplet", "firewall", "VPC"} {
			if strings.Contains(got, unwanted) {
				t.Errorf("Azure summary still says %q: %s", unwanted, got)
			}
		}
	})

	t.Run("kind says no cloud resources", func(t *testing.T) {
		// config.yaml is the local Kind default; promising to delete load
		// balancers and a VPC there is simply untrue.
		got := teardownResourceSummary("../../config.yaml", "")
		if !strings.Contains(got, "no cloud resources") {
			t.Errorf("Kind summary = %s", got)
		}
	})

	t.Run("every cloud provider has its own wording", func(t *testing.T) {
		// A provider added without a summary would silently inherit generic
		// wording; this is the reminder to write one.
		for _, p := range []string{
			globals.CloudProviderAzure, globals.CloudProviderAWS, globals.CloudProviderGKE,
			globals.CloudProviderDO, globals.CloudProviderCivo, globals.CloudProviderKind,
		} {
			if providerResourceSummaries[p] == "" {
				t.Errorf("provider %q has no teardown resource summary", p)
			}
		}
	})

	t.Run("unreadable config falls back rather than blocking teardown", func(t *testing.T) {
		if got := teardownResourceSummary(filepath.Join(t.TempDir(), "nope.yaml"), "dev"); !strings.Contains(got, "compute instances") {
			t.Errorf("a missing config must still yield generic wording, got %q", got)
		}
		if got := teardownResourceSummary("", ""); !strings.Contains(got, "compute instances") {
			t.Errorf("empty config path = %q", got)
		}
	})
}
