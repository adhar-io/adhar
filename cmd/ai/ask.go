/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	askNoContext bool
	askRaw       bool
)

var askCmd = &cobra.Command{
	Use:     "ask [question]",
	Aliases: []string{"q"},
	Short:   "Ask one question and get one answer",
	Long: `Ask the platform's model a single question.

The question is sent with a compact summary of THIS cluster — the platform's
conditions, how many Applications are converged and which are not — so answers
are about the platform in front of you rather than Kubernetes in general. Pass
--no-context for a plain completion with no cluster data attached, which is what
you want when the question is not about this cluster or the cluster is unreachable.

For anything that needs looking things up — reading logs, following a failure to
its cause — use ` + "`adhar ai agent`" + ` or ` + "`adhar ai diagnose`" + `: those can call tools,
and this cannot.

The question may also arrive on stdin, so this composes with the rest of your
shell: ` + "`kubectl get events | adhar ai ask \"what is going wrong here?\"`" + `.`,
	Example: `  adhar ai ask "why would an ExternalSecret stay Degraded?"
  adhar ai ask "summarise what is not converged"
  kubectl -n adhar-system get events | adhar ai ask "explain these events"
  adhar ai ask "what is a sync wave?" --no-context`,
	RunE:         runAsk,
	SilenceUsage: true,
}

func init() {
	askCmd.Flags().BoolVar(&askNoContext, "no-context", false, "Do not attach the cluster summary to the prompt")
	askCmd.Flags().BoolVar(&askRaw, "raw", false, "Print the answer with no framing, for piping into another command")
}

func runAsk(cmd *cobra.Command, args []string) error {
	ctx := ensureContext(cmd.Context())
	question := strings.Join(args, " ")

	// Stdin is appended rather than replacing the argument: the useful shape is a
	// question ABOUT piped data ("explain these events"), and silently discarding
	// one or the other would make that impossible.
	if piped := readPipedStdin(); piped != "" {
		if question == "" {
			question = piped
		} else {
			question += "\n\nHere is the input to consider:\n\n" + piped
		}
	}
	if strings.TrimSpace(question) == "" {
		return fmt.Errorf("ask what? pass a question as an argument or on stdin")
	}

	p, err := newPlatform()
	if err != nil {
		return err
	}
	c, err := dial(ctx, p)
	if err != nil {
		return err
	}
	defer c.Close()

	msgs := []message{{Role: "system", Content: systemPrompt(ctx, p, !askNoContext, nil)}}
	msgs = append(msgs, message{Role: "user", Content: question})

	if !askRaw {
		fmt.Println()
	}
	answer, err := c.stream(ctx, msgs, os.Stdout)
	if err != nil {
		if answer == "" {
			return err
		}
		// A stream that died mid-answer has still told the user something; report
		// the failure after what arrived rather than throwing the answer away.
		fmt.Println()
		return err
	}
	if !strings.HasSuffix(answer, "\n") {
		fmt.Println()
	}
	if !askRaw {
		fmt.Printf("\n%s\n\n", helpers.SubtitleStyle.Render(fmt.Sprintf("— %s via the platform AI data plane", c.model)))
	}
	return nil
}

// readPipedStdin returns stdin when it is a pipe or file, and "" when it is a
// terminal. Reading a terminal here would hang the command waiting for input the
// user has no reason to type.
func readPipedStdin() string {
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) != 0 {
		return ""
	}
	var b strings.Builder
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lines := 0
	for sc.Scan() && lines < 4000 {
		b.WriteString(sc.Text())
		b.WriteString("\n")
		lines++
	}
	return strings.TrimSpace(b.String())
}

// systemPrompt builds the instruction the model runs under.
//
// It states what Adhar is, in Adhar's terms, because a generic Kubernetes
// assistant gives advice this platform's conventions contradict — "kubectl edit
// the Deployment" is wrong here, since ArgoCD selfHeal reverts it. It also states
// the honesty rules the tool loop depends on: never claim to have changed
// something, and say when the evidence does not support a conclusion.
func systemPrompt(ctx context.Context, p *platform, withContext bool, extra []string) string {
	var b strings.Builder
	b.WriteString(`You are Adhar AI, the assistant built into the Adhar internal developer platform.

About the platform you are running inside:
- Adhar is an open-source IDP: one cluster runs ~90 curated CNCF packages, all in the adhar-system namespace, delivered by Argo CD from Git repositories hosted in an in-cluster Gitea.
- It is GitOps-first. Argo CD selfHeal reverts direct kubectl edits to any stack package, so the correct fix for a package is a change to its manifests in Git, not a live edit. The bootstrap components (Cilium, the Gateway, Argo CD, Gitea, Crossplane) are the exception: those are applied by the platform controller.
- Sync waves order a package's own resources; each Argo CD Application has its own wave sequence.
- Secrets live in OpenBao and reach workloads as Kubernetes Secrets through External Secrets Operator. An ExternalSecret that is Degraded usually means the value has not been written to the backend yet.
- Single sign-on is Keycloak; most apps sit behind oauth2-proxy.

How to answer:
- Be concrete and brief. Name the resource, the namespace and the field.
- Ground every claim in what you were shown. If the evidence is thin, say what you would look at next instead of guessing confidently.
- Never state or imply that you have changed, applied, restarted or fixed anything. You cannot: you have read access only, and any change you propose is a file a human reviews and pushes.
- Prefer this platform's conventions over generic Kubernetes advice, and say which convention you are following.`)

	for _, e := range extra {
		b.WriteString("\n")
		b.WriteString(e)
	}

	if withContext {
		if summary := clusterSummary(ctx, p); summary != "" {
			b.WriteString("\n\nCurrent state of the cluster you are attached to:\n")
			b.WriteString(summary)
		}
	}
	return b.String()
}

// clusterSummary is the cheap, always-affordable context: what the platform
// itself says about its readiness plus the Applications that are not converged.
// It is capped deliberately — a full app list on an 80-package platform is a
// wall of noise that pushes the actual question out of the model's attention.
func clusterSummary(ctx context.Context, p *platform) string {
	ctx, cancel := withTimeout(ctx, 20*time.Second)
	defer cancel()

	tb := newToolbox(p)
	var b strings.Builder

	if out, err := toolPlatformStatus.run(ctx, tb, map[string]interface{}{}); err == nil {
		b.WriteString("AdharPlatform status:\n")
		b.WriteString(truncate(out, 1500))
		b.WriteString("\n")
	}
	if out, err := toolArgoApps.run(ctx, tb, map[string]interface{}{"unhealthy": true}); err == nil {
		b.WriteString("\nApplications that are not Synced+Healthy:\n")
		b.WriteString(truncate(out, 2500))
		b.WriteString("\n")
	}
	return b.String()
}
