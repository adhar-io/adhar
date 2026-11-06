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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
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
	envFlag           string
)

// deployEnv returns the environment a scaffolded app is published in.
//
// Defaults to the head of the promotion chain: nothing is promoted INTO dev, so
// an app that has never been promoted lives there. An unrecognised value is
// REFUSED rather than passed through, because it would reach the hostname and
// the Gateway terminates TLS only for the canonical environments — the app
// would get a URL that resolves and then serves the wrong certificate, which is
// far harder to diagnose than a flag error.
func deployEnv() string {
	if envFlag == "" {
		return globals.DefaultEnvironment()
	}
	for _, e := range globals.PlatformEnvironments() {
		if envFlag == e {
			return envFlag
		}
	}
	// Cobra has already parsed, so this is the only place left to object. The
	// caller treats an empty return as "leave the template's hostname alone".
	helpers.Println(helpers.WarningStyle.Render("!") +
		" unknown environment " + envFlag + "; the platform publishes " +
		strings.Join(globals.PlatformEnvironments(), ", ") + ". Using " + globals.DefaultEnvironment() + ".")
	return globals.DefaultEnvironment()
}

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
	deployCmd.Flags().StringVar(&projectFlag, "project", "adhar", "Platform Project the application belongs to; its environments (dev/test/prod namespaces) and promotion policy come from it. With --template it must exist, and a template that declares a `project` parameter takes it from there unless this is given. With --repo its Argo CD project must allow the target namespace: the default `adhar` admits only adhar/dev/test/prod, so for another namespace pass your team's project or `default`")
	deployCmd.Flags().StringArrayVar(&paramFlags, "param", nil, "Template parameter as key=value (repeatable); see 'adhar application templates'")
	deployCmd.Flags().StringVar(&envFlag, "env", "", "Environment the app is published in; part of its hostname (default "+globals.DefaultEnvironment()+")")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	appName := args[0]
	logger.Info(fmt.Sprintf("▣ Deploying application: %s", appName))

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}

	if templateFlag == "" && repoFlag == "" && fileFlag == "" {
		return fmt.Errorf("must specify one of --template, --repo, or --file")
	}

	deploymentNamespace := namespace
	if deploymentNamespace == "" {
		deploymentNamespace = globals.DefaultEnvironment()
	}

	if destinationNSFlag == "" {
		destinationNSFlag = deploymentNamespace
	}

	ctx := cmd.Context()
	var appliedName, appliedNamespace, registeredProject string

	// A --repo deploy names its destination namespace, so check the Argo CD
	// project admits it before anything is created. (--file deploys may set
	// their own project in the file, so they are left to Argo CD; a --template
	// deploy runs in its project's environments and checks the project itself,
	// in deployFromTemplate.)
	if templateFlag == "" && repoFlag != "" {
		if err := checkProjectAdmitsNamespace(ctx, projectFlag, destinationNSFlag, cmd.Flags().Changed("project")); err != nil {
			return err
		}
	}

	switch {
	case fileFlag != "":
		appliedName, appliedNamespace, err = deployFromFile(ctx, kubeconfigPath, appName, deploymentNamespace, fileFlag)
	case templateFlag != "":
		for _, w := range templateIgnoredFlags(cmd.Flags().Changed) {
			helpers.Println(helpers.WarningStyle.Render("!") + " " + w)
		}
		appliedName, appliedNamespace, registeredProject, err = deployFromTemplate(ctx, kubeconfigPath, appName, templateFlag, cmd.Flags().Changed("project"))
	case repoFlag != "":
		appliedName, appliedNamespace, err = deployFromRepo(ctx, kubeconfigPath, appName, deploymentNamespace)
	}
	if err != nil {
		return err
	}

	note := fmt.Sprintf("Application %s deployed to namespace %s", appliedName, appliedNamespace)
	if registeredProject != "" {
		note = fmt.Sprintf("Application %s registered in project %s: its environments and promotion chain come from the project", appliedName, registeredProject)
	}
	fmt.Println(helpers.CreateSuccess(note))

	if waitForReady {
		logger.Info("◌ Waiting for application to become healthy...")
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
// that skeleton into the application's own Gitea repository, then registering
// the paved-road Application for it — the object the Console's Create wizard
// registers — in the template's project. It returns the Application's name and
// namespace and that project.
func deployFromTemplate(ctx context.Context, kubeconfigPath, appName, template string, projectExplicit bool) (string, string, string, error) {
	// Everything that can refuse is asked before anything is created: the
	// repository is the one side effect a re-run cannot undo.
	if err := validateScaffoldName(appName); err != nil {
		return "", "", "", err
	}
	dc, err := dynamicClientFor(kubeconfigPath)
	if err != nil {
		return "", "", "", err
	}
	gc, err := newGiteaClient(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("connecting to the platform Gitea (is the cluster up?): %w", err)
	}
	giteaURL, err := utils.GiteaBaseUrl(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("resolving the platform Gitea URL: %w", err)
	}
	return scaffoldTemplateApplication(ctx, dc, gc, giteaURL, appName, template, projectExplicit)
}

// scaffoldTemplateApplication is deployFromTemplate once it holds its clients:
// the whole sequence, so that it can be run against a fake Gitea and a fake
// cluster. It reads the deploy flags (--param, --project, --path, --env).
func scaffoldTemplateApplication(ctx context.Context, dc dynamic.Interface, gc *gitea.Client, giteaURL, appName, template string, projectExplicit bool) (string, string, string, error) {
	if err := validateScaffoldName(appName); err != nil {
		return "", "", "", err
	}
	overrides, err := parseTemplateParams(paramFlags)
	if err != nil {
		return "", "", "", err
	}
	t, err := loadBackstageTemplate(gc, template)
	if err != nil {
		return "", "", "", err
	}
	project, err := scaffoldProject(t, projectFlag, projectExplicit, overrides)
	if err != nil {
		return "", "", "", err
	}
	if err := checkPavedRoadProject(ctx, dc, project); err != nil {
		return "", "", "", err
	}
	if owner, ok := templateOwnerDefault(t, overrides); ok {
		fmt.Println(helpers.CreateMuted(fmt.Sprintf("   owner %s, the template's own; --param owner=group:<team> names another.", owner)))
	}

	logger.Info(fmt.Sprintf("▸ Rendering template %s into %s/%s", template, globals.GiteaPlatformOrg, appName))
	rendered, err := renderScaffold(ctx, gc, t, giteaURL, template, appName, deployEnv(), overrides)
	if err != nil {
		return "", "", "", err
	}
	sourcePath, err := templateSourcePath(sourcePathFlag, rendered)
	if err != nil {
		return "", "", "", err
	}
	if err := checkDescriptorProject(rendered, appName, project); err != nil {
		return "", "", "", err
	}
	scaffolded, err := commitScaffold(gc, giteaURL, appName, rendered)
	if err != nil {
		return "", "", "", err
	}
	logger.Info(fmt.Sprintf("▣ Committed %d files to %s", scaffolded.Files, scaffolded.CloneURL))

	// Register the paved-road Application, the one the Console's Create wizard
	// registers. Until 2026-11-03 this handed the repo to deployFromRepo, whose
	// XR (namespace dev, selector feature=application/provider=local) selected
	// application-local-argocd: one Argo CD app syncing deploy/ on main into
	// dev, with no test/prod and no Kargo. The --namespace, --dest-* and
	// --version flags do not apply here (templateIgnoredFlags).
	app := pavedRoadApplication(appName, project, scaffolded, sourcePath)
	if err := applyWithTypeRetry(app, func(obj *unstructured.Unstructured) error {
		_, aerr := dc.Resource(applicationGVR).Namespace(obj.GetNamespace()).Apply(ctx, obj.GetName(), obj,
			metav1.ApplyOptions{FieldManager: cliFieldManager})
		return aerr
	}); err != nil {
		return "", "", "", fmt.Errorf("registering Application %s for %s (the repository is committed; nothing was rolled back): %w",
			appName, scaffolded.CloneURL, err)
	}
	return app.GetName(), app.GetNamespace(), project, nil
}

// templateIgnoredFlags says which of the flags a --template deploy was given
// do not apply to it, and why. --version used to reach the Application's
// targetRevision, but the scaffold is committed to main: any other revision
// left the Kargo Warehouse watching a branch with nothing on it, so the app
// never deployed. The Console always registers main.
func templateIgnoredFlags(changed func(string) bool) []string {
	var out []string
	for _, f := range []struct{ name, why string }{
		{"namespace", "the project's environments decide where the app runs"},
		{"dest-namespace", "the project's environments decide where the app runs"},
		{"dest-server", "the project's environments decide where the app runs"},
		{"version", "the scaffold is committed to main, the branch its Application tracks"},
	} {
		if changed(f.name) {
			out = append(out, "--"+f.name+" does not apply to --template: "+f.why)
		}
	}
	return out
}

// templateSourcePath is the directory a template's Application deploys from
// when --path names one ("" leaves it to the template, as the Console always
// does). The paved road deploys <path>/envs/<environment> in every
// environment of the project (application-multienv), so a --path with nothing
// under <path>/envs/ in the rendered skeleton is refused here, before the
// repository exists: it would register an Application that never deploys,
// reporting OverlayMissing. The documented `--path manifests` did that to every
// shipped template, all of which ship deploy/envs/<environment>.
func templateSourcePath(flag string, r *renderedScaffold) (string, error) {
	if strings.TrimSpace(flag) == "" {
		return "", nil
	}
	p := strings.TrimPrefix(path.Clean(strings.TrimSpace(flag)), "/")
	overlays := p + "/envs/"
	if p == "." || p == "" {
		p, overlays = ".", "envs/"
	}
	for _, f := range r.paths {
		if strings.HasPrefix(f, overlays) {
			return p, nil
		}
	}
	own := r.template.manifestPathFor(r.paths)
	return "", fmt.Errorf("--path %s: template %q renders nothing under %s, and its Application deploys <path>/envs/<environment> in each of the project's environments; the template ships them under %s/envs/, so leave --path out",
		flag, r.templateID, overlays, own)
}

// cliFieldManager owns what the CLI declares, as `adhar-console` owns what the
// Console declares. It is also the adhar.io/managed-by label the XR carries.
const cliFieldManager = "adhar-cli"

// applyWithTypeRetry server-side applies the Application the way the Console's
// applyApplication does (control-plane.ts): no force, so a field another
// manager set differently is reported as a conflict rather than taken over.
// A control plane whose XRD predates spec.parameters.type, or its `function`
// value, refuses the whole object ("field not declared in schema" /
// `Unsupported value: "function"`); that one refusal is retried without the
// field — the adhar.io/dashboard-type and adhar.io/workload labels still say
// what the app is.
func applyWithTypeRetry(app *unstructured.Unstructured, apply func(*unstructured.Unstructured) error) error {
	err := apply(app)
	if err == nil {
		return nil
	}
	if _, has, _ := unstructured.NestedFieldNoCopy(app.Object, "spec", "parameters", "type"); !has || !parametersTypeRe.MatchString(err.Error()) {
		return err
	}
	unstructured.RemoveNestedField(app.Object, "spec", "parameters", "type")
	return apply(app)
}

// parametersTypeRe is the Console's /parameters\.type\b/.
var parametersTypeRe = regexp.MustCompile(`parameters\.type\b`)

// pavedRoadApplication is the Application XR a template scaffold registers: the
// same object as the Console's applicationManifest (apps/console/app/server/
// control-plane.ts), labelled as the CLI's. It names no composition, so the
// XRD's default (application-multienv) composes one Argo CD Application per
// environment of the project, the Kargo Warehouse and the promotion Stages.
// It tracks main, the branch the scaffold is committed to.
func pavedRoadApplication(appName, project string, s *scaffoldedRepo, path string) *unstructured.Unstructured {
	project = pavedRoadProject(project)
	if path == "" {
		path = s.ManifestPath
	}
	labels := map[string]interface{}{
		"adhar.io/managed-by":  cliFieldManager,
		"adhar.io/project":     project,
		"adhar.io/application": appName,
		"adhar.io/scaffolded":  "true",
	}
	dashboard := s.TemplateLabels["adhar.io/dashboard-type"]
	switch dashboard {
	case "backend-jvm", "backend-generic", "frontend", "fullstack":
		labels["adhar.io/dashboard-type"] = dashboard
	default:
		dashboard = ""
	}
	params := map[string]interface{}{
		"project":     project,
		"displayName": appName,
		"source": map[string]interface{}{
			"repoURL":        publicCloneURL(s.CloneURL),
			"path":           path,
			"targetRevision": "main",
		},
	}
	// The Console's applicationTypeOf: a function template deploys a Function
	// XR per environment, a browser app is `web`, anything else keeps the
	// XRD's default (`service`).
	if isFunctionTemplate(s) {
		labels["adhar.io/workload"] = "function"
		params["type"] = "function"
	} else if dashboard == "frontend" || dashboard == "fullstack" {
		params["type"] = "web"
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": helpers.XRGroup + "/" + helpers.XRVersion,
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":      appName,
			"namespace": globals.AdharSystemNamespace,
			"labels":    labels,
		},
		"spec": map[string]interface{}{"parameters": params},
	}}
}

