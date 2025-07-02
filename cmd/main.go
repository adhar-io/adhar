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

	"adhar-io/adhar/globals" // Added import for globals package

	// Import necessary packages if needed by commands
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Required for cloud provider auth plugins

	"go.uber.org/zap/zapcore" // Added for log level constants
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	version  string // Set at build time by ldflags
	setupLog = ctrl.Log.WithName("setup")
)

func main() {
	// Set the globals.Version from the build-time version variable
	globals.Version = version

	// Setup minimal basic logger that will be replaced by the proper user-friendly logger
	// This is just for early initialization - the real logger is configured in commands
	opts := zap.Options{
		Development: true, // Use development mode for console output initially
		Level:       zapcore.InfoLevel,
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Execute the root command (now directly accessible)
	if err := Execute(); err != nil {
		setupLog.Error(err, "command execution failed")
		os.Exit(1)
	}
}
