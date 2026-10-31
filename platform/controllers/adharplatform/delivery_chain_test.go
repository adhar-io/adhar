package adharplatform

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// The paved road's promotion chain, end to end: a build lands in dev, is
// VERIFIED there against Prometheus, moves to test on its own, and waits for a
// Release before prod. Four things have to agree for that to happen at all, and
// on 2026-10-16 none of them did: every custom app sat in adhar-system (or
// default) behind a plain Argo CD Application, no Kargo Warehouse or Stage
// existed, and `Project adhar` could not even create its Kargo Project.

// Kargo initialises a Project only in a namespace it labelled. A project named
// after its organisation lives in the ORGANISATION's namespace (the project
// composition defers it to avoid two managers), so that namespace must carry
// the label — without it the Kargo Project and ProjectConfig stay Unready and
// nothing in the project can be promoted.
func TestTheOrganisationNamespaceCanHostItsKargoProject(t *testing.T) {
	comp := organisationComposition(t)
	ns := strings.Index(comp, "kind: Namespace")
	quota := strings.Index(comp, "composition-resource-name: quota")
	if ns < 0 || quota < ns {
		t.Fatal("the organisation composition no longer composes a Namespace before its quota; update this guard")
	}
	if !strings.Contains(comp[ns:quota], `kargo.akuity.io/project: "true"`) {
		t.Error("the organisation namespace lacks kargo.akuity.io/project=\"true\"; Kargo refuses to initialise the " +
			"same-named project there (\"namespace adhar does not belong to Kargo\")")
	}
}

// Freight moves on only when Prometheus says the environment is healthy. The
// Stages are the project's (shared by its applications), so the check is the
// project's too: one AnalysisTemplate per environment, metrics per application.
func TestApplicationStagesVerifyAgainstPrometheusBeforeFreightMovesOn(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "project", "local.yaml")
	for _, want := range []string{
		"kind: AnalysisTemplate",
		`rollout_info{namespace=\"%s\",name=\"%s\",phase=\"Degraded\"}`,
		"kube_pod_status_ready{",
		"kube_pod_container_status_restarts_total{",
		"verification:\n                      analysisTemplates:",
		`replace "$NAMESPACE" $ens .`,
	} {
		if !strings.Contains(comp, want) {
			t.Errorf("the Project composition lacks %q; a promoted build is not checked before the next environment takes it", want)
		}
	}
	if !strings.Contains(readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml"), "interval: 2m") {
		t.Error("the Warehouse no longer polls Harbor every 2m")
	}
	xrd := readConfigurationFile(t, "xrd", "apps.xrd.yaml")
	for _, want := range []string{"verification:", "errorRateQuery:", "errorRateThreshold:", "failureLimit:"} {
		if !strings.Contains(xrd, want) {
			t.Errorf("the Application XRD does not declare promotion.verification.%s; the XRD is strict, so an application setting it is rejected", strings.TrimSuffix(want, ":"))
		}
	}
}

// Workloads promoted into dev/test/prod must be able to pull from Harbor.
func TestEveryEnvironmentNamespaceGetsTheHarborPullSecret(t *testing.T) {
	body := readStackFile(t, "application/adhar-supply-chain/manifests/55-pull-secret-sync.yaml")
	for _, want := range []string{
		"kind: ClusterPolicy",
		"{key: adhar.io/environment, operator: Exists}",
		"synchronize: true",
		"generateExisting: true",
		"clone:\n          namespace: adhar-system\n          name: harbor-dockercfg",
		"clone:\n          namespace: adhar-system\n          name: root-creds",
		`rbac.kyverno.io/aggregate-to-background-controller: "true"`,
		"resources: [secrets]",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pull-secret sync lacks %q; a Rollout in dev sits in ImagePullBackOff with a good image in Harbor", want)
		}
	}
}

// One writer per overlay: Kargo. app-ci must not pin images in a repository
// that has dev/test/prod overlays — rewriting the base tag would move every
// environment at once.
func TestAppCiDoesNotPinImagesWhereKargoPromotes(t *testing.T) {
	body := readStackFile(t, "application/adhar-supply-chain/manifests/70-app-ci.yaml")
	guard := strings.Index(body, "if [ -d deploy/envs ]; then")
	targets := strings.Index(body, `TARGETS=$(grep -rl "${REGISTRY}/${REPO}" deploy manifests`)
	if guard < 0 || targets < 0 || guard > targets {
		t.Error("gitops-writeback pins images even when deploy/envs exists; app-ci and Kargo then both commit to the same overlay, and the base tag moves every environment")
	}
}

