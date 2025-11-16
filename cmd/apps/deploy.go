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

package apps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// deployCmd represents the deploy command
var deployCmd = &cobra.Command{
	Use:   "deploy [app-name]",
	Short: "Deploy an application",
	Long: `Deploy an application using declarative specifications.
	
Examples:
  adhar apps deploy my-app --file=my-app.yaml
  adhar apps deploy my-app --template=basic-git --namespace=platform-apps
  adhar apps deploy my-app --repo=https://github.com/org/service --path=deploy/overlays/prod --version=main --wait`,
	Args: cobra.ExactArgs(1),
	RunE: runDeploy,
}

var (
	// Deploy-specific flags
	templateFlag      string
	repoFlag          string
	fileFlag          string
	versionFlag       string
	waitForReady      bool
	deployTimeout     time.Duration
	sourcePathFlag    string
	destinationNSFlag string
	destinationSrv    string
	projectFlag       string
)

func init() {
	deployCmd.Flags().StringVarP(&templateFlag, "template", "t", "", "Application template to use")
	deployCmd.Flags().StringVarP(&repoFlag, "repo", "r", "", "Git repository URL")
	deployCmd.Flags().StringVarP(&fileFlag, "file", "f", "", "Application configuration file")
	deployCmd.Flags().StringVarP(&versionFlag, "version", "v", "", "Application version or Git revision")
	deployCmd.Flags().BoolVarP(&waitForReady, "wait", "w", false, "Wait for the application to become healthy")
	deployCmd.Flags().DurationVar(&deployTimeout, "timeout", 10*time.Minute, "Maximum time to wait when --wait is set")
	deployCmd.Flags().StringVar(&sourcePathFlag, "path", "", "Path within the repository or template to deploy")
	deployCmd.Flags().StringVar(&destinationNSFlag, "dest-namespace", "", "Destination namespace for application workloads")
	deployCmd.Flags().StringVar(&destinationSrv, "dest-server", "https://kubernetes.default.svc", "Destination cluster API server")
	deployCmd.Flags().StringVar(&projectFlag, "project", "platform", "ArgoCD project to associate with the application")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	appName := args[0]
	logger.Info(fmt.Sprintf("🚀 Deploying application: %s", appName))

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}

	if templateFlag == "" && repoFlag == "" && fileFlag == "" {
		return fmt.Errorf("must specify one of --template, --repo, or --file")
	}

	deploymentNamespace := namespace
	if deploymentNamespace == "" {
		deploymentNamespace = "default"
	}

	if destinationNSFlag == "" {
		destinationNSFlag = deploymentNamespace
	}

	ctx := cmd.Context()
	var appliedName, appliedNamespace string

	switch {
	case fileFlag != "":
		appliedName, appliedNamespace, err = deployFromFile(ctx, kubeconfigPath, appName, deploymentNamespace, fileFlag)
	case templateFlag != "":
		appliedName, appliedNamespace, err = deployFromTemplate(ctx, kubeconfigPath, appName, deploymentNamespace, templateFlag)
	case repoFlag != "":
		appliedName, appliedNamespace, err = deployFromRepo(ctx, kubeconfigPath, appName, deploymentNamespace)
	}
	if err != nil {
		return err
	}

	note := fmt.Sprintf("Application %s deployed to namespace %s", appliedName, appliedNamespace)
	fmt.Println(helpers.CreateSuccess(note))

	if waitForReady {
		logger.Info("⏱️  Waiting for application to become healthy...")
		status, err := waitForApplicationReady(ctx, kubeconfigPath, appliedNamespace, appliedName, deployTimeout)
		if err != nil {
			return err
		}

		fmt.Println(helpers.SuccessStyle.Render("Application is synced and healthy"))
		return RenderApplicationStatus(status, output, true)
	}

	return nil
}

func deployFromTemplate(ctx context.Context, kubeconfigPath, appName, namespace, template string) (string, string, error) {
	templatePath := filepath.Join("control-plane", "examples", "apps", fmt.Sprintf("%s.yaml", template))
	if _, err := os.Stat(templatePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("template %q not found at %s", template, templatePath)
		}
		return "", "", fmt.Errorf("read template: %w", err)
	}

	return deployFromFile(ctx, kubeconfigPath, appName, namespace, templatePath)
}

func deployFromRepo(ctx context.Context, kubeconfigPath, appName, namespace string) (string, string, error) {
	appObj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":      appName,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"parameters": map[string]interface{}{
				"project": projectFlag,
				"source": map[string]interface{}{
					"repoURL": repoFlag,
				},
				"destination": map[string]interface{}{
					"namespace": destinationNSFlag,
					"server":    destinationSrv,
				},
			},
		},
	}}

	source := mapFrom(appObj.Object, "spec", "parameters", "source")
	if sourcePathFlag != "" {
		source["path"] = sourcePathFlag
	}
	if versionFlag != "" {
		source["targetRevision"] = versionFlag
	}

	if err := applyApplication(ctx, kubeconfigPath, appObj); err != nil {
		return "", "", err
	}

	return appObj.GetName(), appObj.GetNamespace(), nil
}

func deployFromFile(ctx context.Context, kubeconfigPath, appName, namespace, filePath string) (string, string, error) {
	appObj, err := loadApplicationFromFile(filePath)
	if err != nil {
		return "", "", err
	}

	metadata := mapFrom(appObj.Object, "metadata")
	metadata["name"] = appName
	if namespace != "" {
		metadata["namespace"] = namespace
	}

	spec := mapFrom(appObj.Object, "spec")
	if len(spec) == 0 {
		spec = map[string]interface{}{}
		appObj.Object["spec"] = spec
	}

	// Allow overrides for destination namespace/server if user specified flags.
	params := mapFrom(spec, "parameters")
	if params == nil {
		params = map[string]interface{}{}
		spec["parameters"] = params
	}

	if _, ok := params["project"]; !ok {
		params["project"] = projectFlag
	}

	dest := mapFrom(params, "destination")
	if len(dest) == 0 {
		dest = map[string]interface{}{}
		params["destination"] = dest
	}
	if destinationNSFlag != "" {
		dest["namespace"] = destinationNSFlag
	}
	if destinationSrv != "" {
		dest["server"] = destinationSrv
	}

	source := mapFrom(params, "source")
	if sourcePathFlag != "" {
		source["path"] = sourcePathFlag
	}
	if versionFlag != "" {
		source["targetRevision"] = versionFlag
	}
	if repoFlag != "" && source["repoURL"] == nil {
		source["repoURL"] = repoFlag
	}
	params["source"] = source

	if err := applyApplication(ctx, kubeconfigPath, appObj); err != nil {
		return "", "", err
	}

	return appObj.GetName(), appObj.GetNamespace(), nil
}

func loadApplicationFromFile(path string) (*unstructured.Unstructured, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	for {
		var doc map[string]interface{}
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
		if len(doc) == 0 {
			continue
		}

		kind := strings.ToLower(fmt.Sprint(doc["kind"]))
		apiVersion := fmt.Sprint(doc["apiVersion"])
		if kind == "application" && strings.HasPrefix(apiVersion, "platform.adhar.io/") {
			return &unstructured.Unstructured{Object: doc}, nil
		}
	}

	return nil, fmt.Errorf("no Application resource found in %s", path)
}

// *** End Patch
