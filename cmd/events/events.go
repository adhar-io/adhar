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

// Package events is `adhar events`: read the platform's event stream.
//
// The bus answers a question nothing else on the platform could: "what
// happened here, in order". `kubectl get events` is namespaced, 1-hour-lived
// and says nothing about a node the cloud refused to create; `adhar get status`
// is a snapshot. This reads the CloudEvents the platform published.
//
// It reads THROUGH THE BRIDGE rather than with a Kafka client, for the same
// reason the publishers do: the credentials and the protocol live in one
// deployment, and the CLI needs nothing but HTTP over a port-forward.
package events

import (
	"github.com/spf13/cobra"
)

// EventsCmd is the `adhar events` command group.
var EventsCmd = &cobra.Command{
	Use:     "events",
	Aliases: []string{"event", "ev"},
	Short:   "Read the platform event stream",
	Long: `Read the CloudEvents the platform publishes to Kafka.

Every notable thing the platform does is published once, as a CloudEvent, onto
one of five topics by domain:

  adhar.platform   platform and component lifecycle
  adhar.cluster    nodes, scaling and data planes
  adhar.tenancy    organisations, teams and projects
  adhar.delivery   GitOps, packages and pipelines
  adhar.ai         agent runs

This is the record of what HAPPENED, in order, which neither ` + "`kubectl get events`" + `
(namespaced, short-lived) nor ` + "`adhar get status`" + ` (a snapshot) can answer.

Examples:
  adhar events tail                        # follow everything, newest last
  adhar events tail --domain cluster       # only node and scaling events
  adhar events tail --type io.adhar.cluster.scale.refused
  adhar events list --since 1h -o json     # the last hour, as JSON`,
}

func init() {
	EventsCmd.AddCommand(tailCmd)
	EventsCmd.AddCommand(listCmd)
	EventsCmd.AddCommand(topicsCmd)
}
