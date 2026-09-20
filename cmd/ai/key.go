/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// Giving the platform its LLM key.
//
// WHY THE KEY DOES NOT GO IN CONFIG. Adhar's rule is that Git carries pointers and
// the secrets backend carries values (ADR-0009). A key in config.yaml is a key in
// Git. So `adhar up` never takes one: it installs the AI stack unkeyed, and this
// command writes the value into OpenBao afterwards, where External Secrets picks
// it up on its next refresh. No redeploy, no restart.
//
// HOW THE KEY TRAVELS. Into this process from a terminal prompt with echo off, or
// from an environment variable for automation — then over STDIN to `bao` inside the
// OpenBao pod. Never a command-line argument (argv is world-readable in the
// container's process list), never a file, never a log line, and never returned by
// any read in this package. The same discipline as hack/seed-adhar-ai-llm.sh, which
// this replaces for CLI users.

const openbaoPod = "openbao-0"

var (
	keyProvider string
	keyModel    string
	keyEndpoint string
	keyFromEnv  bool
)

var keyCmd = &cobra.Command{
	Use:     "key",
	Aliases: []string{"keys"},
	Short:   "Manage the provider key the AI data plane uses",
	Long: `Manage the one credential that turns the AI stack from installed into working.

The key is stored in the platform's secrets backend (OpenBao) and projected to the
data plane by External Secrets. It is never written to Git, never passed on a
command line, and no command in this CLI can read it back.`,
	RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
}

var keySetCmd = &cobra.Command{
	Use:   "set",
	Short: "Give the platform its LLM provider key",
	Long: `Write a provider key into the platform's secrets backend.

The key is read from a terminal prompt with echo off, or from
$ADHAR_AI_LLM_API_KEY when --from-env is passed (for CI). It travels to OpenBao on
stdin, so it never appears in argv, in shell history, or in a log.

Providers:
  anthropic   (default) claude-* models
  openai      gpt-*, o1-* models
  openrouter  OpenAI-compatible; model ids look like openai/gpt-4o-mini
  local       in-cluster inference; needs no key at all

Other properties of the entry (the provider you are not setting, the budgets) are
left as they are, so setting a second provider does not erase the first.`,
	Example: `  adhar ai key set                                     # prompts, Anthropic
  adhar ai key set --provider openrouter --model openai/gpt-4o-mini
  ADHAR_AI_LLM_API_KEY=sk-... adhar ai key set --provider openai --from-env
  adhar ai key set --provider local                    # no key required`,
	RunE:         runKeySet,
	SilenceUsage: true,
}

var keyStatusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"get", "show"},
	Short:   "Show which provider slots are keyed, without revealing a key",
	RunE:    runKeyStatus,
}

func init() {
	keySetCmd.Flags().StringVar(&keyProvider, "provider", "anthropic", "Provider: anthropic|openai|openrouter|openai-compatible|local")
	keySetCmd.Flags().StringVar(&keyModel, "model", "", "Default model id (a sensible per-provider default is used)")
	keySetCmd.Flags().StringVar(&keyEndpoint, "endpoint", "", "Base URL, for openai-compatible or self-hosted providers")
	keySetCmd.Flags().BoolVar(&keyFromEnv, "from-env", false, "Read the key from $ADHAR_AI_LLM_API_KEY instead of prompting")
	keyCmd.AddCommand(keySetCmd, keyStatusCmd)
}

// providerProfile is how one provider maps onto the backend entry: which key slot
// the gateway reads for it, and what model id makes the gateway route there.
type providerProfile struct {
	kvProvider   string
	model        string
	endpoint     string
	setAnthropic bool
	setOpenAI    bool
	needsKey     bool
}

