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
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"
	"adhar-io/adhar/platform/utils"

	"code.gitea.io/sdk/gitea"
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
	deployCmd.Flags().StringVar(&versionFlag, "version", "", "Application version or Git revision")
	deployCmd.Flags().BoolVarP(&waitForReady, "wait", "w", false, "Wait for the application to become healthy")
	deployCmd.Flags().DurationVar(&deployTimeout, "timeout", 10*time.Minute, "Maximum time to wait when --wait is set")
	deployCmd.Flags().StringVar(&sourcePathFlag, "path", "", "Path within the repository or template to deploy")
	deployCmd.Flags().StringVar(&destinationNSFlag, "dest-namespace", "", "Destination namespace for application workloads")
	deployCmd.Flags().StringVar(&destinationSrv, "dest-server", "https://kubernetes.default.svc", "Destination cluster API server")
	deployCmd.Flags().StringVar(&projectFlag, "project", "default", "ArgoCD project to associate with the application")
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

// deployFromTemplate instantiates a service/application template from the Gitea
// `templates` repo (the single source of truth the Console uses too) through the
// CompositeApplication control-plane layer. It fetches <template>.yaml from
// Gitea, substitutes ${APP_NAME}/${APP_NAMESPACE}, then hands off to
// deployFromFile — which normalizes to a CompositeApplication XR and applies it,
// so a --template deploy takes the exact same control-plane path as --repo and
// as the Console.
func deployFromTemplate(ctx context.Context, kubeconfigPath, appName, namespace, template string) (string, string, error) {
	gc, err := newGiteaClient(ctx)
	if err != nil {
		return "", "", fmt.Errorf("connecting to the platform Gitea (is the cluster up?): %w", err)
	}

	raw, _, err := gc.GetFile(globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, "main", template+".yaml")
	if err != nil {
		avail := listGiteaTemplates(gc)
		if avail != "" {
			return "", "", fmt.Errorf("template %q not found in gitea %s/%s. Available: %s",
				template, globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, avail)
		}
		return "", "", fmt.Errorf("fetching template %q from gitea %s/%s: %w",
			template, globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, err)
	}

	// Substitute the placeholders the templates declare.
	rendered := strings.NewReplacer(
		"${APP_NAME}", appName,
		"${APP_NAMESPACE}", namespace,
	).Replace(string(raw))

	tmp, err := os.CreateTemp("", "adhar-template-*.yaml")
	if err != nil {
		return "", "", fmt.Errorf("staging template: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(rendered); err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("writing template: %w", err)
	}
	_ = tmp.Close()

	return deployFromFile(ctx, kubeconfigPath, appName, namespace, tmp.Name())
}

// newGiteaClient builds an authenticated Gitea SDK client against the platform's
// in-cluster Gitea (resolved from the active idp config), reachable from the CLI
// via the platform host.
func newGiteaClient(ctx context.Context) (*gitea.Client, error) {
	baseURL, err := utils.GiteaBaseUrl(ctx)
	if err != nil {
		return nil, err
	}
	return gitea.NewClient(baseURL,
		gitea.SetHTTPClient(utils.GetHttpClient()),
		gitea.SetBasicAuth(globals.GiteaAdminUser, globals.GiteaAdminPassword),
		gitea.SetContext(ctx),
	)
}

// listGiteaTemplates returns a comma-separated list of available template names
// (best-effort, for friendlier "not found" errors).
func listGiteaTemplates(gc *gitea.Client) string {
	entries, _, err := gc.ListContents(globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, "main", "")
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e != nil && e.Type == "file" && strings.HasSuffix(e.Name, ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name, ".yaml"))
		}
	}
	return strings.Join(names, ", ")
}

func deployFromRepo(ctx context.Context, kubeconfigPath, appName, namespace string) (string, string, error) {
	source := map[string]interface{}{"repoURL": repoFlag}
	if sourcePathFlag != "" {
		source["path"] = sourcePathFlag
	}
	if versionFlag != "" {
		source["targetRevision"] = versionFlag
	}

	// Provider-aware CompositeApplication XR — the same control-plane path the
	// Console uses. helpers.NewXR sets spec.crossplane.compositionSelector.
	appObj := helpers.NewXR("CompositeApplication", appName, namespace, "application", nil,
		map[string]interface{}{
			"parameters": map[string]interface{}{
				"project": projectFlag,
				"source":  source,
				"destination": map[string]interface{}{
					"namespace": destinationNSFlag,
					"server":    destinationSrv,
				},
			},
		})

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

	// Normalise to a CompositeApplication XR and ensure a provider-aware
	// composition is selected, so file-based deploys take the same control-plane
	// path as repo-based ones.
	appObj.Object["apiVersion"] = helpers.XRGroup + "/" + helpers.XRVersion
	appObj.Object["kind"] = "CompositeApplication"
	crossplane := mapFrom(spec, "crossplane")
	if _, ok := crossplane["compositionSelector"]; !ok {
		crossplane["compositionSelector"] = helpers.CompositionSelector("application", nil)
		spec["crossplane"] = crossplane
	}

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
		if (kind == "application" || kind == "compositeapplication") && strings.HasPrefix(apiVersion, "platform.adhar.io/") {
			return &unstructured.Unstructured{Object: doc}, nil
		}
	}

	return nil, fmt.Errorf("no Application resource found in %s", path)
}

// *** End Patch
