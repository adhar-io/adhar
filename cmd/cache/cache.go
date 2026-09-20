/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package cache provides `adhar cache` — self-service in-memory caching.
//
// A cache is requested as a namespaced Crossplane CompositeDatabase with a cache
// ENGINE (valkey by default, redis for the raw-Deployment path), which is the same
// contract the Console and `adhar database` use. On this platform that resolves to
// an operator-managed Valkey in the requester's namespace — Valkey being the
// BSD-licensed drop-in for Redis, and protocol-compatible with every redis://
// client.
//
// WHY A COMMAND OF ITS OWN, WHEN `adhar database --engine valkey` EXISTS. Because
// that is not where anyone looks. A developer who needs a cache is not thinking
// about databases, does not want to choose an engine version or a storage size,
// and needs exactly three things: a host, a port and a Secret to bind. This
// command asks for the two decisions that matter for a cache (engine and
// replicas), defaults the rest, and publishes where the connection details landed.
//
// WHY IT DEFAULTS TO THE IN-CLUSTER PROVIDER EVEN ON A CLOUD PLATFORM. There is no
// managed-cache composition yet (ElastiCache / Memorystore / Azure Cache are not
// in platform/controlplane/configuration/compositions/database). A cache request
// that inherited ADHAR_PROVIDER=gcp would match no composition and fail with
// Crossplane's "no composition matched" — accurate and useless. So the provider is
// pinned to `local` unless --provider says otherwise, and the flag is documented
// as the hook for the day a cloud composition exists.
package cache

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/spf13/cobra"
)

const (
	// xrKind and xrPlural address the CompositeDatabase XRD — a cache is a
	// database request with a cache engine, not a separate API.
	xrKind   = "CompositeDatabase"
	xrPlural = "compositedatabases"
	// feature is the composition-selector category.
	feature = "database"

	engineValkey = "valkey"
	engineRedis  = "redis"

	// defaultProvider pins cache requests to the in-cluster compositions. See the
	// package comment for why this is not ActiveProvider().
	defaultProvider = "local"
)

// cacheEngines are the engines this command owns. `adhar database` still covers
// every engine including these; this list is what makes `adhar cache list` show
// caches and not the Postgres cluster next to them.
var cacheEngines = map[string]bool{engineValkey: true, engineRedis: true}

var (
	cacheNamespace string
	cacheEngine    string
	cacheReplicas  int
	cacheProvider  string
	outputFormat   string
)

// CacheCmd is `adhar cache`.
var CacheCmd = &cobra.Command{
	Use:     "cache",
	Aliases: []string{"caches", "valkey", "redis"},
	Short:   "Self-service in-memory cache (Valkey/Redis) for your applications",
	Long: `Request and manage an in-memory cache without filing a ticket.

A cache is an ordinary Kubernetes resource in your namespace, so who may create one
is decided by RBAC you already have. The platform provisions it and publishes the
host, port and URI as a Secret; bind that Secret to a workload and the application
is done.

The default engine is Valkey — the BSD-licensed drop-in for Redis, managed here by
an operator that handles failover, scaling and metrics. Every standard Redis client
works unchanged: ` + "`valkey://`" + ` and ` + "`redis://`" + ` are interchangeable, and the published
Secret carries both a URI and the host/port separately so either style of
configuration is a one-line bind.

Each cache is metered automatically: the composition emits an exporter and a
ServiceMonitor, so a new cache appears in the Valkey/Redis Grafana dashboard with
no per-instance wiring.

Examples:
  adhar cache create sessions                      # one Valkey node
  adhar cache create sessions --replicas 2         # a primary plus two replicas
  adhar cache create legacy --engine redis         # the plain-Deployment path
  adhar cache list
  adhar cache status sessions
  adhar cache connection sessions                  # where the host and URI live
  adhar cache delete sessions

Related:
  adhar application bind <app> <cache>   mount the cache's Secret into a workload
  adhar database                         durable databases rather than caches
  adhar bucket                           object storage`,
}

