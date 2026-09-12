# ${{values.name}} — Golden Path: ML Service

${{values.description}}

A production-shaped starting point for a model on the Adhar platform: training is
a **Kubeflow Pipelines v2** pipeline, serving is a hardened FastAPI Deployment
behind the platform Gateway, and both halves are wired to platform capabilities
rather than bundling their own.

```
  pipelines/            serving/              manifests/
  KFP v2 pipeline  ──►  model artifact  ──►   Deployment + HTTPRoute
  (${{values.framework}})  (platform MinIO)      https://${{values.name}}.adhar.localtest.me:8443
```

- **Train** — `pipelines/pipeline.py` is a two-component KFP v2 pipeline
  (`load_data` → `train_model`) that trains a ${{values.framework}} model and emits
  an `Output[Model]` plus `Output[Metrics]`. Submit it to the platform's Kubeflow
  Pipelines (`data/kubeflow`); `manifests/cronjob.yaml` submits it on the
  `${{values.trainingSchedule}}` schedule.
- **Store** — KFP v2 writes artifacts to the pipeline root, which this platform
  points at the shared **MinIO** (`data/minio`, see the kubeflow package's
  `platform-minio.yaml`). So the `Output[Model]` **is** the registry entry:
  versioned per run, addressable by URI, loadable by the server.
- **Serve** — `serving/main.py` fetches that artifact at startup and answers
  `POST /predict`, with the platform's standard `/healthz` + `/readyz` contract on
  `:8080`. With `MODEL_URI` unset it serves a deterministic stub, so the
  deployment (and every PR preview) is green from the first commit.

## Where is MLflow?

**The platform ships no MLflow package today** — `platform/stack/packages/data/`
has no `mlflow`, so this skeleton does not depend on one. Experiment metadata
lives in KFP (runs, parameters, `Output[Metrics]`) and model artifacts live in the
platform object store. If you deploy MLflow, the change is small and additive:
set `MLFLOW_TRACKING_URI` in `manifests/configmap.yaml` and uncomment the
`mlflow.log_*` / `mlflow.sklearn.log_model` calls at the bottom of
`train_model` — nothing else moves.

## What gets deployed

`manifests/` (synced by ArgoCD via `.adhar/app.yaml`):

- `namespace.yaml` — the service namespace, labelled for ADR-0023 placement.
- `configmap.yaml` — model name/URI, the platform MinIO endpoint and the KFP API
  endpoint. Nothing about the platform is baked into the image.
- `deployment.yaml` + `service.yaml` — the hardened model server (non-root,
  dropped capabilities, probes, requests + limits).
- `httproute.yaml` — the API through the platform Cilium Gateway.
- `cronjob.yaml` — scheduled retraining: submits the compiled pipeline to KFP.

`serving/kserve-inferenceservice.yaml` is the opt-in KServe alternative — not
synced, because the platform ships no KServe package; see the file's header for
the swap.

## Run it locally

```bash
python -m venv .venv && source .venv/bin/activate
pip install -r pipelines/requirements.txt -r serving/requirements.txt

# 1. Compile the training pipeline
python pipelines/pipeline.py            # → pipelines/pipeline.yaml

# 2. Submit it to the platform (port-forward or in-cluster DNS)
export KFP_ENDPOINT=http://ml-pipeline.adhar-system.svc:8888
kfp --endpoint "$KFP_ENDPOINT" run create \
    --experiment-name ${{values.modelName}} \
    --package-file pipelines/pipeline.yaml

# 3. Serve (stub mode; set MODEL_URI to serve the trained artifact)
uvicorn serving.main:app --reload --port 8080
curl -s localhost:8080/readyz
curl -s -XPOST localhost:8080/predict -H 'content-type: application/json' \
     -d '{"features":[0.1,0.2,0.3]}'
```

## CI and previews

`.adhar/app.yaml` + the platform Tekton `app-ci` pipeline (supply-chain package):
a push to `main` builds `serving/` with buildpacks, scans it (trivy), signs it
(cosign), pushes it to Harbor under `:latest` **and** `:<commit sha>`, and writes
the revision back so ArgoCD rolls the Deployment.

`preview/kustomization.yaml` opts this repo into **per-PR preview environments**
(ADR-0017): label a pull request `preview` and the platform builds its head
commit and deploys it at

```
https://${{values.name}}-pr-<number>.adhar.localtest.me:8443
```

in its own namespace, updated on every push and destroyed when the PR closes.

### adhar

Checkout adhar website: https://adhar.io

Checkout adhar repository: https://github.com/adhar-io/adhar
