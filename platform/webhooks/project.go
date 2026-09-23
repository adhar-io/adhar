package webhooks

// ProjectValidator enforces the hierarchical tenant quota on CompositeProject.
//
// Per-namespace ResourceQuotas (which the project composition already creates)
// stop one project exhausting the cluster. They do nothing about a team creating
// thirty individually-reasonable projects. This validator is the ceiling: on every
// create or update it sums what the owning team already holds and refuses the
// request if the total would exceed the team's declared allowance.
//
// It fails OPEN in three situations, each deliberate:
//   - no allowance ConfigMap, or no entry for the team and no `default` → nothing
//     is enforced. Shipping this must not start rejecting projects on clusters
//     that never opted in.
//   - the ConfigMap exists but the team's entry does not parse → admit, and say so
//     in the log. A typo in one team's ceiling must not block every other team.
//   - the API read fails → admit. An admission webhook that denies on its own
//     transient errors takes self-service down with it.
//
// It fails CLOSED on exactly one thing: a request whose own quota strings are
// unparseable. That is the caller's input, and accepting it would let a typo buy
// unlimited capacity.

import (
	"context"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// compositeProjectListGVK is the XRD in configuration/xrd/project.xrd.yaml.
// Listed as unstructured because the XR has no generated Go type — Crossplane
// creates the CRD from the XRD at install time.
var compositeProjectListGVK = schema.GroupVersionKind{
	Group:   "platform.adhar.io",
	Version: "v1alpha1",
	Kind:    "CompositeProjectList",
}

// ProjectValidator needs a reader because a ceiling is a property of the TEAM,
// which cannot be known from the object under admission alone.
type ProjectValidator struct {
	Client    client.Client
	Namespace string
	decoder   admission.Decoder
}

// InjectDecoder satisfies the decoder-injection contract used by the other
// validators in this package.
func (v *ProjectValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

func (v *ProjectValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := ctrllog.FromContext(ctx).WithName("tenant-quota")

	obj := &unstructured.Unstructured{}
	if v.decoder != nil {
		if err := v.decoder.Decode(req, obj); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	} else if err := obj.UnmarshalJSON(req.Object.Raw); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	incoming, team, err := projectRequestFrom(obj)
	if err != nil {
		// The caller's own numbers. Fail closed.
		return admission.Denied(err.Error())
	}
	if team == "" {
		// A project with no team has no tenant to charge; the composition's own
		// required-field validation owns that complaint, not this one.
		return admission.Allowed("no team declared; tenant quota not applicable")
	}

	allow, found, err := v.allowanceFor(ctx, team)
	if err != nil {
		log.Info("tenant quota not enforced: could not read the allowance", "team", team, "error", err.Error())
		return admission.Allowed("tenant quota unavailable")
	}
	if !found {
		return admission.Allowed(fmt.Sprintf("no tenant quota declared for team %q", team))
	}

	existing, err := v.projectsOfTeam(ctx, team)
	if err != nil {
		log.Info("tenant quota not enforced: could not list the team's projects", "team", team, "error", err.Error())
		return admission.Allowed("tenant usage unavailable")
	}

	usage, err := SumUsage(existing, incoming.Name)
	if err != nil {
		// One malformed sibling must not block a well-formed request, but it does
		// mean the total is unknown — so say which project is at fault.
		log.Info("tenant quota not enforced: a sibling project has an unparseable quota", "team", team, "error", err.Error())
		return admission.Allowed("tenant usage unparseable")
	}

	verdict, err := Check(allow, usage, incoming)
	if err != nil {
		return admission.Denied(err.Error())
	}
	if !verdict.Allowed {
		return admission.Denied(DenyMessage(team, verdict))
	}
	return admission.Allowed("within the team's tenant quota")
}

// allowanceFor resolves a team's ceiling: its own entry, else `default`.
func (v *ProjectValidator) allowanceFor(ctx context.Context, team string) (Allowance, bool, error) {
	var cm corev1.ConfigMap
	key := client.ObjectKey{Namespace: v.Namespace, Name: TenantQuotaConfigMap}
	if err := v.Client.Get(ctx, key, &cm); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return Allowance{}, false, nil // not opted in
		}
		return Allowance{}, false, err
	}
	raw, ok := cm.Data[team]
	if !ok {
		raw, ok = cm.Data[TenantQuotaDefaultKey]
	}
	if !ok {
		return Allowance{}, false, nil
	}
	a, err := ParseAllowance(raw)
	if err != nil {
		// A typo in one entry must not deny every team.
		return Allowance{}, false, fmt.Errorf("parsing the allowance for %q: %w", team, err)
	}
	return a, true, nil
}

// projectsOfTeam lists the team's existing CompositeProjects.
func (v *ProjectValidator) projectsOfTeam(ctx context.Context, team string) ([]ProjectRequest, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(compositeProjectListGVK)
	if err := v.Client.List(ctx, list); err != nil {
		return nil, err
	}
	var out []ProjectRequest
	for i := range list.Items {
		pr, t, err := projectRequestFrom(&list.Items[i])
		if err != nil || t != team {
			// A sibling we cannot parse is surfaced by SumUsage only if it belongs
			// to this team; one that fails to parse AND has no readable team is
			// not attributable to anyone, so it is skipped rather than guessed at.
			if err != nil && t == team {
				out = append(out, pr) // keep it so SumUsage reports the real problem
			}
			continue
		}
		out = append(out, pr)
	}
	return out, nil
}

// projectRequestFrom pulls the quota-relevant fields off a CompositeProject.
func projectRequestFrom(obj *unstructured.Unstructured) (ProjectRequest, string, error) {
	params, _, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if params == nil {
		return ProjectRequest{}, "", fmt.Errorf("spec.parameters is missing")
	}
	str := func(k string) string {
		s, _, _ := unstructured.NestedString(params, k)
		return s
	}
	name := str("name")
	if name == "" {
		name = obj.GetName()
	}
	pods, _, _ := unstructured.NestedInt64(params, "podQuota")
	return ProjectRequest{
		Name:        name,
		CPUQuota:    str("cpuQuota"),
		MemoryQuota: str("memoryQuota"),
		PodQuota:    pods,
	}, str("team"), nil
}