func init() {
	CacheCmd.PersistentFlags().StringVarP(&cacheNamespace, "namespace", "n", "", "Namespace (default: the platform namespace)")
	CacheCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format (table, json, yaml)")

	createCmd.Flags().StringVar(&cacheEngine, "engine", engineValkey, "Cache engine: valkey (operator-managed, recommended) or redis")
	createCmd.Flags().IntVar(&cacheReplicas, "replicas", 0, "Read replicas alongside the primary (0 = single node)")
	createCmd.Flags().StringVar(&cacheProvider, "provider", defaultProvider,
		"Composition provider to satisfy the request (only `local` has a cache composition today)")

	CacheCmd.AddCommand(createCmd, listCmd, statusCmd, connectionCmd, deleteCmd)
}

// ns resolves the namespace a cache lives in.
func ns() string {
	if cacheNamespace != "" {
		return cacheNamespace
	}
	// The platform namespace: every platform package lives there (ADR-0011), and a
	// cache requested without a namespace belongs with them.
	return globals.AdharSystemNamespace
}

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Request a new cache",
	Long: `Request a new in-memory cache.

The name is the Kubernetes resource name and the Service name, so it must be a
valid DNS label: lowercase letters, digits and dashes.

Replicas are read replicas of one primary, for read-heavy workloads and for
surviving a node going away — not shards. The default is a single node, which is
the right shape for a session store or a request cache.

The cache is unauthenticated inside the cluster by default, which is deliberate for
a cache reached over the pod network: for anything that must not be readable by a
neighbouring workload, set a password and TLS on the composed Valkey and front it
with a NetworkPolicy.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := validateName(name); err != nil {
			return err
		}
		engine, err := normalizeEngine(cacheEngine)
		if err != nil {
			return err
		}
		if cacheReplicas < 0 || cacheReplicas > 5 {
			return fmt.Errorf("--replicas must be between 0 and 5 (the CompositeDatabase XRD's bounds), got %d", cacheReplicas)
		}
		if cacheReplicas > 0 && engine == engineRedis {
			// The redis composition is a single Deployment with no replication, so
			// accepting the flag would silently do nothing.
			return fmt.Errorf("--replicas is not supported by the redis engine (a single Deployment with no replication); use --engine valkey")
		}

		parameters := map[string]interface{}{
			"engine": engine,
			// engineVersion is required by the XRD and defaults to "16", which is a
			// Postgres major. It is unused by the cache compositions (the image is
			// pinned there), so send the engine's own current major rather than
			// leave a nonsensical value on the object for the next reader.
			"engineVersion": cacheEngineVersion(engine),
		}
		if cacheReplicas > 0 {
			parameters["replicas"] = cacheReplicas
		}

		obj := helpers.NewXR(xrKind, name, ns(), feature,
			map[string]string{"engine": engine},
			map[string]interface{}{"parameters": parameters})
		// Pin the provider: see the package comment. NewXR fills this from
		// ActiveProvider, which on a cloud platform selects a composition that
		// does not exist for caches.
		if err := setCompositionProvider(obj, cacheProvider); err != nil {
			return err
		}

		logger.Info(fmt.Sprintf("⚡ Requesting %s cache %s in %s (provider: %s)", engine, name, ns(), cacheProvider))

		if err := helpers.ApplyXR(ctx(cmd), xrPlural, obj); err != nil {
			return fmt.Errorf("requesting cache %s: %w", name, err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Cache %s requested in namespace %s", name, ns())))
		fmt.Printf("  connection details will appear in Secret %s-app; see: adhar cache connection %s\n", name, name)
		return nil
	},
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List caches in the namespace",
	RunE: func(cmd *cobra.Command, args []string) error {
		items, err := listCaches(ctx(cmd))
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Printf("No caches in namespace %s\n", ns())
			return nil
		}
		switch outputFormat {
		case "json":
			return helpers.PrintJSON(items)
		case "yaml":
			return helpers.PrintYAML(items)
		}
		t := helpers.NewTable("⚡ NAME", "ENGINE", "READY", "REPLICAS", "ENDPOINT", "AGE")
		for _, c := range items {
			t.Row(c.Name, c.Engine, readyLabel(c.Ready), fmt.Sprintf("%d", c.Replicas), dash(c.Endpoint), c.Age)
		}
		fmt.Println(t.Render())
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show one cache's readiness and connection details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := getCache(ctx(cmd), args[0])
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			return helpers.PrintJSON(c)
		}
		if outputFormat == "yaml" {
			return helpers.PrintYAML(c)
		}
		fmt.Printf("⚡ %s\n", c.Name)
		fmt.Printf("  namespace   %s\n", ns())
		fmt.Printf("  engine      %s\n", c.Engine)
		fmt.Printf("  ready       %s\n", readyLabel(c.Ready))
		fmt.Printf("  replicas    %d\n", c.Replicas)
		fmt.Printf("  endpoint    %s\n", dash(c.Endpoint))
		fmt.Printf("  secret      %s\n", dash(c.SecretName))
		if !c.Ready && c.Message != "" {
			// The composition's own words: a cache that is not ready is usually
			// waiting on the valkey-operator or on scheduling capacity, and
			// paraphrasing loses the detail that identifies which.
			fmt.Printf("  waiting on  %s\n", c.Message)
		}
		return nil
	},
}

var connectionCmd = &cobra.Command{
	Use:     "connection <name>",
	Aliases: []string{"conn", "credentials", "creds"},
	Short:   "Show where the cache's host, port and URI are published",
	Long: `Show the Secret holding the cache's connection details.

