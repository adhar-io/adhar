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
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"
	"adhar-io/adhar/platform/utils"

	"sigs.k8s.io/controller-runtime/pkg/client"

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
  adhar application deploy my-app --file=my-app.yaml
  adhar application deploy my-app --template=go-web-service --param port=8080 --param withDatabase=true
  adhar application deploy my-app --repo=https://github.com/org/service --path=deploy/overlays/prod --version=main --wait`,
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
	paramFlags        []string
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
	deployCmd.Flags().StringArrayVar(&paramFlags, "param", nil, "Template parameter as key=value (repeatable); see 'adhar application templates'")
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

// deployFromTemplate instantiates one of the platform's golden paths from the
// curated `adhar/adhar-templates` collection — the same source the Console's
// Create wizard reads, so both produce the same repository from the same
// template.
//
// These are Backstage software templates, not deployable manifests: the template
// file declares parameters and a skeleton, and instantiating it means RENDERING
// that skeleton into the application's own Gitea repository. The rendered repo is
// then handed to the same CompositeApplication path a --repo deploy takes, so the
// control-plane behaviour is identical and the generated service is editable in
// git from its first commit.
func deployFromTemplate(ctx context.Context, kubeconfigPath, appName, namespace, template string) (string, string, error) {
	gc, err := newGiteaClient(ctx)
	if err != nil {
		return "", "", fmt.Errorf("connecting to the platform Gitea (is the cluster up?): %w", err)
	}
	giteaURL, err := utils.GiteaBaseUrl(ctx)
	if err != nil {
		return "", "", fmt.Errorf("resolving the platform Gitea URL: %w", err)
	}

	overrides, err := parseTemplateParams(paramFlags)
	if err != nil {
		return "", "", err
	}

	logger.Info(fmt.Sprintf("📐 Rendering template %s into %s/%s", template, globals.GiteaPlatformOrg, appName))
	scaffolded, err := scaffoldTemplate(ctx, gc, giteaURL, template, appName, namespace, overrides)
	if err != nil {
		return "", "", err
	}
	logger.Info(fmt.Sprintf("📦 Committed %d files to %s", scaffolded.Files, scaffolded.CloneURL))

	// Point the deploy at the repo just created. An explicit --path still wins:
	// the template states where its manifests are, but the caller may be
	// deploying a different overlay out of the same repo.
	repoFlag = scaffolded.CloneURL
	if sourcePathFlag == "" {
		sourcePathFlag = scaffolded.ManifestPath
	}
	if versionFlag == "" {
		versionFlag = "main"
	}
	return deployFromRepo(ctx, kubeconfigPath, appName, namespace)
}

// parseTemplateParams turns repeated --param key=value flags into a map.
func parseTemplateParams(raw []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range raw {
		key, value, found := strings.Cut(kv, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, fmt.Errorf("--param %q is not key=value", kv)
		}
		out[key] = value
	}
	return out, nil
}

// newGiteaClient builds an authenticated Gitea SDK client against the platform's
// in-cluster Gitea (resolved from the active idp config), reachable from the CLI
// via the platform host.
func newGiteaClient(ctx context.Context) (*gitea.Client, error) {
	baseURL, err := utils.GiteaBaseUrl(ctx)
	if err != nil {
		return nil, err
	}
	// The credential ACTUALLY in force, not the day-0 constant: the platform rotates
	// the Gitea admin password (security/adhar-credential-rotation), after which the
	// constant authenticates nothing and every template fetch fails with "invalid
	// username, password or token". A cluster that has not rotated yet falls back to
	// the constant, so a fresh `adhar up` is unaffected.
	user, pass := globals.GiteaAdminUser, globals.GiteaAdminPassword
	if cfg, cerr := helpers.GetKubeConfig(); cerr == nil {
		if kc, kerr := client.New(cfg, client.Options{}); kerr == nil {
			user, pass = utils.GiteaAdminCredentials(ctx, kc)
		}
	}
	return gitea.NewClient(baseURL,
		gitea.SetHTTPClient(utils.GetHttpClient()),
		gitea.SetBasicAuth(user, pass),
		gitea.SetContext(ctx),
	)
}

// listGiteaTemplates returns a comma-separated list of available template names
// (best-effort, for friendlier "not found" errors). A template is a DIRECTORY
// under the collection's templates path, holding template.yaml and a skeleton.
func listGiteaTemplates(gc *gitea.Client) string {
	entries, _, err := gc.ListContents(globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, "main", globals.GitOpsTemplatesPath)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		// `organization/` carries Backstage User/Group entities, not a template.
		if e != nil && e.Type == "dir" && e.Name != "organization" {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
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
