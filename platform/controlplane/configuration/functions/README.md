# Crossplane Functions

This directory will host Crossplane Functions used by compositions.  Functions allow injecting procedural logic into the reconciliation pipeline, enabling advanced templating, policy checks, or data enrichment that cannot be achieved with static patches.

Each function should declare its runtime contract and be referenced by compositions via the `pipeline` field once implemented.
