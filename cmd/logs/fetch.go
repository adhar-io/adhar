/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// componentTarget maps a friendly component name to the namespace + label
// selector used to find its pods.
type componentTarget struct {
	Namespace string
	Selector  string
}

// knownComponents are the core platform components with well-known selectors.
// Selectors mirror those used by `adhar get status` / `adhar health`.
var knownComponents = map[string]componentTarget{
	"cilium":     {globals.AdharSystemNamespace, "app.kubernetes.io/name=cilium-agent"},
	"gateway":    {globals.AdharSystemNamespace, "app.kubernetes.io/name=cilium-envoy"},
	"envoy":      {globals.AdharSystemNamespace, "app.kubernetes.io/name=cilium-envoy"},
	"argocd":     {globals.AdharSystemNamespace, "app.kubernetes.io/name=argocd-server"},
	"gitea":      {globals.AdharSystemNamespace, "app=gitea"},
	"crossplane": {globals.AdharSystemNamespace, "app=crossplane"},
	"nginx":      {globals.AdharSystemNamespace, "app.kubernetes.io/name=ingress-nginx"},
}

// getClientset returns a Kubernetes clientset via the shared platform helper.
func getClientset() (*kubernetes.Clientset, error) {
	return k8s.GetClientset()
}

// resolveTarget turns a component/app name into a namespace + selector. Known
// components use their canonical selector; any other name is treated as an
// arbitrary app and matched against the common `app.kubernetes.io/name` and
// `app` labels. The provided ns overrides the default namespace when non-empty.
func resolveTarget(name, ns string) componentTarget {
	key := strings.ToLower(strings.TrimSpace(name))
	if t, ok := knownComponents[key]; ok {
		if ns != "" {
			t.Namespace = ns
		}
		return t
	}

	targetNS := ns
	if targetNS == "" {
		targetNS = globals.AdharSystemNamespace
	}
	// Match either of the two conventional app labels.
	selector := fmt.Sprintf("app.kubernetes.io/name=%s", key)
	return componentTarget{Namespace: targetNS, Selector: selector}
}

// findPods returns pods matching the target's selector. If the primary
// `app.kubernetes.io/name` selector yields nothing for an arbitrary app, it
// retries with the legacy `app=` label before giving up.
func findPods(ctx context.Context, clientset *kubernetes.Clientset, t componentTarget) ([]corev1.Pod, error) {
	pods, err := clientset.CoreV1().Pods(t.Namespace).List(ctx, metav1.ListOptions{LabelSelector: t.Selector})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) > 0 {
		return pods.Items, nil
	}

	// Fallback to the legacy `app=<name>` label for arbitrary apps.
	if strings.HasPrefix(t.Selector, "app.kubernetes.io/name=") {
		name := strings.TrimPrefix(t.Selector, "app.kubernetes.io/name=")
		alt, altErr := clientset.CoreV1().Pods(t.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", name),
		})
		if altErr == nil && len(alt.Items) > 0 {
			return alt.Items, nil
		}
	}
	return pods.Items, nil
}

// streamPodLogs fetches (and optionally follows) logs for every matching pod.
// When match is non-empty, only lines containing match are printed and the
// number of matched lines is returned. The follow flag streams until the
// context is cancelled (Ctrl-C).
func streamPodLogs(ctx context.Context, clientset *kubernetes.Clientset, t componentTarget, tail int64, follow bool, match string) (int, error) {
	pods, err := findPods(ctx, clientset, t)
	if err != nil {
		return 0, fmt.Errorf("failed to list pods in namespace %q: %w", t.Namespace, err)
	}
	if len(pods) == 0 {
		fmt.Println(helpers.CreateMuted(fmt.Sprintf("No pods found in %s matching %q.", t.Namespace, t.Selector)))
		return 0, nil
	}

	matchLower := strings.ToLower(match)
	matched := 0

	for _, pod := range pods {
		opts := &corev1.PodLogOptions{Follow: follow}
		if tail > 0 {
			opts.TailLines = &tail
		}

		req := clientset.CoreV1().Pods(t.Namespace).GetLogs(pod.Name, opts)
		stream, err := req.Stream(ctx)
		if err != nil {
			fmt.Println(helpers.CreateMuted(fmt.Sprintf("  (skipping %s: %v)", pod.Name, err)))
			continue
		}

		prefix := helpers.CreateAccent(fmt.Sprintf("[%s] ", pod.Name))
		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if match != "" && !strings.Contains(strings.ToLower(line), matchLower) {
				continue
			}
			if match != "" {
				matched++
			}
			fmt.Println(prefix + line)
		}
		if scanErr := scanner.Err(); scanErr != nil && scanErr != io.EOF {
			fmt.Println(helpers.CreateMuted(fmt.Sprintf("  (read error on %s: %v)", pod.Name, scanErr)))
		}
		stream.Close()
	}

	return matched, nil
}

// signalContext returns a context cancelled on SIGINT/SIGTERM, used so log
// following can be stopped cleanly with Ctrl-C.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
