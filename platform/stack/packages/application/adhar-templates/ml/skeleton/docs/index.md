# Golden Path: ML Service

A production-shaped starting point for a machine-learning service scaffolded by
the Adhar platform: Kubeflow Pipelines for training, the platform object store
for artifacts, and a hardened FastAPI server behind the Gateway API for serving.

```
pipelines/ (KFP v2, ${{values.framework}}) -> Output[Model] in platform MinIO -> serving/ (FastAPI) -> HTTPRoute
```

- **Training** — `pipelines/pipeline.py`: a KFP v2 pipeline with a `load_data`
  component and a `train_model` component that emits `Output[Model]` and
  `Output[Metrics]`. `manifests/cronjob.yaml` submits it to the platform's
  Kubeflow Pipelines on the `${{values.trainingSchedule}}` schedule.
- **Model store** — KFP v2 artifacts land in the platform MinIO (the kubeflow
  package repoints KFP at it), so the run's `Output[Model]` URI is what the
  server loads. **No MLflow package ships with the platform today**; experiment
  metadata therefore lives in KFP. See the README for the one-file change if you
  add MLflow.
- **Serving** — `serving/main.py`: `/predict`, `/healthz`, `/readyz` on :8080,
  deployed by `manifests/` as a non-root Deployment with probes, requests and
  limits, exposed through the platform Cilium Gateway. `serving/kserve-inferenceservice.yaml`
  is the opt-in KServe replacement.
- **CI** — `.adhar/app.yaml` + the platform Tekton `app-ci` pipeline
  (supply-chain package): build → scan → sign → Harbor (`:latest` and
  `:<commit sha>`) → GitOps writeback.
- **Previews** — `preview/kustomization.yaml` opts the repo into per-PR preview
  environments (ADR-0017): label a PR `preview` to get
  `https://${{values.name}}-pr-<number>.adhar.localtest.me:8443`.

Once synced by ArgoCD the model API answers at
`https://${{values.name}}.adhar.localtest.me:8443`. See `README.md` for how to run
it locally.

### adhar

Checkout adhar website: https://adhar.io

Checkout adhar repository: https://github.com/adhar-io/adhar
