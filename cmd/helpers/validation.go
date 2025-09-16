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

package helpers

import (
	"adhar-io/adhar/platform/utils"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/kustomize/kyaml/kio"
)

func ValidateKubernetesYamlFile(absPath string) error {
	if !filepath.IsAbs(absPath) {
		return fmt.Errorf("given path is not an absolute path %s", absPath)
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("failed reading file: %s, err: %w", absPath, err)
	}
	n, err := kio.FromBytes(b)
	if err != nil {
		return fmt.Errorf("failed parsing file as kubernetes manifests file: %s, err: %w", absPath, err)
	}

	for i := range n {
		obj := n[i]
		if obj.IsNilOrEmpty() {
			return fmt.Errorf("given file %s contains an invalid kubernetes manifest", absPath)
		}
		if obj.GetKind() == "" || obj.GetApiVersion() == "" {
			return fmt.Errorf("given file %s contains an invalid kubernetes manifest", absPath)
		}
	}

	return nil
}

func ParsePackageStrings(pkgStrings []string) ([]string, []string, []string, error) {
	remote, files, dirs := make([]string, 0, 2), make([]string, 0, 2), make([]string, 0, 2)
	for i := range pkgStrings {
		loc := pkgStrings[i]
		_, err := utils.NewKustomizeRemote(loc)
		if err == nil {
			remote = append(remote, loc)
			continue
		}

		absPath, err := getAbsPath(loc, true)
		if err == nil {
			dirs = append(dirs, absPath)
			continue
		}

		absPath, err = getAbsPath(loc, false)
		if err == nil {
			files = append(files, absPath)
			continue
		}

		return nil, nil, nil, err
	}

	return remote, files, dirs, nil
}

func getAbsPath(path string, isDir bool) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to validate path %s : %w", path, err)
	}
	f, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to validate path %s : %w", absPath, err)
	}

	if isDir && !f.IsDir() {
		return "", fmt.Errorf("given path is not a directory. %s", absPath)
	}

	if !isDir && !f.Mode().IsRegular() {
		return "", fmt.Errorf("given path is not a file. %s", absPath)
	}
	return absPath, nil
}

func GetAbsFilePaths(paths []string, isDir bool) ([]string, error) {
	out := make([]string, len(paths))
	for i := range paths {
		absPath, err := getAbsPath(paths[i], isDir)
		if err != nil {
			return nil, err
		}
		out[i] = absPath
	}
	return out, nil
}