// The console and CLI hand the Application XR the repository's PUBLIC clone URL
// (https://gitea.<domain>/<org>/<repo>.git); the AppProject allow-list and the
// Kargo git credential the Project composes are written for the adhar-controlplane
// Gitea. The composition must reconcile the two, or Argo CD refuses the repo
// and Kargo cannot push the promotion commit (every app, 2026-10-17).
func TestTheApplicationCompositionAddressesThePlatformGiteaInCluster(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	if !strings.Contains(comp, `regexReplaceAll "^https?://gitea\\.[^/]+/" $repoURL (printf "http://%s/" $giteaHost)`) {
		t.Error("the Application composition no longer rewrites the public Gitea URL to gitea-http.adhar-system; " +
			"Argo CD will answer \"repo is not permitted in project\" and Kargo \"could not read Username\"")
	}
}

// A promotion commit must not start a build: a build publishes a new digest,
// which is new freight, which is another promotion commit.
func TestPromotionCommitsDoNotTriggerTheBuildPipeline(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "project", "local.yaml")
	if !strings.Contains(comp, `(printf "promote(%s): %s %s [skip ci]" $en $app $digest)`) {
		t.Error("the Kargo git-commit message lacks \"[skip ci]\"; app-ci's CEL filter would build the promotion commit and the chain never settles")
	}
	ci := readStackFile(t, "application/adhar-supply-chain/manifests/70-app-ci.yaml")
	if !strings.Contains(ci, `!body.head_commit.message.contains('[skip ci]')`) {
		t.Error("app-ci no longer filters \"[skip ci]\" pushes; the promotion commit's marker is inert")
	}
}

// The Application composition observes its Project through provider-kubernetes.
// An observe-only Object still computes desired state with a server-side DRY
// RUN, which is a PATCH; without the verb every Application XR stays at
// "Unready resources: project".
func TestProviderKubernetesMayDryRunPlatformObjects(t *testing.T) {
	rbac := readConfigurationFile(t, "providers", "provider-runtime-rbac.yaml")
	i := strings.Index(rbac, `apiGroups: ["platform.adhar.io"]`)
	if i < 0 {
		t.Fatal("provider-runtime-rbac.yaml no longer grants provider-kubernetes anything on platform.adhar.io")
	}
	verbs := rbac[i:]
	if j := strings.Index(verbs, "verbs:"); j >= 0 {
		verbs = verbs[j : j+strings.Index(verbs[j:], "\n")]
	}
	if !strings.Contains(verbs, `"patch"`) {
		t.Errorf("provider-kubernetes lacks patch on platform.adhar.io (%s); observing a Project fails with \"cannot dry run SSA ... forbidden\"", verbs)
	}
}

// Kyverno's admission controller validates a generate rule by checking ITS OWN
// access to the clone source, so the aggregated role must reach it too — or
// the policy is denied at admission and the sync never applies it.
func TestThePullSecretSyncRoleReachesBothKyvernoControllers(t *testing.T) {
	body := readStackFile(t, "application/adhar-supply-chain/manifests/55-pull-secret-sync.yaml")
	for _, want := range []string{
		`rbac.kyverno.io/aggregate-to-admission-controller: "true"`,
		`rbac.kyverno.io/aggregate-to-background-controller: "true"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("55-pull-secret-sync.yaml lacks %s; \"kyverno-admission-controller requires permissions list,get for resource v1/Secret\"", want)
		}
	}
}

// One environment namespace holds every application of its project, and the
// quota cap for it is declared in four places that must agree: the Project and
// Environment XRD defaults, the project composition's fallbacks, and the default
// project's explicit values. At the former 4 CPU the default project's dev
// namespace was full after five small services (2026-10-17).
func TestEnvironmentQuotaDefaultsAgreeEverywhere(t *testing.T) {
	const cpu, mem, pods = "16", "32Gi", "100"
	checks := map[string][]string{
		"xrd/project.xrd.yaml":            {"default: '" + cpu + "'", "default: " + mem, "default: " + pods},
		"xrd/env.xrd.yaml":                {"default: '" + cpu + "'", "default: " + mem, "default: " + pods},
		"compositions/project/local.yaml": {`dig "cpuQuota" "` + cpu + `"`, `dig "memoryQuota" "` + mem + `"`, `dig "podQuota" ` + pods},
		"stack:application/adhar-supply-chain/manifests/95-default-tenancy.yaml": {`cpuQuota: "` + cpu + `"`, "memoryQuota: " + mem, "podQuota: " + pods},
	}
	for file, wants := range checks {
		var body string
		if strings.HasPrefix(file, "stack:") {
			body = readStackFile(t, strings.TrimPrefix(file, "stack:"))
		} else {
			body = readConfigurationFile(t, strings.Split(file, "/")...)
		}
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q; the environment quota caps have drifted apart", file, want)
			}
		}
	}
}

// Kargo keys a step's outputs by its `as` alias, never by `uses`. The Argo CD
// update step reads the pushed commit from the push step, so the alias and the
// reference must be the same word — or the promotion pushes and then fails.
func TestThePromotionReadsThePushedCommitByItsAlias(t *testing.T) {
	// The shared Stage carries one push step per application, so each has its
	// own alias `push-<app>` and its Argo CD step reads exactly that one.
	comp := readConfigurationFile(t, "compositions", "project", "local.yaml")
	if !strings.Contains(comp, "- uses: git-push\n                            if: {{ quote $when }}\n                            as: {{ quote (printf \"push-%s\" $app) }}\n") {
		t.Error("the git-push step has no `as: push-<app>` alias; its outputs are unnamed")
	}
	if strings.Contains(comp, "outputs['git-push']") || !strings.Contains(comp, `outputs['push-%s'].commit }}" $ex $app`) {
		t.Error("the Argo CD update step does not read outputs['push-<app>'].commit; Kargo reports \"cannot fetch commit from <nil>\"")
	}
}

