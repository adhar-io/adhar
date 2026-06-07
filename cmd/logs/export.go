package logs

import (
	"bufio"
	"fmt"
	"os"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export logs to various formats",
	Long: `Export platform logs to various formats for analysis.
	
Examples:
  adhar logs export --format=json --output=logs.json
  adhar logs export --format=csv --since=24h
  adhar logs export --format=html --component=argocd`,
	RunE: runExport,
}

var (
	exportFormat string
	exportOutput string
)

func init() {
	exportCmd.Flags().StringVarP(&exportFormat, "format", "f", "json", "Export format (json, csv, html, xml)")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output file path")
}

func runExport(cmd *cobra.Command, args []string) error {
	logger.Info("📤 Exporting logs...")

	clientset, err := getClientset()
	if err != nil {
		return clusterError(err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	// Determine the destination writer (file or stdout).
	var w *bufio.Writer
	if exportOutput != "" {
		f, ferr := os.Create(exportOutput)
		if ferr != nil {
			return fmt.Errorf("failed to create %s: %w", exportOutput, ferr)
		}
		defer f.Close()
		w = bufio.NewWriter(f)
	} else {
		w = bufio.NewWriter(os.Stdout)
	}
	defer w.Flush()

	// Resolve the set of components to export. A specific --component narrows it;
	// otherwise export all core components.
	var targets []struct {
		name string
		t    componentTarget
	}
	if component != "" {
		targets = append(targets, struct {
			name string
			t    componentTarget
		}{component, resolveTarget(component, namespace)})
	} else {
		for name := range knownComponents {
			if name == "envoy" {
				continue
			}
			targets = append(targets, struct {
				name string
				t    componentTarget
			}{name, resolveTarget(name, namespace)})
		}
	}

	exported := 0
	for _, tgt := range targets {
		pods, perr := findPods(ctx, clientset, tgt.t)
		if perr != nil {
			fmt.Println(helpers.CreateMuted(fmt.Sprintf("  (skipping %s: %v)", tgt.name, perr)))
			continue
		}
		for _, pod := range pods {
			tail := int64(lines)
			opts := &corev1.PodLogOptions{}
			if tail > 0 {
				opts.TailLines = &tail
			}
			stream, serr := clientset.CoreV1().Pods(tgt.t.Namespace).GetLogs(pod.Name, opts).Stream(ctx)
			if serr != nil {
				fmt.Println(helpers.CreateMuted(fmt.Sprintf("  (skipping %s: %v)", pod.Name, serr)))
				continue
			}
			fmt.Fprintf(w, "===== %s/%s (%s) =====\n", tgt.t.Namespace, pod.Name, tgt.name)
			scanner := bufio.NewScanner(stream)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				fmt.Fprintln(w, scanner.Text())
			}
			stream.Close()
			exported++
		}
	}

	if exportOutput != "" {
		logger.Info(fmt.Sprintf("✅ Exported logs from %d pod(s) to %s", exported, exportOutput))
	} else {
		logger.Info(fmt.Sprintf("✅ Exported logs from %d pod(s)", exported))
	}
	return nil
}
