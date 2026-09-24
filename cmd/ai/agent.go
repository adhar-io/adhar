/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	agentAutonomy string
	agentSteps    int
	agentQuiet    bool
	agentMCP      bool
	agentYes      bool
)

var agentCmd = &cobra.Command{
	Use:     "agent [task]",
	Aliases: []string{"do", "investigate"},
	Short:   "Run an agentic task: the model calls read-only platform tools until it can answer",
	Long: `Give the agent a job and let it look things up.

Unlike ` + "`ask`" + `, this runs a loop: the model chooses tools, reads what comes
back, and keeps going until it can answer or hits the step budget. The tools are
reads — resources, pods, logs, events, Argo CD app health, the package catalogue
and Adhar's own documentation — executed with YOUR kubeconfig, so the agent can
never see more than you can, and Secret values are redacted even then.

It cannot change the cluster. There is no apply, patch, delete, restart or sync
tool, so no instruction can make it mutate anything. Where a fix is warranted it
uses one write tool, ` + "`propose_change`" + `, which records a file proposal for you to
read — nothing is pushed, nothing is applied. The staged-autonomy ceiling comes
from the cluster (see ` + "`adhar ai autonomy`" + `) and --autonomy can only narrow it.

Every tool call is printed as it happens, so the reasoning is auditable rather
than a black box that produces a conclusion.`,
	Example: `  adhar ai agent "keycloak is not ready — find out why"
  adhar ai agent "which apps are OutOfSync and what do they have in common?"
  adhar ai agent "is anything about to run out of disk?" --autonomy read-only
  adhar ai agent "propose a fix for the metabase OOM" --autonomy suggest`,
	RunE:         runAgent,
	SilenceUsage: true,
}

var diagnoseCmd = &cobra.Command{
	Use:     "diagnose [app-or-namespace]",
	Aliases: []string{"triage", "why"},
	Short:   "Investigate why something is unhealthy, end to end",
	Long: `Triage a failing app the way an operator would: find the unhealthy pods, read
their container states, pull the logs of what crashed, correlate the events, check
the Argo CD Application, and then say what is wrong and what would fix it.

This is ` + "`adhar ai agent`" + ` with the investigation spelled out, so the common case
is one word long. With no argument it triages whatever the platform reports as not
converged.`,
	Example: `  adhar ai diagnose keycloak
  adhar ai diagnose            # whatever is unhealthy right now
  adhar ai diagnose --namespace kube-system`,
	RunE:         runDiagnose,
	SilenceUsage: true,
}

var explainCmd = &cobra.Command{
	Use:   "explain <kind/name>",
	Short: "Explain a live resource: what it is for, and what its current state means",
	Long: `Explain one live resource in this platform's terms.

It reads the object (and, for a workload, its pods and events), then explains what
the resource does here, what its current status means, and whether anything needs
attention. Adhar's own conventions are consulted, so the answer is about this
platform rather than Kubernetes in the abstract.`,
	Example: `  adhar ai explain deployment/adhar-console
  adhar ai explain application/keycloak
  adhar ai explain certificate/adhar-cert
  adhar ai explain adharplatform/adhar`,
	Args:         cobra.ExactArgs(1),
	RunE:         runExplain,
	SilenceUsage: true,
}

func init() {
	for _, c := range []*cobra.Command{agentCmd, diagnoseCmd, explainCmd} {
		c.Flags().StringVar(&agentAutonomy, "autonomy", "", "Narrow the autonomy for this run (read-only|suggest|approve-to-apply|scoped)")
		c.Flags().IntVar(&agentSteps, "max-steps", 0, "Override the step budget (cannot exceed the platform's limit)")
		c.Flags().BoolVar(&agentQuiet, "quiet", false, "Print only the final answer, not the tool calls")
		c.Flags().BoolVar(&agentMCP, "mcp", false, "Also offer the platform's federated MCP tools, when installed")
		c.Flags().BoolVar(&agentYes, "yes", false, "Do not pause for confirmation before a write tool records a proposal")
	}
}

func runAgent(cmd *cobra.Command, args []string) error {
	task := strings.TrimSpace(strings.Join(args, " "))
	if piped := readPipedStdin(); piped != "" {
		if task == "" {
			task = piped
		} else {
			task += "\n\nInput to consider:\n\n" + piped
		}
	}
	if task == "" {
		return fmt.Errorf("what should the agent do? pass a task, e.g. adhar ai agent \"why is keycloak not ready?\"")
	}
	return runLoop(cmd, task, nil)
}