// functionTemplateRe is the Console's isFunctionTemplate name rule
// (apps/console/app/server/templates.ts).
var functionTemplateRe = regexp.MustCompile(`^function(-|$)`)

// isFunctionTemplate says whether a template scaffolds a Function. The Console
// decides from the template's name (`function-*`) or a `function` tag
// (scaffoldWorkload → templateFamilyOf); the template's own
// adhar.io/workload: function label says the same thing and is honoured too.
func isFunctionTemplate(s *scaffoldedRepo) bool {
	if s.TemplateLabels["adhar.io/workload"] == "function" ||
		functionTemplateRe.MatchString(strings.ToLower(strings.TrimSpace(s.TemplateName))) {
		return true
	}
	for _, tag := range s.TemplateTags {
		if strings.ToLower(strings.TrimSpace(tag)) == "function" {
			return true
		}
	}
	return false
}

// pavedRoadProject is the project a scaffold lands in when none is named.
// `default` is the platform project too: the Application composition reads it
// as `adhar`, so the XR says so rather than carrying a label no composed
// object agrees with.
func pavedRoadProject(project string) string {
	if project = strings.TrimSpace(project); project == "" || project == "default" {
		return "adhar"
	}
	return project
}

// scaffoldProject is the project a template scaffold registers in, from the one
// source the Console reads: the template's own `project` parameter
// (agent-service declares one, defaulting to `adhar`, and renders it into
// deploy/base/agentworkload.yaml) when the template declares it, else
// --project. A --project named explicitly is also what that parameter renders,
// so the repository and its Application cannot disagree; naming both
// differently is refused before anything is created.
func scaffoldProject(t *backstageTemplate, flag string, flagExplicit bool, overrides map[string]string) (string, error) {
	declared := false
	var def string
	for _, g := range t.Spec.Parameters {
		if p, ok := g.Properties["project"]; ok {
			declared = true
			if !p.Default.IsZero() {
				_ = p.Default.Decode(&def)
			}
		}
	}
	param, paramSet := overrides["project"]
	switch {
	case !declared:
		return pavedRoadProject(flag), nil
	case flagExplicit && paramSet && pavedRoadProject(param) != pavedRoadProject(flag):
		return "", fmt.Errorf("--project %s and --param project=%s disagree; template %q renders its project into the repository, so name it once",
			flag, param, t.Metadata.Name)
	case flagExplicit:
		overrides["project"] = flag
		return pavedRoadProject(flag), nil
	case paramSet:
		return pavedRoadProject(param), nil
	default:
		return pavedRoadProject(def), nil
	}
}

