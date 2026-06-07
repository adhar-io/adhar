/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// serviceMonitorsGVR is the ServiceMonitor resource defined by the
// kube-prometheus-stack (Prometheus Operator) CRDs.
var serviceMonitorsGVR = schema.GroupVersionResource{
	Group:    "monitoring.coreos.com",
	Version:  "v1",
	Resource: "servicemonitors",
}

// prometheusRulesGVR is the PrometheusRule resource (alerting/recording rules).
var prometheusRulesGVR = schema.GroupVersionResource{
	Group:    "monitoring.coreos.com",
	Version:  "v1",
	Resource: "prometheusrules",
}

// httpTimeout parses the --timeout flag, falling back to 30s on error.
func httpTimeout() time.Duration {
	if timeout == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// getDynamicClient builds a dynamic client from the standard kubeconfig and
// returns a friendly error when the cluster is unreachable.
func getDynamicClient() (dynamic.Interface, error) {
	kubeconfigPath := helpers.GetKubeConfigPath()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the cluster (is it running? try `adhar up`): %w", err)
	}
	return dynamic.NewForConfig(config)
}

// getClientset builds a typed clientset from the standard kubeconfig.
func getClientset() (*kubernetes.Clientset, error) {
	kubeconfigPath := helpers.GetKubeConfigPath()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the cluster (is it running? try `adhar up`): %w", err)
	}
	return kubernetes.NewForConfig(config)
}

// promResponse models the envelope returned by the Prometheus HTTP API.
type promResponse struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
}

// promQuery executes an instant query against the Prometheus HTTP API and
// returns the decoded "data" payload. The base URL is the configured Prometheus
// service endpoint (see --prometheus-url).
func promQuery(ctx context.Context, base, query string) (json.RawMessage, error) {
	endpoint, err := joinURL(base, "/api/v1/query")
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("query", query)
	endpoint += "?" + q.Encode()
	return promGet(ctx, endpoint)
}

// promGet performs a GET against a fully-formed Prometheus/Alertmanager API URL.
func promGet(ctx context.Context, endpoint string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: httpTimeout()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, unreachable(endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		// Some endpoints (e.g. Alertmanager) do not use the same envelope; return raw.
		return body, nil
	}
	if pr.Status != "" && pr.Status != "success" {
		return nil, fmt.Errorf("prometheus API error: %s: %s", pr.ErrorType, pr.Error)
	}
	if len(pr.Data) > 0 {
		return pr.Data, nil
	}
	return body, nil
}

// joinURL joins a base URL with a path, tolerating trailing/leading slashes.
func joinURL(base, p string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base URL %q: %w", base, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(p, "/")
	return u.String(), nil
}

// unreachable wraps a connection error with a friendly hint.
func unreachable(endpoint string, err error) error {
	return fmt.Errorf("could not reach %s: %w\n  hint: the backing service may not be running, or use --prometheus-url / port-forward (kubectl -n monitoring port-forward svc/kube-prometheus-stack-prometheus 9090:9090)", endpoint, err)
}

// friendlyCRDError annotates a CRD listing failure with a hint when the CRD is
// not installed.
func friendlyCRDError(kind string, err error) error {
	if strings.Contains(err.Error(), "could not find the requested resource") ||
		strings.Contains(err.Error(), "the server could not find") {
		return fmt.Errorf("%s CRD not found: kube-prometheus-stack may not be installed: %w", kind, err)
	}
	return fmt.Errorf("listing %ss: %w", kind, err)
}

// trunc shortens s to n runes, appending an ellipsis when truncated.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
