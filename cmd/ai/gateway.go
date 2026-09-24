/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"adhar-io/adhar/cmd/auth"
	"adhar-io/adhar/cmd/helpers"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// The AI data plane. gatewayService is the ClusterIP Service the
	// agentgateway controller provisions for Gateway `adhar-ai-gateway`
	// (ai/agentgateway/manifests/gateway.yaml); llmSecret is where ESO projects
	// the provider configuration from OpenBao.
	gatewayService = "adhar-ai-gateway"
	gatewayPort    = 8080
	llmSecret      = "adhar-ai-llm"
	agentConfigMap = "adhar-ai-config"
	runtimeService = "adhar-ai-runtime"
)

// platform bundles the cluster handles a subcommand needs. Built once per
// command run: every one of these commands reads the same few resources to work
// out what is installed and how to reach it.
type platform struct {
	rest *rest.Config
	// The INTERFACE, not the concrete Clientset: the safety rules in this package
	// (what the autonomy ceiling is, which key slots are filled, whether a secret
	// value can escape) are worth testing against a fake cluster, and a concrete
	// type would make that impossible.
	clients kubernetes.Interface
	dyn     dynamic.Interface
	ns      string
}

func newPlatform() (*platform, error) {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("no cluster to talk to: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &platform{rest: cfg, clients: cs, dyn: dc, ns: flagNamespace}, nil
}

// llmConfig is the non-secret half of the projected adhar-ai-llm Secret. The key
// fields are deliberately absent from this struct: no command in this package
// has a reason to read a credential, and a struct that cannot hold one cannot
// leak one into an error message or a --json dump.
type llmConfig struct {
	Provider       string
	Model          string
	Endpoint       string
	DailyTokens    string
	MaxToolCalls   string
	Keyed          bool
	SecretPresent  bool
	KeyedProviders []string
}

// readLLMConfig reports how the platform is keyed, without returning the key.
func (p *platform) readLLMConfig(ctx context.Context) (*llmConfig, error) {
	s, err := p.clients.CoreV1().Secrets(p.ns).Get(ctx, llmSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &llmConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	// The ExternalSecret's template projects the Secret with the UPPER_CASE
	// names the workloads read (PROVIDER, API_KEY, …); the camelCase names are
	// the raw data keys a hand-made Secret would carry. Accept both — reading
	// only camelCase reported "every key slot is empty" against a keyed
	// platform, and made `adhar ai key set` wait two minutes for a projection
	// that had already happened.
	get := func(upper, camel string) string {
		if v := strings.TrimSpace(string(s.Data[upper])); v != "" {
			return v
		}
		return strings.TrimSpace(string(s.Data[camel]))
	}
	cfg := &llmConfig{
		SecretPresent: true,
		Provider:      get("PROVIDER", "provider"),
		Model:         get("MODEL", "model"),
		Endpoint:      get("ENDPOINT", "endpoint"),
		DailyTokens:   get("BUDGET_PER_USER_DAILY_TOKENS", "budgetPerUserDailyTokens"),
		MaxToolCalls:  get("BUDGET_PER_OP_MAX_TOOL_CALLS", "budgetPerOpMaxToolCalls"),
	}
	// "Keyed" is per provider slot: a platform can hold an Anthropic key and no
	// OpenAI key, and then `--model gpt-4o` will 401 while `claude-*` works. Say
	// which slots are filled rather than a single yes/no that hides that.
	for _, slot := range []struct{ upper, camel, name string }{
		{"ANTHROPIC_API_KEY", "anthropicApiKey", "anthropic"},
		{"OPENAI_API_KEY", "openaiApiKey", "openai"},
	} {
		if get(slot.upper, slot.camel) != "" {
			cfg.KeyedProviders = append(cfg.KeyedProviders, slot.name)
			cfg.Keyed = true
		}
	}
	if get("API_KEY", "apiKey") != "" {
		cfg.Keyed = true
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// The OpenAI-shaped client
// ---------------------------------------------------------------------------

// gwClient speaks the OpenAI chat-completions API to the AI data plane.
type gwClient struct {
	base  string
	http  *http.Client
	token string
	model string
	stop  func()
}

func (c *gwClient) Close() {
	if c.stop != nil {
		c.stop()
	}
}

// dial resolves how to reach the AI data plane and returns a ready client.
//
// Resolution order, most explicit first:
//  1. --endpoint / $ADHAR_AI_ENDPOINT — an operator pointing at the public route
//     (https://ai.<host>) or at a gateway outside this cluster.
//  2. a port-forward to the in-cluster Service. This is the default because it
//     needs no DNS, no ingress and no certificate: the gateway is a ClusterIP by
//     design (it is not a second public edge), and every developer who can run
//     kubectl can reach it this way.
//
// The bearer token is always the platform's own: the gateway's jwtAuthentication
// policy is strict, so an unauthenticated call is refused before it reaches a
// provider. A missing session is not fatal here — the 401 handler below turns it
// into the one instruction that fixes it.
func dial(ctx context.Context, p *platform) (*gwClient, error) {
	tr := &http.Transport{}
	if flagInsecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- opt-in for the platform's self-signed dev certificate
	}
	c := &gwClient{
		http:  &http.Client{Timeout: flagTimeout, Transport: tr},
		model: flagModel,
	}

	if ep := endpointOverride(); ep != "" {
		c.base = strings.TrimSuffix(ep, "/")
	} else {
		fwd, err := forwardService(ctx, p, gatewayService, gatewayPort)
		if err != nil {
			return nil, err
		}
		c.base = fmt.Sprintf("http://127.0.0.1:%d", fwd.localPort)
		c.stop = fwd.Close
	}

	if tok, err := auth.PlatformToken(ctx); err == nil {
		c.token = tok
	}

	if c.model == "" {
		cfg, err := p.readLLMConfig(ctx)
		if err == nil && cfg.Model != "" {
			c.model = cfg.Model
		}
	}
	if c.model == "" {
		// Last resort. Any model name still routes: the gateway's fallback rule
		// sends an unmatched name to the OpenAI backend, so a wrong guess fails
		// with a provider error rather than a silent misroute.
		c.model = "claude-opus-4-5-20251101"
	}
	return c, nil
}

func endpointOverride() string {
	if flagEndpoint != "" {
		return flagEndpoint
	}
	return os.Getenv("ADHAR_AI_ENDPOINT")
}

// ---------------------------------------------------------------------------
// Wire types (OpenAI chat completions, the subset the platform routes)
// ---------------------------------------------------------------------------

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Parameters  interface{} `json:"parameters"`
	} `json:"function"`
}

type chatRequest struct {
	Model       string     `json:"model"`
	Messages    []message  `json:"messages"`
	Tools       []toolSpec `json:"tools,omitempty"`
	Stream      bool       `json:"stream,omitempty"`
	MaxTokens   int        `json:"max_tokens,omitempty"`
	Temperature *float64   `json:"temperature,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// complete sends one turn and returns the assistant message.
func (c *gwClient) complete(ctx context.Context, msgs []message, tools []toolSpec) (*chatResponse, error) {
	body, err := json.Marshal(chatRequest{Model: c.model, Messages: msgs, Tools: tools, MaxTokens: 4096})
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, "/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("the AI data plane returned something that is not a completion: %w (%s)", err, truncate(string(raw), 200))
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("the model returned no choices")
	}
	return &out, nil
}

// stream sends one turn and writes the assistant's text as it arrives, returning
// the full text. Streaming exists for the human-facing commands only: a question
// that takes twenty seconds to answer should not look like a hang.
func (c *gwClient) stream(ctx context.Context, msgs []message, w io.Writer) (string, error) {
	body, err := json.Marshal(chatRequest{Model: c.model, Messages: msgs, Stream: true, MaxTokens: 4096})
	if err != nil {
		return "", err
	}
	req, err := c.request(ctx, "/v1/chat/completions", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", c.reachError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", c.statusError(resp.StatusCode, b)
	}

	var full strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // a keep-alive or a provider-specific event; not ours to parse
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content == "" {
				continue
			}
			full.WriteString(ch.Delta.Content)
			_, _ = io.WriteString(w, ch.Delta.Content)
		}
	}
	if err := sc.Err(); err != nil {
		return full.String(), fmt.Errorf("reading the model stream: %w", err)
	}
	return full.String(), nil
}

// models lists what the data plane will route. The gateway answers /v1/models
// from its backend table when it can; when it cannot, the caller falls back to
// the route table, which is a static and honest answer.
func (c *gwClient) models(ctx context.Context) ([]string, error) {
	raw, err := c.get(ctx, "/v1/models")
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func (c *gwClient) request(ctx context.Context, path string, body []byte) (*http.Request, error) {
	method := http.MethodGet
	var rdr io.Reader
	if body != nil {
		method = http.MethodPost
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

func (c *gwClient) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	return c.do(ctx, path, body)
}

func (c *gwClient) get(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, path, nil)
}

func (c *gwClient) do(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := c.request(ctx, path, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.reachError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp.StatusCode, raw)
	}
	return raw, nil
}

// statusError turns the gateway's HTTP failures into the instruction that fixes
// them. These three are the ones a new user actually hits, and the raw body
// ("401 Unauthorized") explains none of them.
func (c *gwClient) statusError(code int, body []byte) error {
	msg := truncate(strings.TrimSpace(string(body)), 400)
	switch code {
	case http.StatusUnauthorized:
		if c.token == "" {
			return fmt.Errorf("the AI data plane requires a platform token and this shell has no session — run `adhar auth login <username>` (add --insecure locally for the self-signed certificate)")
		}
		return fmt.Errorf("the AI data plane rejected the platform token (401). It may have expired — run `adhar auth login <username>` again. If the token is good, the provider key behind model %q may be missing: check `adhar ai key status`", c.model)
	case http.StatusForbidden:
		return fmt.Errorf("the AI data plane refused this call (403). Completions need the platform-developer group and agent write tools need platform-admin — check `adhar auth whoami`")
	case http.StatusTooManyRequests:
		return fmt.Errorf("the platform token budget for this user is exhausted (429) — see `adhar ai budget`")
	}
	return fmt.Errorf("the AI data plane returned HTTP %d: %s", code, msg)
}

func (c *gwClient) reachError(err error) error {
	return fmt.Errorf("cannot reach the AI data plane at %s: %w", c.base, err)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// deploymentReady reports the ready/desired replicas of a Deployment, and
// whether it exists at all. Used by status to tell "not installed" (the default
// on this platform) apart from "installed and broken", which need different
// advice.
func (p *platform) deploymentReady(ctx context.Context, name string) (ready, want int32, found bool) {
	d, err := p.clients.AppsV1().Deployments(p.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, 0, false
	}
	want = 1
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	return d.Status.ReadyReplicas, want, true
}

// serviceExists reports whether a Service is present, which for the gateway is
// the difference between "the agentgateway controller provisioned the data
// plane" and "the Gateway has not been accepted yet".
func (p *platform) serviceExists(ctx context.Context, name string) bool {
	_, err := p.clients.CoreV1().Services(p.ns).Get(ctx, name, metav1.GetOptions{})
	return err == nil
}

// pickPod returns a Running pod matching a label selector.
func (p *platform) pickPod(ctx context.Context, selector string) (*corev1.Pod, error) {
	pods, err := p.clients.CoreV1().Pods(p.ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			return &pods.Items[i], nil
		}
	}
	return nil, fmt.Errorf("no running pod matches %s in %s", selector, p.ns)
}

// withTimeout gives a command its own deadline without inheriting the model
// timeout, which is per-call and much longer than a cluster read should take.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
