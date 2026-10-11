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

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"adhar-io/adhar/cmd/helpers"
	platformevents "adhar-io/adhar/platform/events"

	"github.com/spf13/cobra"
)

var (
	flagBridge  string
	flagDomains []string
	flagTypes   []string
	flagSubject string
	flagSince   time.Duration
	flagLimit   int
	flagOutput  string
	flagFollow  bool
)

var tailCmd = &cobra.Command{
	Use:   "tail",
	Short: "Follow the platform event stream",
	Long: `Follow the platform event stream as it happens.

Reads through the in-cluster Kafka bridge. With no --bridge it port-forwards to
the bridge Service, so it works from a laptop with nothing but a kubeconfig.

Examples:
  adhar events tail
  adhar events tail --domain cluster --domain delivery
  adhar events tail --type io.adhar.cluster.scale.refused
  adhar events tail --subject prod/argocd`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		flagFollow = true
		return run(cmd)
	},
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "Print recent platform events and exit",
	Long: `Print the platform events already on the bus and exit.

Unlike ` + "`tail`" + `, this reads the HISTORY — from the oldest event the topic still
retains, or from --since — and stops. That is the form to pipe into jq or paste
into an incident note.

Examples:
  adhar events list --since 1h
  adhar events list --domain tenancy -o json
  adhar events list --type io.adhar.delivery.package.degraded --limit 20`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		flagFollow = false
		return run(cmd)
	},
}

var topicsCmd = &cobra.Command{
	Use:          "topics",
	Short:        "Show the event topics and what each carries",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		tb := helpers.NewTable("TOPIC", "DOMAIN", "CARRIES")
		for _, t := range platformevents.Topics {
			domain := strings.TrimPrefix(t, "adhar.")
			tb.Row(t, domain, domainDescription[domain])
		}
		fmt.Println(helpers.SectionHeading(helpers.IconInfo, "Platform event topics"))
		fmt.Println(tb.Render())
		fmt.Println(helpers.CreateMuted(fmt.Sprintf(
			"%d topics · events are CloudEvents 1.0 · type is io.adhar.<domain>.<object>.<verb>",
			len(platformevents.Topics))))
		return nil
	},
}

// domainDescription explains each stream in the terms an operator would use.
var domainDescription = map[string]string{
	"platform": "platform and component lifecycle",
	"cluster":  "nodes, scaling and data planes",
	"tenancy":  "organisations, teams and projects",
	"delivery": "GitOps, packages and pipelines",
	"ai":       "agent runs",
}

func init() {
	for _, c := range []*cobra.Command{tailCmd, listCmd} {
		c.Flags().StringVar(&flagBridge, "bridge", "", "Kafka bridge base URL (default: port-forward to the in-cluster bridge)")
		c.Flags().StringSliceVar(&flagDomains, "domain", nil, "Only these domains (platform, cluster, tenancy, delivery, ai)")
		c.Flags().StringSliceVar(&flagTypes, "type", nil, "Only these exact CloudEvents types")
		c.Flags().StringVar(&flagSubject, "subject", "", "Only events whose subject contains this string")
		c.Flags().StringVarP(&flagOutput, "output", "o", "table", "Output format: table or json")
	}
	listCmd.Flags().DurationVar(&flagSince, "since", 0, "Only events newer than this (e.g. 1h, 30m); default: everything retained")
	listCmd.Flags().IntVar(&flagLimit, "limit", 200, "Stop after this many matching events")
}

// topicsToRead maps the --domain selection onto topics, defaulting to all.
func topicsToRead() ([]string, error) {
	if len(flagDomains) == 0 {
		// Derived from the types when the user asked for types but no domain:
		// reading five topics to show one type is five consumer group members
		// and a lot of decoding for nothing.
		if len(flagTypes) > 0 {
			seen := map[string]bool{}
			var out []string
			for _, t := range flagTypes {
				topic, ok := platformevents.TopicFor(t)
				if !ok {
					return nil, fmt.Errorf("%q is not a platform event type (they start with %s)", t, platformevents.TypePrefix)
				}
				if !seen[topic] {
					seen[topic] = true
					out = append(out, topic)
				}
			}
			return out, nil
		}
		return platformevents.Topics, nil
	}
	var out []string
	for _, d := range flagDomains {
		topic := "adhar." + strings.TrimPrefix(strings.ToLower(d), "adhar.")
		var known bool
		for _, t := range platformevents.Topics {
			if t == topic {
				known = true
			}
		}
		if !known {
			var names []string
			for _, t := range platformevents.Topics {
				names = append(names, strings.TrimPrefix(t, "adhar."))
			}
			sort.Strings(names)
			return nil, fmt.Errorf("unknown domain %q; known domains are %s", d, strings.Join(names, ", "))
		}
		out = append(out, topic)
	}
	return out, nil
}