func runDiagnose(cmd *cobra.Command, args []string) error {
	target := "whatever the platform currently reports as not converged"
	if len(args) > 0 {
		target = fmt.Sprintf("the app or component %q", args[0])
	}
	task := fmt.Sprintf(`Investigate %s in namespace %s and determine the root cause.

Work like an operator: start with pod_issues to see what is unhealthy, then read
the container states and restart counts, then pod_logs (previous=true when a
container has already crashed) for the failing container, then k8s_events for the
scheduling, image, probe or volume reasons that do not appear in pod status, and
argo_apps for whether Argo CD considers the package converged. Consult
docs_search when a platform convention is relevant.

Finish with: (1) the root cause in one sentence, (2) the evidence that shows it,
(3) the fix, as a change to the package's manifests in Git if it is a stack
package. Say plainly if the evidence does not identify a cause.`, target, flagNamespace)
	return runLoop(cmd, task, []string{"The user has asked for a full investigation, so prefer calling a tool over asking them for information you could look up yourself."})
}

func runExplain(cmd *cobra.Command, args []string) error {
	ref := args[0]
	kind, name := ref, ""
	if i := strings.Index(ref, "/"); i > 0 {
		kind, name = ref[:i], ref[i+1:]
	}
	if name == "" {
		return fmt.Errorf("pass the resource as kind/name, e.g. deployment/adhar-console")
	}
	task := fmt.Sprintf(`Explain the %s named %q in namespace %s.

Read it with kube_get. If it owns pods, look at them (pod_issues, and pod_logs or
k8s_events if any are unhealthy). Then explain: what this resource does in the
Adhar platform, what its current status means, and whether it needs attention.
Keep it short, and say which Adhar convention applies if one does.`, kind, name, flagNamespace)
	return runLoop(cmd, task, nil)
}

// runLoop is the agentic loop.
//
// Shape: send the task with the tool specs, execute whatever tools come back,
// feed the results in as tool messages, repeat. It ends when the model answers
// without calling a tool, when the step budget runs out, or — at `suggest` — as
// soon as the first proposal is recorded, which is what that rung means: a human
// reads one proposal rather than a chain of them.
func runLoop(cmd *cobra.Command, task string, extraSystem []string) error {
	ctx := ensureContext(cmd.Context())
	p, err := newPlatform()
	if err != nil {
		return err
	}

	ac := p.readAgentConfig(ctx)
	level, err := effectiveAutonomy(ac.Level, agentAutonomy)
	if err != nil {
		return err
	}
	steps := ac.MaxSteps
	if agentSteps > 0 && agentSteps < steps {
		steps = agentSteps
	} else if agentSteps > steps {
		return fmt.Errorf("--max-steps %d exceeds the platform's limit of %d (adhar-ai-config limits.maxSteps)", agentSteps, ac.MaxSteps)
	}

	c, err := dial(ctx, p)
	if err != nil {
		return err
	}
	defer c.Close()

	tb := newToolbox(p)
	tools := builtinTools(level, ac)
	if agentMCP {
		mcpTools, err := loadMCPTools(ctx, p, c)
		if err != nil {
			fmt.Printf("  %s\n", helpers.WarningStyle.Render("federated MCP tools unavailable: "+err.Error()))
		} else {
			tools = append(tools, mcpTools...)
		}
	}
	byName := map[string]tool{}
	for _, t := range tools {
		byName[t.name] = t
	}

	sys := systemPrompt(ctx, p, true, append(extraSystem, autonomyInstruction(level, ac)))
	msgs := []message{{Role: "system", Content: sys}, {Role: "user", Content: task}}

	if !agentQuiet {
		fmt.Println()
		fmt.Println(helpers.SectionHeading("🛠", "Adhar AI agent"))
		fmt.Printf("\n  model %s · autonomy %s · %d tools · up to %d steps\n\n", c.model, level, len(tools), steps)
	}

	start := time.Now()
	for step := 1; step <= steps; step++ {
		resp, err := c.complete(ctx, msgs, specsFor(tools))
		if err != nil {
			return err
		}
		choice := resp.Choices[0]
		msgs = append(msgs, choice.Message)

		if len(choice.Message.ToolCalls) == 0 {
			if !agentQuiet {
				fmt.Println()
			}
			fmt.Println(strings.TrimSpace(choice.Message.Content))
			printProposals(tb)
			if !agentQuiet {
				fmt.Printf("\n%s\n", helpers.SubtitleStyle.Render(fmt.Sprintf(
					"— %d step(s), %d tool call(s), %s, %d tokens", step, tb.calls, time.Since(start).Round(time.Second), resp.Usage.TotalTokens)))
				// An answer about THIS cluster that read nothing from it is a
				// guess. Small models (the CPU-only local/* default) narrate the
				// tools they would call and then invent the result — "Root
				// cause: missing keycloak dependency" for an app whose real
				// failure was a Maven mirror. Say so, in the place the answer is
				// read, rather than let the summary line carry it quietly.
				if tb.calls == 0 && len(byName) > 0 {
					fmt.Printf("%s\n", helpers.WarningStyle.Render(
						"⚠ no tools were called: this answer was not checked against the cluster. "+
							"Treat it as a suggestion — a larger model (`adhar ai key set`, or the vllm GPU profile) is what makes the agent read before it answers."))
				}
				fmt.Println()
			}
			return nil
		}

		stopAfter := false
		for _, call := range choice.Message.ToolCalls {
			result, stop := executeToolCall(ctx, tb, byName, call, level, ac)
			stopAfter = stopAfter || stop
			msgs = append(msgs, message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: result})
		}

		if tb.calls >= ac.MaxToolCalls {
			msgs = append(msgs, message{Role: "user", Content: fmt.Sprintf(
				"You have used the platform's tool-call budget (%d). Answer now with what you have, and say what you would have looked at next.", ac.MaxToolCalls)})
		}
		if stopAfter {
			// `suggest`: one proposal ends the run. Ask for the summary that goes
			// with it rather than dropping the user at a bare diff.
			msgs = append(msgs, message{Role: "user", Content: "Stop here and summarise: the root cause, the evidence, and what the proposal you recorded changes. Do not call another tool."})
			resp, err := c.complete(ctx, msgs, nil)
			if err == nil && len(resp.Choices) > 0 {
				fmt.Println()
				fmt.Println(strings.TrimSpace(resp.Choices[0].Message.Content))
			}
			printProposals(tb)
			return nil
		}
	}

	// Out of steps. Ask for the best answer available rather than ending on the
	// last tool result, which on its own reads as a crash.
	msgs = append(msgs, message{Role: "user", Content: fmt.Sprintf(
		"You have reached the %d-step budget. Answer now with what you found, and state what remains unknown.", steps)})
	resp, err := c.complete(ctx, msgs, nil)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(strings.TrimSpace(resp.Choices[0].Message.Content))
	printProposals(tb)
	if !agentQuiet {
		fmt.Printf("\n%s\n\n", helpers.WarningStyle.Render(fmt.Sprintf("stopped at the %d-step budget — rerun with --max-steps or a narrower task", steps)))
	}
	return nil
}

