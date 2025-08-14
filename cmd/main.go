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

package main

import (
	"os"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	_ "k8s.io/client-go/plugin/pkg/client/auth" // Required for cloud provider auth plugins
)

var (
	version   string // Set at build time by ldflags
	gitCommit string // Set at build time by ldflags
	buildDate string // Set at build time by ldflags
)

func main() {
	// Set the globals from the build-time variables
	globals.Version = version
	globals.GitCommit = gitCommit
	globals.BuildDate = buildDate

	// Initialize platform logger with default configuration
	// This provides consistent logging throughout the platform
	logger.Init(logger.DefaultConfig())

	// Execute the root command
	if err := Execute(); err != nil {
		logger.Error("Command execution failed", err, map[string]interface{}{
			"command": os.Args,
		})
		os.Exit(1)
	}
}