// Kargo updates the Argo CD Application a promotion targets, and looks for it in
// ONE configured namespace. Every package here lives in adhar-system (ADR-0011),
// so the chart default `argocd` finds nothing — after the commit is pushed. The
// manifest is generated from the values, so both must carry the namespace.
func TestKargoLooksForArgoCDApplicationsInThePlatformNamespace(t *testing.T) {
	values := readStackFile(t, "application/kargo/values.yaml")
	if !strings.Contains(values, "argocd:\n    namespace: adhar-system") {
		t.Error("kargo/values.yaml does not set controller.argocd.namespace: adhar-system")
	}
	install := readStackFile(t, "application/kargo/manifests/install.yaml")
	if !strings.Contains(install, "ARGOCD_NAMESPACE: adhar-system") || strings.Contains(install, "ARGOCD_NAMESPACE: argocd") {
		t.Error("kargo/manifests/install.yaml still tells the controller Argo CD is in `argocd`; argocd-update steps fail with 'unable to find Argo CD Application'")
	}
}

// Kargo updates only an Argo CD Application that names, in an annotation, the
// Stage allowed to touch it; without it the promotion pushes the commit and then
// fails "is not authorized".
func TestEveryEnvironmentApplicationAuthorisesItsKargoStage(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	if !strings.Contains(comp, `kargo.akuity.io/authorized-stage: {{ quote (printf "%s:%s" $project $envName) }}`) {
		t.Error("the per-environment Argo CD Application carries no kargo.akuity.io/authorized-stage annotation naming the project's shared Stage <project>:<env>")
	}
}

// An environment's default-deny must still admit the platform plane: the
// Gateway's Envoy that serves its URLs, Prometheus that scrapes it, and the
// operators driving its data services all live in adhar-system (ADR-0011).
func TestEnvironmentIsolationAdmitsThePlatformPlane(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "env", "local.yaml")
	i := strings.Index(comp, "-default-deny")
	if i < 0 {
		t.Fatal("the environment composition no longer composes a default-deny NetworkPolicy; update this guard")
	}
	block := comp[i:]
	if j := strings.Index(block, "{{- end }}"); j >= 0 {
		block = block[:j]
	}
	if !strings.Contains(block, "kubernetes.io/metadata.name: adhar-system") {
		t.Error("the environment default-deny admits only its own namespace; the Gateway's Envoy in adhar-system cannot reach the apps and every environment URL answers 503")
	}
}

// The Gateway's Envoy carries Cilium's reserved `ingress` identity, which no
// namespaceSelector matches, so an isolated environment must also admit that
// entity with a CiliumNetworkPolicy — and provider-kubernetes must be allowed
// to create one.
func TestEnvironmentIsolationAdmitsTheGatewayDataPath(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "env", "local.yaml")
	if !strings.Contains(comp, "kind: CiliumNetworkPolicy") || !strings.Contains(comp, "fromEntities: [ingress]") {
		t.Error("the environment composition admits no `ingress` entity; the Gateway answers 503 \"connection timeout\" for every environment URL")
	}
	rbac := readConfigurationFile(t, "providers", "provider-runtime-rbac.yaml")
	if !strings.Contains(rbac, `resources: ["ciliumnetworkpolicies"]`) {
		t.Error("provider-kubernetes may not manage ciliumnetworkpolicies; the environment's gateway policy is never created")
	}
}

// Freight is what the paved-road CI signed and tagged with the commit SHA;
// without the tag filter a kpack build or a hand-pushed `latest` becomes
// freight and is promoted by digest, unsigned.
func TestOnlyCommitTaggedImagesBecomeFreight(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	if !strings.Contains(comp, `allowTagsRegexes: ["^[0-9a-f]{40}$"]`) {
		t.Error("the Warehouse image subscription has no allowTagsRegexes for the 40-hex commit SHA app-ci tags with (Kargo 1.11 refuses the older allowTags)")
	}
}

