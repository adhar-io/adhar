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

package autoscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Measured CPU, because requests lie here.
//
// Platform pods request little (the tenant LimitRange defaults a container to
// 20m) and use much more while they boot, so a requests-based utilization
// reads "half empty" on a fleet whose every worker sits at 100% CPU. That is
// exactly what a cloud bring-up looked like (Azure, 2026-10-17: three workers
// at 3.9–4.0 of 4 cores, requests at ~55%, autoscaler idle, every pod start
// and every Argo CD comparison crawling). The kubelet publishes each node's
// actual CPU use on its stats endpoint, reachable through the API server
// proxy with no metrics-server in the way — metrics-server is itself one of
// the applications still being brought up at that point.

// NodeUsageReader returns each node's current CPU usage in millicores. Best
// effort: nodes it could not read are absent from the map.
type NodeUsageReader func(ctx context.Context, nodeNames []string) map[string]int64

// KubeletUsageReader reads node CPU usage from the kubelet stats summary
// through the API server's node proxy (`nodes/<name>/proxy/stats/summary`).
func KubeletUsageReader(cfg *rest.Config) NodeUsageReader {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil
	}
	return func(ctx context.Context, nodeNames []string) map[string]int64 {
		out := map[string]int64{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, name := range nodeNames {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
				defer cancel()
				raw, err := cs.CoreV1().RESTClient().Get().Resource("nodes").Name(name).SubResource("proxy").Suffix("stats/summary").DoRaw(rctx)
				if err != nil {
					return
				}
				milli, err := parseSummaryCPUMilli(raw)
				if err != nil {
					return
				}
				mu.Lock()
				out[name] = milli
				mu.Unlock()
			}(name)
		}
		wg.Wait()
		return out
	}
}

// parseSummaryCPUMilli extracts node.cpu.usageNanoCores from a kubelet stats
// summary document and returns it in millicores.
func parseSummaryCPUMilli(raw []byte) (int64, error) {
	var doc struct {
		Node struct {
			CPU *struct {
				UsageNanoCores *uint64 `json:"usageNanoCores"`
			} `json:"cpu"`
		} `json:"node"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, err
	}
	if doc.Node.CPU == nil || doc.Node.CPU.UsageNanoCores == nil {
		return 0, fmt.Errorf("stats summary carries no node cpu usage")
	}
	return int64(*doc.Node.CPU.UsageNanoCores / 1_000_000), nil
}