func profileFor(provider, model, endpoint string) (providerProfile, error) {
	switch strings.ToLower(provider) {
	case "anthropic", "claude":
		return providerProfile{
			kvProvider: "claude", model: def(model, "claude-opus-4-5-20251101"), endpoint: endpoint,
			setAnthropic: true, needsKey: true,
		}, nil
	case "openai":
		return providerProfile{
			kvProvider: "openai", model: def(model, "gpt-4o"), endpoint: endpoint,
			setOpenAI: true, needsKey: true,
		}, nil
	case "openrouter":
		// OpenRouter is OpenAI-compatible, so it uses the OpenAI backend and key
		// slot. Only ids beginning `openai/` (or gpt-/o1-…) match the HTTPRoute
		// that selects that backend, so the default model is deliberately prefixed.
		return providerProfile{
			kvProvider: "openai", model: def(model, "openai/gpt-4o-mini"),
			endpoint:  def(endpoint, "https://openrouter.ai/api/v1"),
			setOpenAI: true, needsKey: true,
		}, nil
	case "openai-compatible":
		if endpoint == "" || model == "" {
			return providerProfile{}, fmt.Errorf("openai-compatible needs both --endpoint and --model")
		}
		return providerProfile{kvProvider: "openai-compatible", model: model, endpoint: endpoint, setOpenAI: true, needsKey: true}, nil
	case "local":
		return providerProfile{kvProvider: "local", model: def(model, "local/Qwen/Qwen2.5-0.5B-Instruct")}, nil
	}
	return providerProfile{}, fmt.Errorf("unknown provider %q — expected anthropic, openai, openrouter, openai-compatible or local", provider)
}