// A composed broker must carry its own resources and heap: inside an
// environment namespace it otherwise inherits the LimitRange's 512Mi and dies
// of "Java heap space" on every start.
func TestComposedKafkaBrokersSizeTheirOwnHeap(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "messaging", "local-strimzi.yaml")
	for _, want := range []string{"limits: {memory: 1536Mi}", "-Xmx: 1024m", "log.cleaner.dedupe.buffer.size: 33554432"} {
		if !strings.Contains(comp, want) {
			t.Errorf("the Messaging composition lacks %q; a broker in a quota'd namespace OOMs at start", want)
		}
	}
}

// Every application gets a Grafana dashboard of its own: the Rollout, its pods,
// their resources and HTTP traffic per environment, discovered by Grafana's
// sidecar through the grafana_dashboard label.
func TestEveryApplicationComposesAGrafanaDashboard(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	for _, want := range []string{
		"composition-resource-name: dashboard",
		`grafana_dashboard: "1"`,
		"grafana_folder: Applications",
		`rollout_info{namespace=\"$env\",name=\"__APP__\",phase=`,
		"http_server_requests_seconds_count",
		"container_memory_working_set_bytes",
	} {
		if !strings.Contains(comp, want) {
			t.Errorf("the Application composition's dashboard lacks %q", want)
		}
	}
}

// A Promotion must be CREATED, never applied by name: Kargo's webhook renames
// every Promotion, and an apply over the rename leaves a ghost Kargo never
// reconciles. The Release composes a one-shot Job that creates it with
// generateName, as an identity the Project composition grants.
func TestAReleaseCreatesItsPromotionInsteadOfApplyingOne(t *testing.T) {
	rel := readConfigurationFile(t, "compositions", "release", "kargo.yaml")
	if strings.Contains(rel, "kind: Promotion\n                  metadata:\n                    name:") {
		t.Error("the Release composition still applies a Promotion by name; Kargo's webhook renames it and the result is a ghost")
	}
	for _, want := range []string{"kind: Job", "generateName: ${STAGE}-", "serviceAccountName: adhar-release", `kubectl create -f -`} {
		if !strings.Contains(rel, want) {
			t.Errorf("the Release composition lacks %q", want)
		}
	}
	proj := readConfigurationFile(t, "compositions", "project", "local.yaml")
	for _, want := range []string{"name: adhar-release", "verbs: [get, list, promote]", "resources: [promotions]"} {
		if !strings.Contains(proj, want) {
			t.Errorf("the Project composition does not grant the release identity %q", want)
		}
	}
}

// On the first reconcile the Project has not been observed yet, so the
// environment list is empty. Anything that indexes it must sit inside the
// `if $envs` guard: the per-application dashboard's `index $envNames 0` ran
// outside it, returned a FATAL result, the Project was never composed, and
// every Application XR stayed unsynced for ever while CI built images nobody
// deployed (Azure, 2026-10-18).
func TestNothingIndexesTheEnvironmentsBeforeTheProjectReportsThem(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	guard := strings.Index(comp, "{{- if $envs }}")
	if guard < 0 {
		t.Fatal("the Application composition no longer guards its environment-dependent resources with `if $envs`")
	}
	for _, needle := range []string{"index $envNames", "$firstEnv"} {
		if at := strings.Index(comp, needle); at >= 0 && at < guard {
			t.Errorf("%q is evaluated before the `if $envs` guard; on the first pass the list is empty and the "+
				"whole composition fails, so the Project is never composed", needle)
		}
	}
}

// An HPA and Argo CD must not both own a Rollout's replica count: each HPA
// decision was reverted by selfHeal, the replacement pod's boot window made the
// Rollout Progressing, and Kargo failed verification for it (GCP, 2026-10-21).
func TestArgoCDLeavesTheAutoscalersReplicaCountAlone(t *testing.T) {
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	if !strings.Contains(comp, "kind: Rollout\n                        managedFieldsManagers: [kube-controller-manager]") {
		t.Error("the per-environment Argo CD Application does not ignore Rollout fields the HPA (kube-controller-manager) owns")
	}
	if !strings.Contains(comp, "RespectIgnoreDifferences=true") {
		t.Error("RespectIgnoreDifferences is off, so a sync would overwrite the HPA's replica count anyway")
	}
}

