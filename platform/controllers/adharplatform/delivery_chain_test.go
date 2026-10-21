package adharplatform

import (
	"strings"
	"testing"
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
		`count(rollout_info{namespace=\"%s\",name=\"%s\",phase!~\"Healthy|Completed\"} == 1) or vector(0)`,
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
// Kargo git credential the Project composes are written for the in-cluster
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
		`rollout_info{namespace=\"$env\",name=\"%s\"}`,
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