func def(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func runKeySet(cmd *cobra.Command, _ []string) error {
	ctx := ensureContext(cmd.Context())
	prof, err := profileFor(keyProvider, keyModel, keyEndpoint)
	if err != nil {
		return err
	}

	p, err := newPlatform()
	if err != nil {
		return err
	}
	if _, err := p.clients.CoreV1().Pods(p.ns).Get(ctx, openbaoPod, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("the secrets backend pod %s/%s is not there — is security/openbao installed? %w", p.ns, openbaoPod, err)
	}

	apiKey := ""
	if prof.needsKey {
		apiKey, err = readKey()
		if err != nil {
			return err
		}
	}

	rootToken, err := p.openbaoRootToken(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("\n  writing secret/adhar-ai/llm (provider=%s, model=%s)\n", prof.kvProvider, prof.model)
	if err := p.seedLLMEntry(ctx, rootToken, apiKey, prof); err != nil {
		return err
	}

	// The entry is written. Whether it reaches a Kubernetes Secret depends on the
	// ExternalSecret, which the ai/adhar-ai package creates — and on a cluster
	// still converging (or with the AI stack disabled) that does not exist yet.
	// That is a legitimate order to do this in, not a failure: the value waits in
	// the backend and ESO projects it the moment the package syncs. So say what
	// happened rather than timing out on a Secret nothing is producing.
	if !p.externalSecretExists(ctx, llmSecret) {
		fmt.Printf("\n  %s the key is stored in the secrets backend, but ExternalSecret %s does not exist yet\n", helpers.IconPending, llmSecret)
		fmt.Println("  (the ai/adhar-ai package creates it). External Secrets will project the")
		fmt.Println("  value automatically once that package syncs — nothing more to do here.")
		fmt.Println()
		hint("Check later with:  adhar ai status")
		fmt.Println()
		return nil
	}

	// Nudge External Secrets instead of waiting out the refresh interval.
	if err := p.forceSyncExternalSecret(ctx, llmSecret); err != nil {
		fmt.Printf("  %s\n", helpers.WarningStyle.Render("could not annotate the ExternalSecret; it will refresh on its own interval: "+err.Error()))
	}

	fmt.Print("  waiting for the Secret to be projected")
	ok := false
	for i := 0; i < 20; i++ {
		time.Sleep(6 * time.Second)
		fmt.Print(".")
		cfg, err := p.readLLMConfig(ctx)
		if err == nil && (cfg.Keyed || prof.kvProvider == "local") && cfg.Model == prof.model {
			ok = true
			break
		}
	}
	fmt.Println()
	if !ok {
		return fmt.Errorf("the entry was written but Secret %s did not reflect it within two minutes — check `kubectl -n %s describe externalsecret %s`", llmSecret, p.ns, llmSecret)
	}

	fmt.Printf("\n  %s Adhar AI is keyed.\n\n", helpers.StateReady("ready"))
	hint("Try it:  adhar ai ask \"what is not converged?\"")
	if prof.endpoint != "" && prof.kvProvider != "claude" {
		fmt.Println()
		fmt.Printf("  NOTE for a non-default endpoint (%s): agentgateway's openai backend\n", prof.endpoint)
		fmt.Println("  defaults to api.openai.com. The llm-openai AgentgatewayBackend and its")
		fmt.Println("  BackendTLSPolicy must point at your endpoint, or requests leave as plain")
		fmt.Println("  HTTP — see ai/agentgateway/manifests/llm-routes.yaml.")
	}
	fmt.Println()
	return nil
}

// readKey obtains the key without it ever reaching argv or a file.
func readKey() (string, error) {
	if keyFromEnv {
		k := strings.TrimSpace(os.Getenv("ADHAR_AI_LLM_API_KEY"))
		if k == "" {
			return "", fmt.Errorf("--from-env was passed but ADHAR_AI_LLM_API_KEY is empty")
		}
		return k, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no terminal to prompt on — set ADHAR_AI_LLM_API_KEY and pass --from-env")
	}
	fmt.Print("  provider API key (not echoed): ")
	b, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", fmt.Errorf("no key entered")
	}
	return k, nil
}

// openbaoRootToken reads the backend token the bootstrap persisted.
func (p *platform) openbaoRootToken(ctx context.Context) (string, error) {
	s, err := p.clients.CoreV1().Secrets(p.ns).Get(ctx, "openbao-keys", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading the OpenBao init secret: %w", err)
	}
	var init struct {
		RootToken string `json:"root_token"`
	}
	if err := json.Unmarshal(s.Data["init.json"], &init); err != nil {
		return "", fmt.Errorf("parsing openbao-keys/init.json: %w", err)
	}
	if init.RootToken == "" {
		return "", fmt.Errorf("openbao-keys holds no root_token")
	}
	return init.RootToken, nil
}

// seedLLMEntry writes the entry with `bao kv patch` when it exists and `put` when
// it does not, so setting one provider never clears another or the budgets.
//
// The token and the key are the first two lines of stdin. Everything else is
// non-secret and travels as environment variables of the exec'd command.
func (p *platform) seedLLMEntry(ctx context.Context, rootToken, apiKey string, prof providerProfile) error {
	script := `
set -eu
IFS= read -r BAO_TOKEN
IFS= read -r K
export BAO_TOKEN
if bao kv get secret/adhar-ai/llm >/dev/null 2>&1; then OP=patch; else OP=put; fi
set -- "PROVIDER=$P" "MODEL=$M" "ENDPOINT=$E"
if [ "$SET_ANTHROPIC" = "1" ]; then set -- "$@" "API_KEY=$K" "ANTHROPIC_API_KEY=$K"; fi
if [ "$SET_OPENAI" = "1" ];    then set -- "$@" "OPENAI_API_KEY=$K"; fi
if [ "$OP" = "put" ]; then
  set -- "$@" "BUDGET_PER_USER_DAILY_TOKENS=2000000" "BUDGET_PER_OP_MAX_TOOL_CALLS=40"
  if [ "$SET_ANTHROPIC" != "1" ]; then set -- "$@" "API_KEY=" "ANTHROPIC_API_KEY="; fi
  if [ "$SET_OPENAI" != "1" ];    then set -- "$@" "OPENAI_API_KEY="; fi
fi
bao kv "$OP" secret/adhar-ai/llm "$@" >/dev/null
echo "$OP"
`
	cmd := []string{"env",
		"BAO_ADDR=http://127.0.0.1:8200",
		"P=" + prof.kvProvider,
		"M=" + prof.model,
		"E=" + prof.endpoint,
		"SET_ANTHROPIC=" + boolDigit(prof.setAnthropic),
		"SET_OPENAI=" + boolDigit(prof.setOpenAI),
		"sh", "-c", script,
	}
	stdin := strings.NewReader(rootToken + "\n" + apiKey + "\n")
	out, errOut, err := p.exec(ctx, openbaoPod, "", cmd, stdin)
	if err != nil {
		return fmt.Errorf("writing the entry in %s: %w: %s", openbaoPod, err, strings.TrimSpace(errOut))
	}
	fmt.Printf("  wrote secret/adhar-ai/llm (%s)\n", strings.TrimSpace(out))
	return nil
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// exec runs a command in a pod, streaming stdin. It is SPDY-based like kubectl's
// own exec, so it works against any apiserver this CLI can already reach.
func (p *platform) exec(ctx context.Context, pod, container string, command []string, stdin *strings.Reader) (string, string, error) {
	req := p.clients.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(p.ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(p.rest, "POST", req.URL())
	if err != nil {
		return "", "", err
	}
	var stdout, stderr strings.Builder
	streamOpts := remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}
	if stdin != nil {
		streamOpts.Stdin = stdin
	}
	err = exec.StreamWithContext(ctx, streamOpts)
	return stdout.String(), stderr.String(), err
}

// forceSyncExternalSecret annotates an ExternalSecret so ESO refreshes now.
func (p *platform) forceSyncExternalSecret(ctx context.Context, name string) error {
	gvr := schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"force-sync":"%d"}}}`, time.Now().Unix())
	_, err := p.dyn.Resource(gvr).Namespace(p.ns).Patch(ctx, name, "application/merge-patch+json", []byte(patch), metav1.PatchOptions{})
	if err == nil {
		return nil
	}
	// v1beta1 is what older clusters serve; try it before reporting failure.
	gvr.Version = "v1beta1"
	_, err2 := p.dyn.Resource(gvr).Namespace(p.ns).Patch(ctx, name, "application/merge-patch+json", []byte(patch), metav1.PatchOptions{})
	if err2 != nil {
		return err
	}
	return nil
}

func runKeyStatus(cmd *cobra.Command, _ []string) error {
	ctx := ensureContext(cmd.Context())
	p, err := newPlatform()
	if err != nil {
		return err
	}
	cfg, err := p.readLLMConfig(ctx)
	if err != nil {
		return err
	}
	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cfg)
	}
	fmt.Println()
	fmt.Println(helpers.SectionHeading("🔑", "AI provider key"))
	fmt.Println()
	t := helpers.NewTable("FIELD", "VALUE")
	t.Row("Secret", fmt.Sprintf("%s/%s %s", p.ns, llmSecret, presence(cfg.SecretPresent)))
	t.Row("Provider", orDash(cfg.Provider))
	t.Row("Default model", orDash(cfg.Model))
	t.Row("Endpoint", orDash(cfg.Endpoint))
	t.Row("Anthropic slot", slotState(cfg, "anthropic"))
	t.Row("OpenAI slot", slotState(cfg, "openai"))
	fmt.Println(t.Render())
	fmt.Println()
	if !cfg.Keyed && cfg.Provider != "local" {
		hint("Set one:  adhar ai key set --provider anthropic")
	} else {
		fmt.Println("  Key values are never read back by this CLI.")
	}
	fmt.Println()
	return nil
}

func presence(ok bool) string {
	if ok {
		return helpers.StateReady("present")
	}
	return helpers.StateDisabled("absent")
}

func slotState(cfg *llmConfig, name string) string {
	for _, k := range cfg.KeyedProviders {
		if k == name {
			return helpers.StateReady("keyed")
		}
	}
	return helpers.StateDisabled("empty")
}

// externalSecretExists reports whether the ExternalSecret that projects a backend
// entry into a Kubernetes Secret is present, in either API version a cluster may
// serve.
func (p *platform) externalSecretExists(ctx context.Context, name string) bool {
	for _, version := range []string{"v1", "v1beta1"} {
		gvr := schema.GroupVersionResource{Group: "external-secrets.io", Version: version, Resource: "externalsecrets"}
		if _, err := p.dyn.Resource(gvr).Namespace(p.ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return true
		}
	}
	return false
}