// controlPlaneDescriptor is the file through which a repository declares its
// own Application: the adhar-services ApplicationSet adopts every repository
// that carries one and applies its directory with self-heal
// (platform/stack/adhar-services-appset.yaml).
const controlPlaneDescriptor = ".adhar/app.yaml"

// checkDescriptorProject refuses a registration that the repository itself
// would overturn. react-spa-app, angular-spa-app and
// tanstack-start-fullstack-app commit .adhar/app.yaml whenever localisation is
// on (their default): this same Application — the app's name, in adhar-system
// — with project `adhar` written into it. Argo CD applies it with self-heal,
// so an app registered here with --project payments went back into `adhar` at
// the bootstrap's first sync, after the composition had begun deploying it
// into payments' environments. The Console never meets this: its wizard offers
// no project for those templates and registers `adhar`. Checked on the
// rendered skeleton, before anything is created.
func checkDescriptorProject(r *renderedScaffold, appName, project string) error {
	raw, ok := r.file(controlPlaneDescriptor)
	if !ok {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
			Spec struct {
				Parameters struct {
					Project string `yaml:"project"`
				} `yaml:"parameters"`
			} `yaml:"spec"`
		}
		// The end of the file, or a file this cannot read — whether the
		// descriptor is valid is for Argo CD to report, as it is for the
		// Console's scaffolds.
		if err := dec.Decode(&doc); err != nil {
			return nil
		}
		if doc.Kind != "Application" || !strings.HasPrefix(doc.APIVersion, helpers.XRGroup+"/") || doc.Metadata.Name != appName ||
			(doc.Metadata.Namespace != "" && doc.Metadata.Namespace != globals.AdharSystemNamespace) {
			continue
		}
		if declared := pavedRoadProject(doc.Spec.Parameters.Project); declared != project {
			return fmt.Errorf("template %q commits %s, this same Application in project %q, and Argo CD applies it with self-heal "+
				"(the adhar-services ApplicationSet adopts every repository that carries one), so registering it in project %q "+
				"would last only until that first sync; register it in project %q, or scaffold without the descriptor "+
				"(the SPA templates write it only with withLocalisation=true)",
				r.templateID, controlPlaneDescriptor, declared, project, declared)
		}
	}
}

