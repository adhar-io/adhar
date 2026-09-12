"""Golden-path model server: ${{values.name}}.

A small FastAPI inference service with the same health contract every Adhar
service exposes (`/healthz`, `/readyz` on :8080), so the platform's probes,
HTTPRoute and score cards work unchanged.

The model is the artifact the KFP training pipeline published (`pipelines/`):
it is fetched from the platform object store at startup using the endpoint and
credentials injected by `manifests/configmap.yaml` + the platform MinIO secret.
Until a real run exists, the service reports ready with `model_loaded: false`
and `/predict` answers from a deterministic stub, so the deployment (and its
per-PR preview) is green from the first commit.
"""

from __future__ import annotations

import logging
import os

from fastapi import FastAPI
from pydantic import BaseModel

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("${{values.name}}")

MODEL_NAME = os.environ.get("MODEL_NAME", "${{values.modelName}}")
MODEL_URI = os.environ.get("MODEL_URI", "")
MODEL_VERSION = os.environ.get("MODEL_VERSION", "0.1.0")

app = FastAPI(title="${{values.name}}", version=MODEL_VERSION)

_model = None
_features: list[str] = []


def _load_model():
    """Load the trained artifact from the platform object store, if configured.

    MODEL_URI is the `Output[Model]` URI a KFP run printed (s3://<bucket>/<run>/…).
    Left empty, the service runs in stub mode.
    """
    global _model, _features
    if not MODEL_URI:
        log.info("MODEL_URI unset — serving in stub mode")
        return
    import boto3  # imported lazily so stub mode needs no AWS SDK
    import joblib

    endpoint = os.environ["S3_ENDPOINT"]
    bucket, _, key = MODEL_URI.removeprefix("s3://").partition("/")
    s3 = boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=os.environ["AWS_ACCESS_KEY_ID"],
        aws_secret_access_key=os.environ["AWS_SECRET_ACCESS_KEY"],
    )
    s3.download_file(bucket, key, "/tmp/model.joblib")
    bundle = joblib.load("/tmp/model.joblib")
    _model, _features = bundle["model"], bundle["features"]
    log.info("loaded %s from %s (%d features)", MODEL_NAME, MODEL_URI, len(_features))


@app.on_event("startup")
def startup() -> None:
    try:
        _load_model()
    except Exception:  # noqa: BLE001 — never crash-loop on a bad artifact
        log.exception("model load failed — continuing in stub mode")


class PredictRequest(BaseModel):
    features: list[float]


class PredictResponse(BaseModel):
    prediction: float
    model_name: str
    model_version: str
    model_loaded: bool


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok"}


@app.get("/readyz")
def readyz() -> dict:
    return {"status": "ok", "model_loaded": _model is not None, "model_name": MODEL_NAME}


@app.post("/predict", response_model=PredictResponse)
def predict(req: PredictRequest) -> PredictResponse:
    if _model is None:
        score = sum(req.features) / len(req.features) if req.features else 0.0
    else:
        score = float(_model.predict_proba([req.features])[0][1])
    return PredictResponse(
        prediction=score,
        model_name=MODEL_NAME,
        model_version=MODEL_VERSION,
        model_loaded=_model is not None,
    )
