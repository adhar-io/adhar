# ADR-0027: Functions on Knative (Serving + Eventing), OpenFunction Removed

- **Status**: Accepted
- **Date**: 2026-10-25
- **Owners**: Platform Engineering
- **Supersedes**: Any prior platform guidance that depended on OpenFunction

## Context

Adhar needs a durable Functions experience that stays aligned with the rest of
the platform:

- Declarative through GitOps
- Consistent with Gateway API and Keycloak SSO patterns
- Compatible with the existing `Function` Crossplane API
- Maintainable without depending on unmaintained projects

OpenFunction has seen little upstream activity and duplicated capability that is
already available in Knative. Keeping both increased operational and
documentation overhead without adding enough value.

## Decision

Adhar standardizes Functions on **Knative only**:

1. Use **Knative Serving** for HTTP function runtime (scale-to-zero, revisions,
   traffic splitting).
2. Use **Knative Eventing** for event-driven triggers and delivery semantics.
3. Keep the `Function` platform API as the user-facing contract and reconcile it
   to Knative resources.
4. Remove OpenFunction package wiring, manifests, and related references from
   the stack.
5. Ship and maintain function templates aligned to Knative deployment shape
   (Java and Node.js templates included).

## Consequences

### Positive

- Smaller platform surface area and lower maintenance burden.
- Clearer operator/developer mental model (one runtime path).
- Better alignment with active upstream projects and ecosystem tooling.
- Easier security and lifecycle management through fewer components.

### Trade-offs

- OpenFunction-specific abstractions are no longer available.
- Any prior OpenFunction examples must migrate to Knative-native manifests.

## Implementation Notes

- Package wiring excludes OpenFunction from local, production, and gitops appsets.
- Environment package catalogs match the same removal.
- Function-related docs and templates reflect Knative-first usage.

## Verification

- Stack wiring invariants and appset/environment parity tests pass.
- Package-path validation confirms no OpenFunction package dependency remains.