// executeToolCall runs one tool call and returns the tool message content, plus
// whether the run should end after this step.
func executeToolCall(ctx context.Context, tb *toolbox, byName map[string]tool, call toolCall, level string, ac agentConfig) (string, bool) {
	name := call.Function.Name
	t, ok := byName[name]
	if !ok {
		// A model that invents a tool gets told what exists. Silently ignoring the
		// call would leave it looping on the same invention.
		return fmt.Sprintf("no such tool %q. Available: %s", name, strings.Join(toolNames(byName), ", ")), false
	}

	args := map[string]interface{}{}
	if s := strings.TrimSpace(call.Function.Arguments); s != "" && s != "null" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return fmt.Sprintf("your arguments were not valid JSON (%v); send a JSON object matching the tool's parameters", err), false
		}
	}

	if !agentQuiet {
		fmt.Printf("  %s %s %s\n", helpers.IconArrow, helpers.HighlightStyle.Render(name), helpers.SubtitleStyle.Render(compactArgs(args)))
	}

	if t.write {
		allowed, repos, paths, why := ac.writesAllowed(level)
		if !allowed {
			return "refused: " + why, false
		}
		if err := checkWriteTarget(args, repos, paths); err != nil {
			return "refused: " + err.Error(), false
		}
		if !agentYes && !confirmProposal(args) {
			return "the operator declined to record this proposal; continue investigating or answer without it", false
		}
	}

	tb.calls++
	ctx, cancel := withTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := t.run(ctx, tb, args)
	if err != nil {
		// Tool errors go back to the model as text, not up as a command failure: a
		// missing resource or a wrong kind is information the agent should act on.
		if !agentQuiet {
			fmt.Printf("    %s\n", helpers.WarningStyle.Render(truncate(err.Error(), 160)))
		}
		return "tool error: " + err.Error(), false
	}
	if len(out) > toolResultLimit {
		out = out[:toolResultLimit] + "\n…(truncated; narrow the query with a selector, a namespace or a limit)"
	}
	if !agentQuiet {
		fmt.Printf("    %s\n", helpers.SubtitleStyle.Render(fmt.Sprintf("%d bytes", len(out))))
	}
	// At `suggest` the FIRST proposal ends the run.
	return out, t.write && level == autonomySuggest
}

