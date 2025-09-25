# Compositions

Provider specific compositions expand composite resources into concrete managed resources.  Organize compositions by feature and provider, for example:

```
compositions/
  cluster/
    aws-eks.yaml
    gcp-gke.yaml
  storage/
    aws-s3.yaml
    azure-blob.yaml
```

Each composition should include:
- A clear label selector (e.g. `feature: cluster`, `provider: aws`)
- Composition level patches aligning with the XRD schema
- Readiness gates and connection secrets as appropriate