// matches applies the client-side filters. Done here rather than server-side
// because Kafka has no query language: a consumer reads a partition in order
// and decides for itself.
func matches(e platformevents.Event, cutoff time.Time) bool {
	if len(flagTypes) > 0 {
		var ok bool
		for _, t := range flagTypes {
			if e.Type == t {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if flagSubject != "" && !strings.Contains(e.Subject, flagSubject) {
		return false
	}
	if !cutoff.IsZero() {
		ts, err := time.Parse(time.RFC3339Nano, e.Time)
		// An event whose time cannot be parsed is SHOWN rather than hidden: a
		// filter that silently drops malformed events hides exactly the ones
		// worth looking at.
		if err == nil && ts.Before(cutoff) {
			return false
		}
	}
	return true
}

func run(cmd *cobra.Command) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	base := flagBridge
	var cleanup func()
	if base == "" {
		var err error
		base, cleanup, err = portForwardBridge(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
	}

	topics, err := topicsToRead()
	if err != nil {
		return err
	}
	client := newBridgeClient(base)
	// The consumer is deleted on every exit path, including Ctrl-C: a consumer
	// left behind is a group member that never leaves, and the broker then
	// rebalances the group on every session timeout.
	defer client.close()
	go func() {
		<-ctx.Done()
		client.close()
	}()

	// `list` reads history, `tail` follows from now. Reading history to then
	// follow would replay up to two weeks before showing anything new.
	if err := client.open(ctx, "adhar-cli", topics, !flagFollow); err != nil {
		return err
	}

	var cutoff time.Time
	if flagSince > 0 {
		cutoff = time.Now().Add(-flagSince)
	}

	if flagOutput == "json" {
		return stream(ctx, client, cutoff, emitJSON)
	}
	printed := 0
	header := func() {
		if printed == 0 {
			fmt.Println(helpers.SectionHeading(helpers.IconInfo,
				fmt.Sprintf("Platform events · %s", strings.Join(topics, ", "))))
			// helpers.Printf already indents; prepending the gutter too would
			// put the stream two columns right of its own heading.
			helpers.Printf("%-12s  %-44s  %-26s  %s\n", "TIME", "TYPE", "SUBJECT", "SUMMARY")
		}
	}
	return stream(ctx, client, cutoff, func(e platformevents.Event) error {
		header()
		printed++
		helpers.Printf("%-12s  %-44s  %-26s  %s\n",
			shortTime(e.Time), e.Type, helpers.TruncateDisplay(e.Subject, 26), summarise(e))
		return nil
	})
}

// stream polls until the context ends or, for `list`, until the stream goes
// quiet or the limit is reached.
func stream(ctx context.Context, client *bridgeClient, cutoff time.Time, emit func(platformevents.Event) error) error {
	const pollTimeout = 2 * time.Second
	matched := 0
	// `list` must terminate. The bridge returns an empty batch when a partition
	// is exhausted, so two consecutive empty polls mean "that is all there is"
	// — one is not enough, because the first poll after subscribing routinely
	// returns nothing while the group is still being assigned.
	empties := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		batch, problems, err := client.poll(ctx, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, p := range problems {
			fmt.Println(helpers.CreateWarning(p.Error()))
		}
		if len(batch) == 0 {
			empties++
			if !flagFollow && empties >= 2 {
				if matched == 0 {
					fmt.Println(helpers.CreateMuted("No matching events on the bus."))
				}
				return nil
			}
			continue
		}
		empties = 0
		for _, e := range batch {
			if !matches(e, cutoff) {
				continue
			}
			if err := emit(e); err != nil {
				return err
			}
			matched++
			if !flagFollow && flagLimit > 0 && matched >= flagLimit {
				return nil
			}
		}
	}
}

func emitJSON(e platformevents.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// One JSON document per line: the stream is unbounded, so a single array
	// could never be closed, and `jq` reads a line stream natively.
	fmt.Println(string(b))
	return nil
}

// shortTime renders an event's timestamp as wall-clock time. The date is
// dropped on purpose: a tail is read while it happens, and a column of
// identical dates is nine characters of noise per line.
func shortTime(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("15:04:05.000")
}

// summarise turns the event's data into the one phrase worth a column.
//
// Reading `data` generically rather than per type: the taxonomy grows, and a
// switch over every known type would print nothing at all for a type added
// after this was written.
func summarise(e platformevents.Event) string {
	if len(e.Data) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(e.Data, &m); err != nil {
		return ""
	}
	// The fields an operator reads first, in the order they want them.
	for _, key := range []string{"error", "message", "reason"} {
		if v, ok := m[key]; ok {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
				return s
			}
		}
	}
	if v, ok := m["succeeded"]; ok {
		if b, isBool := v.(bool); isBool && !b {
			return "failed"
		}
	}
	var parts []string
	for _, key := range []string{"component", "node", "nodeGroup", "desiredWorkers", "ready"} {
		if v, ok := m[key]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", key, v))
		}
	}
	return strings.Join(parts, " ")
}