// Every environment syncs its OWN rendered branch, written only by promotions
// into that environment. On the shared source branch a test promotion moved
// dev's synced revision past the commit dev's promotion made, and Kargo's
// argocd-update health check reported dev Unhealthy after every downstream
// promotion (GCP, 2026-10-21).
func TestEachEnvironmentSyncsItsOwnRenderedBranch(t *testing.T) {
	app := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	if !strings.Contains(app, `targetRevision: {{ quote (printf "stage/%s" $envName) }}`) {
		t.Error("a promoted environment's Argo CD Application does not follow its rendered branch stage/<env>")
	}
	if !strings.Contains(app, `ls-remote --exit-code --heads "$REPO" "$b"`) || !strings.Contains(app, "kustomize build") {
		t.Error("nothing seeds the stage/<env> branches, so a new application's environments have nothing to sync before their first promotion")
	}
	proj := readConfigurationFile(t, "compositions", "project", "local.yaml")
	for _, want := range []string{
		`{branch: {{ quote (printf "stage/%s" $en) }}, create: true, path: ./out}`,
		"- uses: kustomize-build",
		"outPath: ./out/manifests.yaml",
		`targetBranch: {{ quote (printf "stage/%s" $en) }}`,
	} {
		if !strings.Contains(proj, want) {
			t.Errorf("the promotion does not render into the environment's own branch: missing %q", want)
		}
	}
	if strings.Contains(proj, "path: ./src\n                              targetBranch") {
		t.Error("a promotion still pushes the shared source branch")
	}
}

// Only the FIRST environment, the project's one Stage that takes freight
// straight from the Warehouse, is seeded from its overlay. Every later
// stage/<env> starts with no objects and waits for its first promotion: seeded
// from overlays that ask for :latest, test and prod ran builds nobody had
// promoted, and prod went live on one nobody had released (reactapp on Azure,
// 2026-10-28). The script is RUN as rendered, with git and kustomize stood in
// for, and every branch it pushes is read the way Argo CD reads it.
func TestOnlyTheFirstEnvironmentIsSeededWithAWorkload(t *testing.T) {
	docs := renderGoTemplate(t, fdApplicationRequest(t, "observed-deployed.yaml"), "compositions", "apps", "argocd-multienv.yaml")
	script, env := seedJobScript(t, fdJob(t, docs))
	if env["ENVS"] != "dev test prod" {
		t.Fatalf("the seed Job's ENVS is %q, want the project's promotion order \"dev test prod\"", env["ENVS"])
	}
	overlays := func(envs ...string) map[string]string {
		files := map[string]string{}
		for _, e := range envs {
			files["deploy/envs/"+e+"/kustomization.yaml"] = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [../../base]\n"
		}
		return files
	}
	brokenTest := overlays("dev", "prod")
	brokenTest["deploy/envs/test/kustomization.yaml"] = "BROKEN"
	for _, c := range []struct {
		name, envs, existing string
		files                map[string]string
		code                 int
		workload, awaiting   []string
	}{
		{"a new application", "dev test prod", "", overlays("dev", "test", "prod"), 0, []string{"dev"}, []string{"test", "prod"}},
		{"a later overlay is still built, and a broken one fails the Job", "dev test prod", "", brokenTest, 4, []string{"dev"}, []string{"prod"}},
		{"the first environment's branch already exists", "dev test prod", "stage/dev", overlays("dev", "test", "prod"), 0, nil, []string{"test", "prod"}},
		{"the first environment is the project's first, whatever its name", "staging prod", "", overlays("staging", "prod"), 0, []string{"staging"}, []string{"prod"}},
		{"existing branches belong to the promotions", "dev test prod", "stage/dev stage/test stage/prod", overlays("dev", "test", "prod"), 0, nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := map[string]string{}
			for k, v := range env {
				run[k] = v
			}
			run["ENVS"] = c.envs
			code, pushed, out := runSeedScript(t, script, run, c.files, c.existing)
			if code != c.code {
				t.Fatalf("the seed script exited %d, want %d:\n%s", code, c.code, out)
			}
			var branches []string
			for b := range pushed {
				branches = append(branches, b)
			}
			sort.Strings(branches)
			if len(branches) != len(c.workload)+len(c.awaiting) {
				t.Errorf("the seed script pushed %v, want stage/<env> for %v and %v:\n%s", branches, c.workload, c.awaiting, out)
			}
			for _, e := range c.workload {
				b := pushed["stage/"+e]
				if objs := argoCDObjects(t, b.manifests); len(objs) == 0 || objs[0].GetKind() != "Rollout" {
					t.Errorf("stage/%s was not seeded from its overlay:\n%s", e, b.manifests)
				}
				if b.message != "seed stage/"+e+" from main [skip ci]" {
					t.Errorf("stage/%s was committed as %q", e, b.message)
				}
			}
			for _, e := range c.awaiting {
				b, ok := pushed["stage/"+e]
				if !ok {
					t.Errorf("stage/%s was not created, so its Argo CD Application cannot even compare until the first promotion", e)
					continue
				}
				if objs := argoCDObjects(t, b.manifests); len(objs) != 0 {
					t.Errorf("stage/%s deploys %d object(s) before anything was promoted into %s:\n%s", e, len(objs), e, b.manifests)
				}
				if b.message != "seed stage/"+e+": awaiting first promotion [skip ci]" {
					t.Errorf("stage/%s was committed as %q", e, b.message)
				}
			}
		})
	}
}

