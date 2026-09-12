"""Golden-path ML training pipeline: ${{values.name}}.

A minimal but genuinely runnable **Kubeflow Pipelines v2** pipeline that trains a
${{values.framework}} model and publishes it, with its metrics, to the platform
artifact store. Compile it and submit it to the platform's Kubeflow Pipelines
(the `data/kubeflow` package):

    python pipelines/pipeline.py                  # writes pipelines/pipeline.yaml
    kfp run create --experiment-name ${{values.modelName}} \
        --package-file pipelines/pipeline.yaml

Two steps, one artifact contract:

    load-data  ──►  train-model  ──►  Model artifact + Metrics
   (Dataset out)   (Model + Metrics out)

Where the model goes
--------------------
KFP v2 artifacts are written to the pipeline root, which on this platform is a
bucket in the **platform MinIO** (`data/minio`); the kubeflow package points KFP
at it (`manifests/platform-minio.yaml`) instead of the broken upstream bundle.
So `Output[Model]` below IS the model registry entry: it is versioned by run,
addressable by URI, and readable by the serving Deployment.

There is deliberately no MLflow call here: **the platform ships no MLflow
package today** (`platform/stack/packages/data/` has no mlflow), so logging to a
tracking server would mean depending on something the platform does not run. If
you add an MLflow deployment, set MLFLOW_TRACKING_URI in
`manifests/configmap.yaml` and uncomment the `mlflow.log_*` calls in
`train_model` — the artifact contract above does not change.
"""

from __future__ import annotations

from kfp import compiler, dsl
from kfp.dsl import Dataset, Input, Metrics, Model, Output

BASE_IMAGE = "python:3.12-slim"
MODEL_NAME = "${{values.modelName}}"


@dsl.component(base_image=BASE_IMAGE, packages_to_install=["scikit-learn==1.5.2", "pandas==2.2.3"])
def load_data(dataset: Output[Dataset]) -> None:
    """Materialise the training set.

    Replace the bundled sample with your real extract — typically a read from the
    platform lakehouse (the `data-pipeline` golden path writes those Iceberg
    tables) or a query against a CompositeDatabase. The rest of the pipeline does
    not care where the frame came from.
    """
    import pandas as pd
    from sklearn.datasets import load_breast_cancer

    bunch = load_breast_cancer(as_frame=True)
    frame = bunch.frame
    frame.to_parquet(dataset.path)
    print(f"wrote {len(frame)} rows to {dataset.path}")


@dsl.component(base_image=BASE_IMAGE, packages_to_install=["scikit-learn==1.5.2", "pandas==2.2.3", "joblib==1.4.2"])
def train_model(
    dataset: Input[Dataset],
    model: Output[Model],
    metrics: Output[Metrics],
    test_size: float = 0.2,
    random_state: int = 42,
) -> None:
    """Train, evaluate, and publish the model artifact."""
    import joblib
    import pandas as pd
    from sklearn.ensemble import RandomForestClassifier
    from sklearn.metrics import accuracy_score, f1_score, roc_auc_score
    from sklearn.model_selection import train_test_split

    frame = pd.read_parquet(dataset.path)
    target = "target"
    features = [c for c in frame.columns if c != target]

    x_train, x_test, y_train, y_test = train_test_split(
        frame[features], frame[target], test_size=test_size, random_state=random_state
    )

    clf = RandomForestClassifier(n_estimators=200, random_state=random_state)
    clf.fit(x_train, y_train)

    predictions = clf.predict(x_test)
    probabilities = clf.predict_proba(x_test)[:, 1]

    metrics.log_metric("accuracy", float(accuracy_score(y_test, predictions)))
    metrics.log_metric("f1", float(f1_score(y_test, predictions)))
    metrics.log_metric("roc_auc", float(roc_auc_score(y_test, probabilities)))
    metrics.log_metric("n_train", float(len(x_train)))

    # The Model artifact is the registry entry: stored under the pipeline root
    # (platform MinIO), versioned per run, and resolvable by URI.
    model.metadata["framework"] = "${{values.framework}}"
    model.metadata["model_name"] = MODEL_NAME
    model.metadata["features"] = features
    joblib.dump({"model": clf, "features": features}, model.path)
    print(f"model written to {model.path} ({model.uri})")

    # With an MLflow tracking server on the platform, this is where a run is
    # logged. Left commented because no mlflow package ships today:
    #   import mlflow
    #   mlflow.set_tracking_uri(os.environ["MLFLOW_TRACKING_URI"])
    #   with mlflow.start_run(run_name=MODEL_NAME):
    #       mlflow.log_metrics({...})
    #       mlflow.sklearn.log_model(clf, artifact_path="model",
    #                                registered_model_name=MODEL_NAME)


@dsl.pipeline(
    name="${{values.name}}-training",
    description="${{values.description}}",
)
def training_pipeline(test_size: float = 0.2, random_state: int = 42):
    data = load_data()
    train_model(
        dataset=data.outputs["dataset"],
        test_size=test_size,
        random_state=random_state,
    )


if __name__ == "__main__":
    compiler.Compiler().compile(
        pipeline_func=training_pipeline,
        package_path="pipelines/pipeline.yaml",
    )
    print("compiled → pipelines/pipeline.yaml")
