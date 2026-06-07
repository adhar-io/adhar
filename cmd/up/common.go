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

package up

import (
	"strconv"

	"adhar-io/adhar/platform/config"
)

// targetEnvironment resolves the environment that production operations should
// act on. If options.Environment is set, the matching environment from the
// loaded configuration is returned; otherwise the first environment in the
// configuration map is used as a fallback.
//
// This consolidates the environment-selection logic that was previously
// duplicated across the production pre-flight, cluster-creation, component
// installation, and GitOps setup steps.
func (pp *ProductionProvisioner) targetEnvironment() config.EnvironmentConfig {
	if pp.options.Environment != "" {
		return pp.config.Environments[pp.options.Environment]
	}
	// Use first environment if none specified.
	for _, env := range pp.config.Environments {
		return env
	}
	return config.EnvironmentConfig{}
}

// atoiDef parses s as a base-10 integer, returning def when s is not a valid
// integer. It is used to read optional numeric values from key/value cluster
// configuration without failing the whole provisioning run.
func atoiDef(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