// A later environment still on its seed branch awaits its first promotion. It
// is named, not counted, and does not keep the application from Healthy: prod
// may wait for a Release indefinitely. A promotion always syncs (Argo CD keeps
// it in status.history), so a promoted environment that renders nothing still
// reads "deploys nothing", as does an empty FIRST environment, which the seed
// fills from its overlay.
func TestALaterEnvironmentAwaitsItsFirstPromotion(t *testing.T) {
	for _, c := range []struct {
		name         string
		empty        map[string]bool // environment -> it has synced before
		phase, about string
	}{
		{"test and prod before their first promotion", map[string]bool{"test": false, "prod": false}, "Healthy",
			"1/3 environments healthy; test awaits its first promotion; prod awaits its first promotion (by a Release)"},
		{"a promoted environment that renders nothing", map[string]bool{"test": true, "prod": false}, "Progressing",
			"1/3 environments healthy; test deploys nothing (stage/test renders no manifests); prod awaits its first promotion (by a Release)"},
		{"an empty first environment", map[string]bool{"dev": false}, "Progressing",
			"2/3 environments healthy; dev deploys nothing (stage/dev renders no manifests)"},
		{"nothing deployed anywhere", map[string]bool{"dev": false, "test": false, "prod": false}, "Pending",
			"0/3 environments healthy; dev deploys nothing (stage/dev renders no manifests); test awaits its first promotion; prod awaits its first promotion (by a Release)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			observed := observedWithEmptyEnvironments(t, c.empty)
			xr := readYAMLDocs(t, filepath.Join(fdFixtureDir, "application-function.yaml"))[0]
			st := docOfKind(renderGoTemplate(t, map[string]any{"observed": map[string]any{
				"composite": map[string]any{"resource": xr}, "resources": fnObserved(t, observed),
			}}, "compositions", "apps", "argocd-multienv.yaml"), "Application")
			if dig(st, "status", "phase") != c.phase || dig(st, "status", "message") != c.about {
				t.Errorf("status is %v / %q, want %s / %q", dig(st, "status", "phase"), dig(st, "status", "message"), c.phase, c.about)
			}
		})
	}

	// The same through the real function containers, as Crossplane runs them.
	t.Run("crossplane render", func(t *testing.T) {
		if testing.Short() {
			t.Skip("-short: skipping `crossplane render`, which starts the function containers")
		}
		bin, err := exec.LookPath("crossplane")
		if err != nil {
			t.Skip("the crossplane CLI is not installed; the in-process renders still ran")
		}
		if err := exec.Command("docker", "info").Run(); err != nil {
			t.Skip("Docker is not available; `crossplane render` runs each function in a container")
		}
		var file bytes.Buffer
		for _, d := range observedWithEmptyEnvironments(t, map[string]bool{"test": false, "prod": false}) {
			b, err := yaml.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			file.WriteString("---\n")
			file.Write(b)
		}
		observed := filepath.Join(t.TempDir(), "observed-awaiting.yaml")
		if err := os.WriteFile(observed, file.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, "render", filepath.Join(fdFixtureDir, "application-function.yaml"),
			filepath.Join(controlPlaneDir(t), "compositions", "apps", "argocd-multienv.yaml"),
			filepath.Join(fnFixtureDir, "functions.yaml"), "--observed-resources="+observed)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
		}
		for _, d := range strings.Split(stdout.String(), "\n---") {
			var m map[string]any
			if yaml.Unmarshal([]byte(d), &m) != nil || m["kind"] != "Application" || m["apiVersion"] != "platform.adhar.io/v1alpha1" {
				continue
			}
			if dig(m, "status", "phase") != "Healthy" || dig(m, "status", "message") !=
				"1/3 environments healthy; test awaits its first promotion; prod awaits its first promotion (by a Release)" {
				t.Errorf("rendered status = %v / %q", dig(m, "status", "phase"), dig(m, "status", "message"))
			}
			return
		}
		t.Fatal("crossplane render returned no Application XR")
	})
}