// checkWriteTarget enforces the platform's write policy on a proposal before it
// is recorded. The agent is told the rule rather than just refused, so its next
// attempt can comply.
func checkWriteTarget(args map[string]interface{}, repos, paths []string) error {
	repo, path := str(args, "repo"), str(args, "path")
	if repo == "" || path == "" {
		return fmt.Errorf("a proposal needs both repo and path")
	}
	if len(repos) > 0 && !contains(repos, repo) {
		return fmt.Errorf("repo %q is outside the platform write policy (allowed: %s)", repo, strings.Join(repos, ", "))
	}
	if len(paths) > 0 {
		for _, p := range paths {
			if strings.HasPrefix(path, p) {
				return nil
			}
		}
		return fmt.Errorf("path %q is outside the platform write policy (allowed prefixes: %s)", path, strings.Join(paths, ", "))
	}
	return nil
}

// confirmProposal asks before a proposal is recorded. A non-interactive shell
// declines: a run in CI must not be able to answer its own question.
func confirmProposal(args map[string]interface{}) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		fmt.Printf("    %s\n", helpers.WarningStyle.Render("declined: not an interactive terminal (pass --yes to allow proposals here)"))
		return false
	}
	fmt.Printf("\n    %s wants to record a proposal for %s/%s\n    %s\n    Record it? [y/N] ",
		helpers.IconArrow, str(args, "repo"), str(args, "path"), str(args, "title"))
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(sc.Text()))
	return ans == "y" || ans == "yes"
}

// printProposals shows what the run proposed. Proposals are printed together at
// the end, and never written to disk or pushed: the human decides where a change
// goes, and Adhar's rule is that nothing reaches the cluster except through a
// reviewed Git change.
func printProposals(tb *toolbox) {
	if len(tb.proposals) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(helpers.SectionHeading("📝", fmt.Sprintf("%d proposal(s) — nothing has been applied", len(tb.proposals))))
	for i, pr := range tb.proposals {
		fmt.Printf("\n  %d. %s\n     repo %s · path %s\n     why: %s\n\n",
			i+1, helpers.HighlightStyle.Render(pr.Title), pr.Repo, pr.Path, pr.Rationale)
		for _, line := range strings.Split(strings.TrimRight(pr.Content, "\n"), "\n") {
			fmt.Printf("       %s\n", line)
		}
	}
	fmt.Println()
	hint("To adopt one: edit the file in your adhar checkout, review the diff, and push it.")
	hint("Argo CD reconciles it after review — that is the only path into the cluster.")
	fmt.Println()
}

// autonomyInstruction tells the model the authority it is running with. The
// enforcement is in code (the tool list and the checks above); this exists so the
// model's plan matches what will actually be permitted, instead of it announcing
// a fix it is about to be refused.
func autonomyInstruction(level string, ac agentConfig) string {
	switch level {
	case autonomyReadOnly:
		return "Autonomy for this run: read-only. You have no write tool. If a change is needed, describe it precisely in your answer — do not claim to have made it."
	case autonomySuggest:
		return fmt.Sprintf("Autonomy for this run: suggest. You may record ONE proposal with propose_change (repos %s, paths %s) and the run then ends for human review. Investigate enough to be right before you propose.",
			strings.Join(ac.AllowedRepos, "/"), strings.Join(ac.AllowedPaths, ", "))
	case autonomyApprove:
		return "Autonomy for this run: approve-to-apply. You may record proposals and keep working afterwards to verify them. A human still reviews and merges every one; you have applied nothing."
	case autonomyScoped:
		return "Autonomy for this run: scoped. You may record proposals only inside the platform's narrow scoped allow-list."
	}
	return ""
}

func toolNames(m map[string]tool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// compactArgs renders tool arguments on one line for the live trace, skipping the
// big ones: a proposal's whole file content would bury the trace it belongs to.
func compactArgs(args map[string]interface{}) string {
	var parts []string
	for _, k := range sortedKeys(args) {
		v := fmt.Sprintf("%v", args[k])
		if k == "content" || len(v) > 60 {
			v = fmt.Sprintf("<%d chars>", len(v))
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

func sortedKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