// scaffoldNameRe is the Console's NAME_RE: the name becomes a Gitea repository,
// the Application XR and every `<project>-<app>-<env>` the composition derives.
var scaffoldNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)

// validateScaffoldName refuses a name the Console refuses, before anything is
// created: a name the API server rejects would otherwise be found only after
// the repository had been created and committed.
func validateScaffoldName(name string) error {
	if !scaffoldNameRe.MatchString(name) {
		return fmt.Errorf("application name %q is not lowercase kebab-case of 3-63 characters (a letter first, a letter or digit last)", name)
	}
	return nil
}

// publicCloneURL drops the scheme's default port, so the source reads like
// the Console's (https://gitea.<domain>/<org>/<repo>.git).
func publicCloneURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
	}
	return u.String()
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
// adhar-controlplane Gitea (resolved from the active idp config), reachable from the CLI
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

	// Provider-aware Application XR — the same control-plane path the
	// Console uses. helpers.NewXR sets spec.crossplane.compositionSelector.
	appObj := helpers.NewXR("Application", appName, namespace, "application", nil,
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

	// Normalise to a Application XR and ensure a provider-aware
	// composition is selected, so file-based deploys take the same control-plane
	// path as repo-based ones.
	appObj.Object["apiVersion"] = helpers.XRGroup + "/" + helpers.XRVersion
	appObj.Object["kind"] = "Application"
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
		// The legacy spelling is accepted too: `Composite*` kinds were renamed on
		// 2026-10-07 and a manifest written before that still describes the same
		// object. Applying it would fail at the API server, so matching it here
		// is what lets the error name the rename instead of "no Application
		// resource found".
		if (kind == "application" || kind == "compositeapplication") && strings.HasPrefix(apiVersion, "platform.adhar.io/") {
			if kind == "compositeapplication" {
				return nil, fmt.Errorf("%s declares kind CompositeApplication, which was renamed to Application "+
					"(platform.adhar.io/v1alpha1) on 2026-10-07 — change the kind and re-run", path)
			}
			return &unstructured.Unstructured{Object: doc}, nil
		}
	}

	return nil, fmt.Errorf("no Application resource found in %s", path)
}