// :latest is what an environment no promotion has pinned resolves for each new
// pod, so only a build of main may move it. A pull request's preview build
// pushed :latest too, before any gate, so once signed it reached such an
// environment through the next scale-up or eviction. The build step is RUN,
// with the lifecycle stood in for, to see which names it exports.
func TestAPullRequestBuildLeavesLatestAlone(t *testing.T) {
	const image, sha = "harbor-core.adhar-system.svc.cluster.local/library/app", "0123456789abcdef0123456789abcdef01234567"
	task := findDoc(t, docsOf(t, supplyChain+"60-build-pipelines.yaml"), "Task", "buildpacks-build")
	if got := taskParams(sgDig(task, "spec").(map[string]any))["tag-latest"]; got != "true" {
		t.Errorf("buildpacks-build's tag-latest defaults to %q; a bare build must still move :latest", got)
	}
	var script string
	for _, raw := range sgDig(task, "spec", "steps").([]any) {
		if s := raw.(map[string]any); s["name"] == "build" {
			script = fmt.Sprint(sgDig(s, "args", 0))
		}
	}
	if !strings.Contains(script, "exec /cnb/lifecycle/creator") {
		t.Fatal("buildpacks-build's build step no longer execs the lifecycle; update this guard")
	}
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on PATH")
	}
	exported := func(tagLatest string) []string {
		t.Helper()
		bin := t.TempDir()
		writeFiles(t, bin, map[string]string{"creator": "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done\n"})
		if err := os.Chmod(filepath.Join(bin, "creator"), 0o755); err != nil {
			t.Fatal(err)
		}
		s := strings.NewReplacer("$(params.image)", image, "$(params.extra-tag)", sha, "$(params.tag-latest)", tagLatest,
			"$(params.subdirectory)", "", "$(workspaces.source.path)", "/workspace/source",
			"/cnb/lifecycle/creator", filepath.Join(bin, "creator")).Replace(script)
		out, err := exec.Command(sh, "-c", s).CombinedOutput()
		if err != nil {
			t.Fatalf("the build step failed with tag-latest=%s: %v\n%s", tagLatest, err, out)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}
	main := exported("true")
	if main[len(main)-1] != image+":latest" || !strings.Contains(strings.Join(main, " "), "-tag="+image+":"+sha) {
		t.Errorf("a build of main exports %v, want %s:latest tagged %s too, exactly as before", main, image, sha)
	}
	pr := exported("false")
	if pr[len(pr)-1] != image+":"+sha || strings.Contains(strings.Join(pr, " "), ":latest") {
		t.Errorf("a build with tag-latest=false exports %v, want %s:%s alone", pr, image, sha)
	}

	ci := docsOf(t, supplyChain+"70-app-ci.yaml")
	pipeline := findDoc(t, ci, "Pipeline", "app-ci")
	if got := taskParams(sgDig(pipeline, "spec").(map[string]any))["tag-latest"]; got != "true" {
		t.Errorf("app-ci's tag-latest defaults to %q, want \"true\" for builds of main", got)
	}
	build := pipelineTasks(t, pipeline, "tasks")["build"]
	if sgDig(build, "taskRef", "name") != "buildpacks-build" || taskParams(build)["tag-latest"] != "$(params.tag-latest)" {
		t.Errorf("app-ci's build does not hand tag-latest to buildpacks-build: %v", build["params"])
	}
	previews := 0
	for _, d := range ci {
		if d["kind"] != "TriggerTemplate" {
			continue
		}
		run := sgDig(d, "spec", "resourcetemplates", 0)
		tagLatest := taskParams(sgDig(run, "spec").(map[string]any))["tag-latest"]
		if sgDig(run, "metadata", "labels", "adhar.io/preview") == "true" {
			previews++
			if tagLatest != "false" {
				t.Errorf("TriggerTemplate %v builds previews with tag-latest=%q; a pull request moves :latest", sgDig(d, "metadata", "name"), tagLatest)
			}
		} else if tagLatest == "false" {
			t.Errorf("TriggerTemplate %v builds main with tag-latest=false; :latest would stop following main", sgDig(d, "metadata", "name"))
		}
	}
	if previews == 0 {
		t.Error("no TriggerTemplate in 70-app-ci.yaml runs labelled adhar.io/preview; update this guard")
	}
}

type seededBranch struct{ manifests, message string }

// seedJobScript returns the seed Job's script and its literal environment.
func seedJobScript(t *testing.T, job map[string]any) (string, map[string]string) {
	t.Helper()
	cs, _ := dig(job, "spec", "template", "spec", "containers").([]any)
	if len(cs) != 1 {
		t.Fatalf("the seed Job has %d containers, want 1", len(cs))
	}
	c := cs[0].(map[string]any)
	env := map[string]string{}
	for _, raw := range c["env"].([]any) {
		if e := raw.(map[string]any); e["value"] != nil {
			env[fmt.Sprint(e["name"])] = fmt.Sprint(e["value"])
		}
	}
	return fmt.Sprint(sgDig(c, "args", 0)), env
}

