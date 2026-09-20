/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package cache

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The one mistake this command must never make is provisioning something other
// than a cache — the CompositeDatabase XRD also backs Postgres, MySQL and MongoDB,
// and its engine default is postgresql. So engine handling is tested exhaustively.

func TestEngineNeverFallsThroughToADatabase(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":         engineValkey, // the XRD would default this to postgresql
		"valkey":   engineValkey,
		"VALKEY":   engineValkey,
		" valkey ": engineValkey,
		"redis":    engineRedis,
		"Redis":    engineRedis,
	} {
		got, err := normalizeEngine(in)
		if err != nil {
			t.Errorf("normalizeEngine(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeEngine(%q) = %q, want %q", in, got, want)
		}
	}

	// A durable engine must be refused here, not quietly provisioned: `adhar cache
	// create --engine postgresql` asking for a database would be a 10Gi volume
	// nobody expected.
	for _, in := range []string{"postgresql", "postgres", "mysql", "mongodb", "memcached", "valkeyy"} {
		got, err := normalizeEngine(in)
		if err == nil {
			t.Errorf("normalizeEngine(%q) = %q, want an error", in, got)
			continue
		}
		if in == "postgresql" && !strings.Contains(err.Error(), "adhar database") {
			t.Errorf("refusing %q should point at the right command, got: %v", in, err)
		}
	}
}

func TestEngineVersionIsNeverThePostgresDefault(t *testing.T) {
	t.Parallel()
	// The XRD requires engineVersion and defaults it to "16" — a Postgres major.
	// Leaving it unset stamps a nonsensical value on every cache.
	for _, engine := range []string{engineValkey, engineRedis} {
		if v := cacheEngineVersion(engine); v == "16" || v == "" {
			t.Errorf("cacheEngineVersion(%q) = %q, which is the Postgres default", engine, v)
		}
	}
}

func TestCacheListOnlyClaimsCaches(t *testing.T) {
	t.Parallel()
	// A Postgres CompositeDatabase in the same namespace must not appear as a
	// cache, and `adhar cache delete` must not be able to drop it.
	postgres := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "orders-db"},
		"spec": map[string]interface{}{
			"parameters": map[string]interface{}{"engine": "postgresql", "engineVersion": "16"},
		},
	}}
	if info, isCache := flatten(postgres); isCache {
		t.Errorf("a postgresql database was reported as a cache: %+v", info)
	}

	valkey := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "sessions"},
		"spec": map[string]interface{}{
			"parameters": map[string]interface{}{"engine": "valkey", "replicas": int64(2)},
		},
		"status": map[string]interface{}{
			"endpoint": "sessions.adhar-system.svc.cluster.local",
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
		},
	}}
	info, isCache := flatten(valkey)
	if !isCache {
		t.Fatal("a valkey request was not recognised as a cache")
	}
	if !info.Ready || info.Replicas != 2 || info.Endpoint == "" {
		t.Errorf("flatten lost detail: %+v", info)
	}
}

func TestReplicasSurviveEveryJSONNumberShape(t *testing.T) {
	t.Parallel()
	// An XR read back from the apiserver carries int64; one decoded from JSON
	// carries float64. Reading only one of them showed every cache as 0 replicas.
	// A plain `int` is deliberately not tested: an unstructured object may not hold
	// one, and apimachinery panics rather than accepting it.
	for name, value := range map[string]interface{}{
		"int64":   int64(3),
		"float64": float64(3),
	} {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "c"},
			"spec": map[string]interface{}{
				"parameters": map[string]interface{}{"engine": "valkey", "replicas": value},
			},
		}}
		info, _ := flatten(obj)
		if info.Replicas != 3 {
			t.Errorf("replicas as %s read back as %d, want 3", name, info.Replicas)
		}
	}
}

func TestCompositionProviderIsPinnedNotInherited(t *testing.T) {
	t.Parallel()
	// There is no managed-cache composition, so a cache request must not inherit
	// ADHAR_PROVIDER=gcp and fail with Crossplane's "no composition matched".
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"crossplane": map[string]interface{}{
				"compositionSelector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"provider": "gcp", "feature": "database"},
				},
			},
		},
	}}
	if err := setCompositionProvider(obj, defaultProvider); err != nil {
		t.Fatal(err)
	}
	got, _, _ := unstructured.NestedString(obj.Object, "spec", "crossplane", "compositionSelector", "matchLabels", "provider")
	if got != "local" {
		t.Errorf("provider = %q, want local", got)
	}
	// An empty override falls back to local rather than clearing the label, which
	// would match every composition.
	if err := setCompositionProvider(obj, ""); err != nil {
		t.Fatal(err)
	}
	got, _, _ = unstructured.NestedString(obj.Object, "spec", "crossplane", "compositionSelector", "matchLabels", "provider")
	if got != "local" {
		t.Errorf("empty provider = %q, want local", got)
	}
}

func TestNameMustBeADNSLabel(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"sessions", "web-cache", "c1", "a-b-c-1"} {
		if err := validateName(ok); err != nil {
			t.Errorf("validateName(%q) rejected a valid name: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Sessions", "web_cache", "-lead", "trail-", "sessions.prod", strings.Repeat("a", 64)} {
		if err := validateName(bad); err == nil {
			t.Errorf("validateName(%q) accepted an invalid name", bad)
		}
	}
}
