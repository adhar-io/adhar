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

package utils

import (
	"os"
	"os/exec"
	"strings"
	"sync"
)

// ContainerEngine is the OCI runtime that hosts Kind's nodes.
//
// Kind itself supports Docker, Podman and nerdctl-family engines, and
// DetectKindNodeProvider already honours that for cluster creation. Everything
// AROUND cluster creation used to shell out to `docker` unconditionally, so on
// a Podman-only machine the cluster came up and then teardown, image preloading
// and the version report all silently failed or did nothing.
type ContainerEngine struct {
	// Binary is what to exec: "docker", "podman", "nerdctl", …
	Binary string
	// Name is the display name for humans.
	Name string
	// Available reports whether the binary was found AND answered.
	Available bool
}

// supportedEngines is kind's own precedence order: Docker first, then Podman,
// then the nerdctl family. Keeping the same order means Adhar picks the same
// engine kind would, so a cluster is never created on one engine and managed
// with another.
var supportedEngines = []struct{ binary, name string }{
	{"docker", "Docker"},
	{"podman", "Podman"},
	{"nerdctl", "nerdctl"},
	{"finch", "Finch"},
	{"nerdctl.lima", "nerdctl (Lima)"},
}

var (
	engineOnce sync.Once
	engine     ContainerEngine
)

// DetectContainerEngine resolves the engine exactly as kind does:
//
//  1. KIND_EXPERIMENTAL_PROVIDER, if set, wins — even if that engine is not
//     currently responding, because overriding it is a deliberate act and a
//     silent fallback to a different engine would be worse than a clear error.
//  2. Otherwise the first supported engine that is installed and responding.
//
// The result is cached: probing runs a subprocess, and the engine cannot change
// underneath a single CLI invocation.
func DetectContainerEngine() ContainerEngine {
	engineOnce.Do(func() { engine = detectContainerEngine() })
	return engine
}

func detectContainerEngine() ContainerEngine {
	if p := strings.TrimSpace(os.Getenv("KIND_EXPERIMENTAL_PROVIDER")); p != "" {
		return ContainerEngine{
			Binary:    p,
			Name:      engineDisplayName(p),
			Available: engineResponds(p),
		}
	}

	for _, e := range supportedEngines {
		if engineResponds(e.binary) {
			return ContainerEngine{Binary: e.binary, Name: e.name, Available: true}
		}
	}

	// Nothing responded. Report Docker so messages name the common case, but
	// say plainly that it is unavailable.
	return ContainerEngine{Binary: "docker", Name: "Docker", Available: false}
}

func engineDisplayName(binary string) string {
	for _, e := range supportedEngines {
		if e.binary == binary {
			return e.name
		}
	}
	return binary
}

// engineResponds reports whether the binary exists and its daemon answers.
// `info` is the cheapest call that proves both; presence on PATH alone is not
// enough, because a Docker CLI with no running daemon is the most common
// broken state.
func engineResponds(binary string) bool {
	if _, err := exec.LookPath(binary); err != nil {
		return false
	}
	return exec.Command(binary, "info").Run() == nil
}

// EngineNames lists the engines Adhar supports, for help text and errors.
func EngineNames() []string {
	names := make([]string, 0, len(supportedEngines))
	for _, e := range supportedEngines {
		names = append(names, e.binary)
	}
	return names
}
