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

package adharplatform

import (
	"context"
	"encoding/json"
	"strings"

	"adhar-io/adhar/platform/events"
)

// componentFor maps a condition type to the component an event is ABOUT.
//
// The condition names are the platform's public vocabulary (`kubectl wait
// --for=condition=ArgoCDReady`), while an event's subject should name the thing
// rather than the assertion — `argocd`, not `ArgoCDReady`. Mapping explicitly
// rather than trimming a suffix: `Ready` is also the aggregate's whole name,
// and trimming would turn it into the empty string.
var componentFor = map[string]string{
	ConditionArgoCDReady:     "argocd",
	ConditionGatewayReady:    "gateway",
	ConditionGiteaReady:      "gitea",
	ConditionCrossplaneReady: "crossplane",
	ConditionGitOpsReady:     "gitops",
}

// publishTransitions turns the conditions that changed on this pass into events.
//
// Called with the result of syncConditions, so there is exactly one event per
// actual change — the reconcile runs every 30 seconds and republishing the
// whole condition set each time would bury the changes in repetition.
//
// The aggregate `Ready` condition becomes a PLATFORM event and the component
// conditions become COMPONENT events, because those are different questions: an
// operator watching for "is the platform up" should not have to filter five
// component events to find it.
func (r *AdharPlatformReconciler) publishTransitions(ctx context.Context, name string, transitions []Transition) {
	if r.Events == nil || len(transitions) == 0 {
		return
	}
	for _, t := range transitions {
		if t.Type == ConditionReady {
			eventType := events.TypePlatformFailed
			if t.Ready {
				eventType = events.TypePlatformReconciled
			}
			r.Events.Publish(ctx, events.Event{
				Type:            eventType,
				Subject:         name,
				DataContentType: "application/json",
				Data: jsonOrNil(map[string]any{
					"platform": name,
					"ready":    t.Ready,
					"reason":   t.Reason,
					"message":  t.Message,
				}),
				Plane: "control",
			})
			continue
		}
		component, ok := componentFor[t.Type]
		if !ok {
			// A new condition with no mapping: name it from the condition so
			// the event is still useful, rather than dropping it silently.
			component = strings.ToLower(strings.TrimSuffix(t.Type, "Ready"))
		}
		eventType := events.TypeComponentFailed
		if t.Ready {
			eventType = events.TypeComponentReady
		}
		r.Events.Publish(ctx, events.Event{
			Type:            eventType,
			Subject:         name + "/" + component,
			DataContentType: "application/json",
			Data: jsonOrNil(map[string]any{
				"platform":  name,
				"component": component,
				"condition": t.Type,
				"ready":     t.Ready,
				"reason":    t.Reason,
				"message":   t.Message,
			}),
			Plane: "control",
		})
	}
}

// jsonOrNil marshals event data, returning nil rather than an error.
//
// A payload that cannot be marshalled is a programming error at the call site,
// and the envelope is worth more than the payload: an event saying "argocd went
// ready" with no data still answers the question an operator asked.
func jsonOrNil(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