The values are not printed for you to copy: bind the Secret to a workload instead,
so the application reads it from the environment and no connection string ends up
in scrollback or a CI log.

  adhar application bind <app> <cache>

To read one key deliberately:
  kubectl -n <namespace> get secret <name>-app -o jsonpath='{.data.uri}' | base64 -d`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := getCache(ctx(cmd), args[0])
		if err != nil {
			return err
		}
		secret := c.SecretName
		if secret == "" {
			// The cache compositions publish `<name>-app` by convention; say so
			// rather than reporting nothing while the XR is still settling.
			secret = c.Name + "-app"
		}
		fmt.Printf("%s %s\n", helpers.IconSecurity, secret)
		fmt.Printf("  namespace  %s\n", ns())
		fmt.Printf("  keys       uri, host, port, engine\n")
		if !c.Ready {
			fmt.Printf("  note       the cache is not Ready yet, so the Secret may not exist\n")
		}
		fmt.Printf("\n  bind it:   adhar application bind <app> %s\n", c.Name)
		return nil
	},
}

var deleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a cache",
	Long: `Delete a cache.

A cache holds no durable data by design, so this is a cheap operation — but it is
immediate: anything relying on the cache starts missing, and a workload that treats
it as a store rather than a cache loses what was in it.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		// Refuse to delete a CompositeDatabase that is not a cache: the same XRD
		// backs Postgres, and `adhar cache delete` must not be a way to drop a
		// database by mistake.
		c, err := getCache(ctx(cmd), name)
		if err != nil {
			return err
		}
		client, err := helpers.DynamicClient()
		if err != nil {
			return err
		}
		if err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).
			Delete(ctx(cmd), c.Name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("deleting cache %s: %w", name, err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Cache %s deleted from namespace %s", name, ns())))
		return nil
	},
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

func ctx(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

// normalizeEngine accepts the friendly spellings and rejects anything that is not
// a cache. A typo must not fall through to the XRD's default (postgresql), which
// would quietly provision a database from `adhar cache create`.
func normalizeEngine(engine string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(engine))
	switch e {
	case "", engineValkey:
		return engineValkey, nil
	case engineRedis:
		return engineRedis, nil
	}
	return "", fmt.Errorf("unknown cache engine %q — expected valkey or redis (for a durable database use `adhar database create --engine %s`)", engine, e)
}

// cacheEngineVersion is the major the engine's composition actually runs. The XRD
// requires engineVersion and defaults it to a Postgres major, so leaving it unset
// would stamp "16" on a Valkey request.
func cacheEngineVersion(engine string) string {
	switch engine {
	case engineRedis:
		return "8"
	default:
		return "8"
	}
}

// setCompositionProvider overrides the provider label NewXR derived from the
// environment.
func setCompositionProvider(obj *unstructured.Unstructured, provider string) error {
	if provider == "" {
		provider = defaultProvider
	}
	if err := unstructured.SetNestedField(obj.Object, provider,
		"spec", "crossplane", "compositionSelector", "matchLabels", "provider"); err != nil {
		return fmt.Errorf("setting the composition provider: %w", err)
	}
	return nil
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("a cache name is required")
	}
	if len(name) > 63 {
		return fmt.Errorf("cache name %q is too long (63 characters maximum)", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return fmt.Errorf("cache name %q is not a valid DNS label: use lowercase letters, digits and dashes", name)
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("cache name %q may not start or end with a dash", name)
	}
	return nil
}

func readyLabel(ready bool) string {
	if ready {
		return helpers.StateReady("Ready")
	}
	return helpers.StatePending("Pending")
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// cacheInfo is the flattened view the commands print.
type cacheInfo struct {
	Name       string `json:"name"`
	Engine     string `json:"engine"`
	Ready      bool   `json:"ready"`
	Replicas   int    `json:"replicas"`
	Endpoint   string `json:"endpoint,omitempty"`
	SecretName string `json:"secretName,omitempty"`
	Message    string `json:"message,omitempty"`
	Age        string `json:"age"`
}

func listCaches(c context.Context) ([]cacheInfo, error) {
	client, err := helpers.DynamicClient()
	if err != nil {
		return nil, err
	}
	list, err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).List(c, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing caches in %s: %w", ns(), err)
	}
	var out []cacheInfo
	for i := range list.Items {
		info, isCache := flatten(&list.Items[i])
		if isCache {
			out = append(out, info)
		}
	}
	return out, nil
}

func getCache(c context.Context, name string) (cacheInfo, error) {
	client, err := helpers.DynamicClient()
	if err != nil {
		return cacheInfo{}, err
	}
	obj, err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).Get(c, name, metav1.GetOptions{})
	if err != nil {
		return cacheInfo{}, fmt.Errorf("reading cache %s in %s: %w", name, ns(), err)
	}
	info, isCache := flatten(obj)
	if !isCache {
		return cacheInfo{}, fmt.Errorf("%s is a %s database, not a cache — use `adhar database` for it", name, dash(info.Engine))
	}
	return info, nil
}