// runSeedScript runs the seed script with git and kustomize stood in for: a
// clone copies src, ls-remote finds the branches `existing` names, and a push
// files the commit (manifests.yaml and its message) under its branch.
// kustomize renders what every template's overlay renders, a Rollout on
// :latest, and fails an overlay marked BROKEN.
func runSeedScript(t *testing.T, script string, env, src map[string]string, existing string) (int, map[string]seededBranch, string) {
	t.Helper()
	bin, checkout, pushes := t.TempDir(), t.TempDir(), t.TempDir()
	writeFiles(t, checkout, src)
	writeFiles(t, bin, map[string]string{
		"git": `#!/bin/sh
while [ "$1" = "-c" ]; do shift 2; done
cmd="$1"; shift
for a in "$@"; do last="$a"; done
case "$cmd" in
  clone) cp -R "$FAKE_SRC" "$last" ;;
  ls-remote) case " $FAKE_BRANCHES " in *" $last "*) exit 0 ;; *) exit 2 ;; esac ;;
  commit) printf '%s' "$last" > .seed-message ;;
  push) to="$FAKE_PUSHES/${last#HEAD:refs/heads/}"; mkdir -p "$to" && cp manifests.yaml .seed-message "$to/" ;;
esac
exit 0
`,
		"kustomize": `#!/bin/sh
grep -q BROKEN "$2/kustomization.yaml" && { echo "Error: accumulating resources" >&2; exit 1; }
printf 'apiVersion: argoproj.io/v1alpha1\nkind: Rollout\nmetadata:\n  name: app\nspec:\n  template:\n    spec:\n      containers:\n        - name: app\n          image: harbor-core.adhar-system.svc.cluster.local/library/app:latest\n'
`,
	})
	for _, f := range []string{"git", "kustomize"} {
		if err := os.Chmod(filepath.Join(bin, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "PATH="+bin+":"+os.Getenv("PATH"), "HOME="+t.TempDir(), "TMPDIR="+t.TempDir(),
		"GITEA_USER=u", "GITEA_PASSWORD=p", "FAKE_SRC="+checkout, "FAKE_BRANCHES="+existing, "FAKE_PUSHES="+pushes)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	pushed := map[string]seededBranch{}
	err := filepath.WalkDir(pushes, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "manifests.yaml" {
			return err
		}
		branch, _ := filepath.Rel(pushes, filepath.Dir(p))
		manifests, _ := os.ReadFile(p)
		message, _ := os.ReadFile(filepath.Join(filepath.Dir(p), ".seed-message"))
		pushed[filepath.ToSlash(branch)] = seededBranch{string(manifests), string(message)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return code, pushed, out.String()
}

// argoCDObjects reads a rendered branch the way Argo CD's repo-server reads a
// directory source (reposerver/repository splitYAMLOrJSON): a document that
// decodes to null, empty or only comments, is skipped, not an error. An
// Application with no objects compares Synced and Healthy, and auto-sync acts
// only on OutOfSync, so an empty branch is left alone.
func argoCDObjects(t *testing.T, manifests string) []*unstructured.Unstructured {
	t.Helper()
	d := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(manifests), 4096)
	var objs []*unstructured.Unstructured
	for {
		u := &unstructured.Unstructured{}
		if err := d.Decode(&u); err != nil {
			if errors.Is(err, io.EOF) {
				return objs
			}
			t.Fatalf("Argo CD could not read the branch (%v):\n%s", err, manifests)
		}
		if u != nil {
			objs = append(objs, u)
		}
	}
}

// observedWithEmptyEnvironments is observed-deployed.yaml with the named
// environments' Argo CD Applications Synced and Healthy with no resources, and
// a sync in their history when the value is true.
func observedWithEmptyEnvironments(t *testing.T, empty map[string]bool) []map[string]any {
	t.Helper()
	docs := readYAMLDocs(t, filepath.Join(fdFixtureDir, "observed-deployed.yaml"))
	seen := 0
	for _, d := range docs {
		name, _ := dig(d, "metadata", "annotations", "crossplane.io/composition-resource-name").(string)
		env, isApp := strings.CutPrefix(name, "app-")
		synced, ok := empty[env]
		if !isApp || !ok {
			continue
		}
		status := map[string]any{"sync": map[string]any{"status": "Synced", "revision": "5eed5eed"},
			"health": map[string]any{"status": "Healthy"}, "resources": []any{}}
		if synced {
			status["history"] = []any{map[string]any{"id": 0, "revision": "9c0ffee0"}}
		}
		dig(d, "status", "atProvider", "manifest").(map[string]any)["status"] = status
		seen++
	}
	if seen != len(empty) {
		t.Fatalf("observed-deployed.yaml has no Argo CD Application for some of %v", empty)
	}
	return docs
}

// The Performance Center suite repository holds k6 tests, not an application.
// app-ci built it on every push and failed (no buildpack, no deploy/), and
// scaffolds now add a test to it for every new service (GCP, 2026-10-21).
func TestAppCiIgnoresThePerformanceSuiteRepository(t *testing.T) {
	ci := readStackFile(t, "application/adhar-supply-chain/manifests/70-app-ci.yaml")
	if strings.Count(ci, "'performance-tests']") < 2 {
		t.Error("app-ci's push and preview triggers do not exclude the performance-tests suite repository")
	}
}
