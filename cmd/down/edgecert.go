/*
Copyright 2026 The Adhar Authors.

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

package down

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/platform/domain"
	"adhar-io/adhar/platform/k8s"
)

// saveEdgeCertificateBeforeTeardown caches the cluster's issued edge
// certificate on this machine so the next `adhar up` for the same domain
// reuses it instead of ordering another from Let's Encrypt (five per exact
// name set per week — a fresh cluster per run burned through that,
// 2026-10-17). Best effort: a cluster that cannot be reached, or one still on
// its self-signed bootstrap certificate, is simply torn down.
func saveEdgeCertificateBeforeTeardown(ctx context.Context, clusterName string, detail func(string, ...interface{})) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	kubeconfig := filepath.Join(home, ".adhar", "clusters", clusterName, "kubeconfig")
	if _, err := os.Stat(kubeconfig); err != nil {
		return
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		detail("  ! edge certificate not cached (kubeconfig %s): %v", kubeconfig, err)
		return
	}
	cfg.Timeout = 20 * time.Second
	c, err := client.New(cfg, client.Options{Scheme: k8s.GetScheme()})
	if err != nil {
		detail("  ! edge certificate not cached: %v", err)
		return
	}
	saveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ec, err := domain.SaveEdgeCertificate(saveCtx, c)
	switch {
	case err != nil:
		detail("  ! edge certificate not cached: %v", err)
	case ec != nil:
		detail("  ● cached the %s edge certificate for %s (valid until %s) for the next cluster", ec.Issuer, ec.Host, ec.NotAfter.UTC().Format("2006-01-02"))
	}
}