// flatten reads the display fields out of an unstructured XR and reports whether
// the request is a cache at all.
func flatten(obj *unstructured.Unstructured) (cacheInfo, bool) {
	params, _, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	engine, _ := params["engine"].(string)
	// int64 is what the apiserver returns; float64 is what a JSON decode produces.
	// Those are the only two an unstructured object may legally hold for a number,
	// and reading just one of them showed every cache as having 0 replicas.
	replicas := 0
	switch v := params["replicas"].(type) {
	case int64:
		replicas = int(v)
	case float64:
		replicas = int(v)
	}

	ready, message := readiness(obj)
	endpoint, _, _ := unstructured.NestedString(obj.Object, "status", "endpoint")
	secret, _, _ := unstructured.NestedString(obj.Object, "status", "connectionSecret")
	if secret == "" {
		secret, _, _ = unstructured.NestedString(obj.Object, "spec", "writeConnectionSecretToRef", "name")
	}

	return cacheInfo{
		Name:       obj.GetName(),
		Engine:     engine,
		Ready:      ready,
		Replicas:   replicas,
		Endpoint:   endpoint,
		SecretName: secret,
		Message:    message,
		Age:        duration.HumanDuration(time.Since(obj.GetCreationTimestamp().Time)),
	}, cacheEngines[strings.ToLower(engine)]
}

// readiness reads the standard Crossplane Ready condition.
func readiness(obj *unstructured.Unstructured) (bool, string) {
	conds, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false, ""
	}
	for _, c := range conds {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			msg, _ := cond["message"].(string)
			if msg == "" {
				msg, _ = cond["reason"].(string)
			}
			return cond["status"] == "True", msg
		}
	}
	return false, ""
}
