# rules_cloudrun

Declarative Bazel rules for deploying applications to Google Cloud Run. Generates [Knative Service manifests](https://cloud.google.com/run/docs/reference/rest/v2/projects.locations.services) from YAML configuration files and deploys them with `gcloud run services replace`.

## Overview

| Rule | Purpose |
|------|---------|
| `cloudrun_service` | Generates Knative Service YAML manifests + deploy targets |
| `cloudrun_job` | Generates Cloud Run Job manifests + deploy targets |
| `cloudrun_worker` | Generates Worker Pool manifests + deploy targets |

### How it works

```
apphosting.yaml + apphosting.dev.yaml
       │
       ▼
  ┌─────────────────┐     ┌──────────────────┐
  │ Validate config  │ ──▶ │ Generate manifest │
  └─────────────────┘     └──────────────────┘
                                   │
                         ┌─────────┴─────────┐
                         ▼                   ▼
                  myapp_dev.render     myapp_dev.deploy
                  (Knative YAML)       (gcloud replace)
```

1. **Validate** — Strict schema validation at build time (catches typos, bad ranges, missing fields)
2. **Render** — Produces manifests via a typed Go resource renderer (Knative API types for services)
3. **Deploy** — Runs `gcloud run services replace <manifest>` against the target project

## Quick Start

### 1. Add to MODULE.bazel

```starlark
git_override(
    module_name = "rules_cloudrun",
    remote = "https://github.com/justinswe/rules_cloudrun.git",
    commit = "RELEASE_SHA",
)
```

### 2. Define a service

```starlark
load("@rules_cloudrun//:defs.bzl", "cloudrun_service")

cloudrun_service(
    name = "myapp",
    service_name = "myapp",
    image = "gcr.io/my-project/myapp:latest",
    region = "us-central1",
    config = ":apphosting.yaml",
)
```

### 3. Build and deploy

```bash
# Render the Knative manifest
bazel build //:myapp.render

# Deploy to Cloud Run
bazel run //:myapp.deploy
```

### Digest-pinned image targets

Use `image_repo` with an OCI image target to get deterministic `repo@sha256:...` manifests:

```starlark
cloudrun_service(
    name = "myapp",
    service_name = "myapp",
    config = ":apphosting.yaml",
    region = "us-central1",
    image_repo = "us-central1-docker.pkg.dev/my-project/my-repo/myapp",
    image_target = ":image",  # optional, defaults to :image
)
```

In image-target mode, rules derive `:image.push` and `:image.digest`, execute the push binary directly from runfiles (no nested `bazel run`), and render the manifest image as `image_repo@sha256:...`.
This behavior is supported for `cloudrun_service`, `cloudrun_job`, and `cloudrun_worker`.

You can also override the image at deploy runtime for promotion workflows:

```bash
SKIP_PUSH=1 bazel run //:myapp_prd.deploy -- \
  --image us-central1-docker.pkg.dev/lavndr-ai/lavndr-ai/lavndrapi@sha256:46e099f6d3eab8fc3246ad867aace15a5503a4cf6c7c54f2ffac5c28ea1facad
```

The runtime `--image` flag must be a fully qualified digest reference (`repo@sha256:...`).
When provided, the deployer rewrites the manifest image in a temporary file and deploys that pinned image, which is useful for promoting a tested dev digest into prod without rebuilding.

You may omit both `image` and `image_repo` in rule definitions when your workflow always provides `--image` at deploy time.
In that mode, a deterministic placeholder image is rendered and expected to be overridden at runtime.

## Multi-Environment Deployments

The primary use case: deploy the same image to multiple environments with different resource, secret, and scaling configurations.

### Configuration files

**apphosting.yaml** — base (shared defaults):
```yaml
runConfig:
  minInstances: 0
  maxInstances: 3
  concurrency: 1000
  cpu: 1
  memoryMiB: 512

env:
  - variable: LOG_LEVEL
    value: info
```

**apphosting.dev.yaml** — dev overrides:
```yaml
runConfig:
  maxInstances: 2

env:
  - variable: API_KEY
    secret: projects/123456789/secrets/API_KEY
  - variable: ENVIRONMENT
    value: development

serviceAccount: myapp-dev@my-project-dev.iam.gserviceaccount.com
```

**apphosting.prd.yaml** — production overrides:
```yaml
runConfig:
  cpu: 2
  memoryMiB: 2048
  minInstances: 1
  maxInstances: 10

env:
  - variable: API_KEY
    secret: projects/987654321/secrets/API_KEY
  - variable: ENVIRONMENT
    value: production

serviceAccount: myapp-prd@my-project-prd.iam.gserviceaccount.com
cloudsqlConnector: my-project-prd:us-central1:myapp-db
```

### BUILD.bazel

```starlark
load("@rules_cloudrun//:defs.bzl", "cloudrun_service")

cloudrun_service(
    name = "myapp",
    service_name = "myapp",
    image = "gcr.io/my-project/myapp:latest",
    region = "us-central1",
    base_config = ":apphosting.yaml",
    configs = [":apphosting.dev.yaml", ":apphosting.prd.yaml"],
    project_id = "my-project-{}-00",
)
```

### Generated targets

| Target | Description |
|--------|-------------|
| `myapp_dev.render` | Build: generates Knative YAML for dev |
| `myapp_dev.deploy` | Run: deploys to `my-project-dev-00` |
| `myapp_prd.render` | Build: generates Knative YAML for prd |
| `myapp_prd.deploy` | Run: deploys to `my-project-prd-00` |

Environment names are auto-extracted from filenames: `apphosting.dev.yaml` → `dev`, `apphosting.prd.yaml` → `prd`.

The `{}` placeholder in `project_id` is replaced with the environment name.

### Deploy

```bash
bazel run //:myapp_dev.deploy
bazel run //:myapp_prd.deploy

# Pass extra gcloud flags at runtime
bazel run //:myapp_prd.deploy -- --quiet
```

### Discover all deploy targets

```bash
bazel query 'attr(tags, cloudrun_deploy, //...)'
```

---

## Configuration Schema Reference

The YAML configuration format is validated at build time. Invalid configurations **fail the build** with a detailed error message listing every violation.

### Top-Level Keys

| Key | Type | Description |
|-----|------|-------------|
| `runConfig` | object | Resource and scaling configuration |
| `env` | list | Environment variables and secrets |
| `serviceAccount` | string | IAM service account email |
| `cloudsqlConnector` | string | Cloud SQL instance (`project:region:instance`) |

Any other top-level key will fail validation.

### `runConfig` (service)

| Field | Type | Constraint | Default | Description |
|-------|------|-----------|---------|-------------|
| `cpu` | int | `1`, `2`, `4`, or `8` | `1` | vCPU allocation |
| `memoryMiB` | int | `128` – `32768` | `512` | Memory in MiB |
| `minInstances` | int | ≥ 0 | `0` | Minimum instances |
| `maxInstances` | int | ≥ 1 | `3` | Maximum instances |
| `concurrency` | int | ≥ 1 | `1000` | Requests per instance |
| `network` | string | — | — | VPC network name |
| `subnet` | string | — | — | VPC subnet (requires `network`) |
| `vpcConnector` | string | — | — | Serverless VPC connector |
| `vpcEgress` | string | — | — | VPC egress setting |

> **Co-dependency**: `network` and `subnet` must both be specified together.

### `runConfig` (job)

| Field | Type | Constraint | Description |
|-------|------|-----------|-------------|
| `cpu` | int | `1`, `2`, `4`, or `8` | vCPU allocation |
| `memoryMiB` | int | `128` – `32768` | Memory in MiB |
| `taskCount` | int | ≥ 1 | Number of tasks |
| `parallelism` | int | ≥ 1 | Parallel task execution |
| `maxRetries` | int | ≥ 0 | Max retries per task |
| `timeoutSeconds` | int | ≥ 1 | Task timeout |

### `runConfig` (worker)

Same as **service** minus `concurrency`.

### `env` entries

Each entry must have `variable` and exactly one of `value` or `secret`:

```yaml
env:
  # Plain value
  - variable: LOG_LEVEL
    value: info

  # Secret Manager reference
  - variable: API_KEY
    secret: projects/123456789/secrets/API_KEY
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `variable` | string | ✅ | Environment variable name |
| `value` | string | ✅ (or `secret`) | Literal value |
| `secret` | string | ✅ (or `value`) | `projects/<num>/secrets/<name>` |
| `availability` | string | — | Optional availability scope |

**Mutually exclusive**: specifying both `value` and `secret` on the same entry is an error.

**Secret format**: must match `projects/<project-number>/secrets/<secret-name>`.

### `serviceAccount`

Must be a valid email format: `name@project.iam.gserviceaccount.com`

### `cloudsqlConnector`

Connection string format: `project:region:instance`

---

## Validation Errors

Invalid configurations are caught at **build time** and produce clear, actionable error messages. Example:

```
ERROR: Config validation failed for 'apphosting.dev.yaml' (resource_type=service):
  - Unknown top-level key 'unknownKey'. Allowed: runConfig env serviceAccount cloudsqlConnector
  - env 'MY_VAR' has both 'value' and 'secret' — must have exactly one
  - runConfig.cpu must be 1, 2, 4, or 8, got '3'
  - runConfig.memoryMiB must be 128–32768, got '64'
  - serviceAccount must be a valid email, got 'bad-account'
```

All violations are reported at once — the validator does not stop at the first error.

---

## Rule Reference

### `cloudrun_service`

```starlark
cloudrun_service(
    name,              # target base name
    service_name,      # Cloud Run service name
    image = "",        # optional container image URL
    region,            # GCP region
    config = None,     # single config file (Label)
    base_config = None,# base config for multi-env (Label)
    configs = [],      # list of env overlay configs (list[Label])
    config_format = "apphosting.*.yaml",  # pattern for env extraction
    project_id = "",   # project ID template (use {} for env name)
)
```

### `cloudrun_job`

Same interface as `cloudrun_service`, generates Cloud Run Job manifests.

### `cloudrun_worker`

Same interface as `cloudrun_service`, generates Worker Pool manifests.

---

## Manifest Output

The generated Knative YAML follows the [Cloud Run Service YAML schema](https://cloud.google.com/run/docs/reference/rest/v2/projects.locations.services):

```yaml
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: myapp
  labels:
    cloud.googleapis.com/location: us-central1
  annotations:
    run.googleapis.com/ingress: all
spec:
  template:
    metadata:
      annotations:
        run.googleapis.com/execution-environment: gen2
        autoscaling.knative.dev/minScale: "0"
        autoscaling.knative.dev/maxScale: "10"
        run.googleapis.com/cloudsql-instances: project:region:instance
    spec:
      timeoutSeconds: 300
      containers:
        - image: gcr.io/my-project/myapp:latest
          resources:
            limits:
              cpu: 1000m
              memory: 512Mi
          env:
            - name: LOG_LEVEL
              value: info
            - name: API_KEY
              valueFrom:
                secretKeyRef:
                  name: API_KEY
                  key: latest
      containerConcurrency: 1000
      serviceAccountName: myapp@proj.iam.gserviceaccount.com
```

## Examples

See [docs/examples](docs/examples) for complete working examples.
