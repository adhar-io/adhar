// Package controlplane embeds the Crossplane v2 control-plane configuration
// (XRDs, Compositions, Functions, ProviderConfigs and Operations) so the
// AdharPlatform controller can apply it directly — both when it runs in-process
// during `adhar up` and when it runs in-cluster — without depending on the
// source tree being present on disk or on an external `kubectl` binary.
package controlplane

import "embed"

// ConfigurationFS contains everything under configuration/: the Crossplane
// Configuration package metadata (crossplane.yaml) plus the xrd/, compositions/,
// functions/, providers/ and operations/ directories.
//
//go:embed all:configuration
var ConfigurationFS embed.FS
