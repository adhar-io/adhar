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

package webhook

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// getClientset builds a typed clientset from the standard kubeconfig and returns
// a friendly error when the cluster is unreachable.
func getClientset() (*kubernetes.Clientset, error) {
	kubeconfigPath := helpers.GetKubeConfigPath()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the cluster (is it running? try `adhar up`): %w", err)
	}
	return kubernetes.NewForConfig(config)
}

// webhookRow is a flattened view of a single admission webhook entry.
type webhookRow struct {
	Config        string `json:"config"`
	Kind          string `json:"kind"` // Validating | Mutating
	Webhook       string `json:"webhook"`
	Service       string `json:"service"`
	FailurePolicy string `json:"failurePolicy"`
	Resources     string `json:"resources"`
}

// collectWebhooks lists both validating and mutating webhook configurations and
// flattens them into rows. An optional name filter matches the configuration
// name (substring, case-insensitive handled by caller).
func collectWebhooks(ctx context.Context, cs *kubernetes.Clientset) ([]webhookRow, error) {
	var rows []webhookRow

	vwcs, err := cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing validating webhook configurations: %w", err)
	}
	for _, c := range vwcs.Items {
		for _, w := range c.Webhooks {
			rows = append(rows, validatingRow(c.Name, w))
		}
	}

	mwcs, err := cs.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing mutating webhook configurations: %w", err)
	}
	for _, c := range mwcs.Items {
		for _, w := range c.Webhooks {
			rows = append(rows, mutatingRow(c.Name, w))
		}
	}

	return rows, nil
}

func validatingRow(config string, w admissionv1.ValidatingWebhook) webhookRow {
	return webhookRow{
		Config:        config,
		Kind:          "Validating",
		Webhook:       w.Name,
		Service:       clientService(w.ClientConfig),
		FailurePolicy: failurePolicy(w.FailurePolicy),
		Resources:     ruleResources(w.Rules),
	}
}

func mutatingRow(config string, w admissionv1.MutatingWebhook) webhookRow {
	return webhookRow{
		Config:        config,
		Kind:          "Mutating",
		Webhook:       w.Name,
		Service:       clientService(w.ClientConfig),
		FailurePolicy: failurePolicy(w.FailurePolicy),
		Resources:     ruleResources(w.Rules),
	}
}

func clientService(cc admissionv1.WebhookClientConfig) string {
	if cc.Service != nil {
		path := ""
		if cc.Service.Path != nil {
			path = *cc.Service.Path
		}
		return fmt.Sprintf("%s/%s%s", cc.Service.Namespace, cc.Service.Name, path)
	}
	if cc.URL != nil {
		return *cc.URL
	}
	return "-"
}

func failurePolicy(fp *admissionv1.FailurePolicyType) string {
	if fp == nil {
		return "Fail"
	}
	return string(*fp)
}

func ruleResources(rules []admissionv1.RuleWithOperations) string {
	var out []string
	for _, r := range rules {
		out = append(out, r.Resources...)
	}
	if len(out) == 0 {
		return "*"
	}
	seen := map[string]struct{}{}
	var uniq []string
	for _, r := range out {
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		uniq = append(uniq, r)
	}
	return join(uniq, ",")
}

func join(s []string, sep string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
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
