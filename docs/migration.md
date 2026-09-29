# Migrating from rules_cloudrun 1.x to 2.0

Version 2.0.0 is a rewrite, and it is not backward compatible.

1.x rendered knative-style YAML from an `apphosting.yaml`-like schema, and it deployed with `gcloud run ... replace` through per-environment `.deploy` targets. 2.0 renders native Cloud Run v2 JSON request bodies from configuration that uses v2 field names, and it validates them at build time. Rendering is the main job. Deployment is optional: declare a `cloudrun_deploy` target, or feed the rendered JSON to your own pipeline.

No Cloud Run resource needs to be deleted or recreated. `cloudrun-deploy` updates the existing Services, Jobs, and WorkerPools in place.

## 1. Update the dependency

```starlark
bazel_dep(name = "rules_cloudrun", version = "2.0.0")
git_override(
    module_name = "rules_cloudrun",
    remote = "https://github.com/justinswe/rules_cloudrun.git",
    tag = "v2.0.0",
)
bazel_dep(name = "rules_img", version = "0.3.13")  # only needed for digest-pinned image targets
```

rules_cloudrun no longer downloads `yq`, `skaffold`, or a Google Cloud SDK. `cloudrun-deploy` calls the Cloud Run v2 API directly with Application Default Credentials.

## 2. Replace the rules

| 1.x | 2.0 |
| --- | --- |
| `load("@rules_cloudrun//:defs.bzl", "cloudrun_service")` | `load("@rules_cloudrun//:defs.bzl", "generate_manifest", "cloudrun_deploy")` |
| `cloudrun_service(service_name = "x", ...)` | `generate_manifest(resource_name = "x", resource_type = "service", ...)` |
| `cloudrun_job(job_name = "x", ...)` | `generate_manifest(resource_name = "x", resource_type = "job", ...)` |
| `cloudrun_worker(worker_name = "x", ...)` | `generate_manifest(resource_name = "x", resource_type = "worker", ...)` |
| `config`, `configs`, `base_config` | Unchanged. The files use the v2 schema (section 3). |
| `config_format = "apphosting.*.yaml"` (default) | The default is now `"manifest.*.yaml"`. To keep your file names, set `config_format = "apphosting.*.yaml"`. |
| `<name>.render`, `<name>_<env>.render` (YAML) | Same target names. The output is JSON: `<name>.render.json`. |
| `<name>.deploy`, `<name>_<env>.deploy` (created automatically) | Declare one `cloudrun_deploy(manifest = ":<name>_<env>.render", ...)` for each destination you deploy with Bazel. |
| `project_id = "proj-{}"` | `cloudrun_deploy(project = "proj-dev")`, one for each environment |
| `region`, `regions` | `cloudrun_deploy(regions = [...])` |
| `image = "repo:tag"` | Unchanged. |
| `image_repo = "..."` with an implicit `image_target = ":image"` | Set `image_repo` and `image_target` together, explicitly. The digest comes from the image target's `digest` output group (rules_img). There is no `:image.digest` naming convention. |
| `:image.push` run by `.deploy` | Removed. Push the image yourself (for example `bazel run :image.push`) before deploying. The manifest already pins its digest. |
| No image: automatic placeholder | Set `image = "rules-cloudrun.invalid/override-required:latest"` explicitly and pass `--image` when you deploy. |
| `timeout_seconds = 300` | Put it in YAML: `template.timeout: 300s` for Services. Jobs use `template.template.timeout`. WorkerPools have no request timeout. |
| `bazel run :x_prd.deploy -- --image=repo@sha256:...` | `bazel run :deploy_prd -- --image=repo@sha256:...` |
| `SKIP_PUSH=1` | Removed. `cloudrun_deploy` never pushes images. |
| Extra `gcloud` flags after `--` | Pass `cloudrun-deploy` flags after `--`, such as `--validate-only`, `--image`, `--label`, and `--rollout-timeout`. |
| Tag `cloudrun_deploy` for `bazel query` | Removed. Query `kind(cloudrun_deploy, //...)` instead. |

## 3. Convert the configuration

v2 configuration is a Cloud Run v2 resource body (see the [README](../README.md#configuration-format)). The table below uses Service paths. For Jobs, containers, `serviceAccount`, `volumes`, `vpcAccess`, `timeout`, and `maxRetries` live under `template.template`.

| 1.x key | 2.0 field |
| --- | --- |
| `runConfig.cpu: 1` | `template.containers[].resources.limits.cpu: '1'` |
| `runConfig.memoryMiB: 512` | `template.containers[].resources.limits.memory: 512Mi` |
| implicit request-based CPU | `template.containers[].resources.cpuIdle: true`. This is required whenever a Service sets `resources`. |
| `runConfig.minInstances` / `maxInstances` (Service) | `scaling.minInstanceCount` / `scaling.maxInstanceCount` |
| `runConfig.minInstances` / `maxInstances` (worker) | `scaling.manualInstanceCount` |
| `runConfig.concurrency` | `template.maxInstanceRequestConcurrency` |
| `runConfig.network` + `subnet` | `template.vpcAccess.networkInterfaces: [{network: ..., subnetwork: ...}]` |
| `runConfig.vpcConnector` | `template.vpcAccess.connector` |
| `runConfig.vpcEgress` | `template.vpcAccess.egress` (`ALL_TRAFFIC` or `PRIVATE_RANGES_ONLY`) |
| `runConfig.livenessProbe` / `readinessProbe` / `startupProbe` | `template.containers[].livenessProbe` and so on, in v2 probe syntax. `readinessProbe.successThreshold` is not supported. |
| probes implied `launch-stage: BETA` | Set `launchStage: BETA` yourself when a feature needs it. |
| `runConfig.taskCount` / `parallelism` (job) | `template.taskCount` / `template.parallelism` |
| `runConfig.maxRetries` / `timeoutSeconds` (job) | `template.template.maxRetries` / `template.template.timeout: 600s` |
| `env: [{variable: X, value: v}]` | `template.containers[].env: [{name: X, value: v}]`. Quote numbers and booleans. |
| `env: [{variable: X, secret: projects/N/secrets/S}]` | `env: [{name: X, valueSource: {secretKeyRef: {secret: projects/N/secrets/S, version: latest}}}]`. The version is now explicit. |
| `env[].availability` | No equivalent. It never affected rendering, so drop it. |
| `serviceAccount` | `template.serviceAccount` |
| `cloudsqlConnector: p:r:i` | `template.volumes: [{name: cloudsql, cloudSqlInstance: {instances: [p:r:i]}}]` plus a `volumeMounts: [{name: cloudsql, mountPath: /cloudsql}]` on the container |
| implicit `ingress: all` | `ingress: INGRESS_TRAFFIC_ALL` (also the API default) |
| implicit `execution-environment: gen2` | `template.executionEnvironment: EXECUTION_ENVIRONMENT_GEN2` |
| implicit `timeoutSeconds: 300` (Service) | `template.timeout: 300s` |

Merging changed as well. 1.x appended `env` lists from the base and the overlay. In 2.0, arrays replace completely, and the build rejects an array that is set in both files. Put each environment's complete `containers` list in its overlay, and keep only mappings such as `scaling` and `ingress` in the base. Duplicate environment variable names are rejected.

Build the new manifests. Then compare them with the live resources (`gcloud run services describe ... --format=json`) before the first rollout.

## 4. Deploy

For each destination, declare a deployment:

```starlark
cloudrun_deploy(
    name = "deploy_prd",
    manifest = ":manifest_prd.render",
    project = "my-project-prd",
    regions = ["us-central1"],
)
```

1. Build the manifests and review the diff against the live resource.
2. Check with Cloud Run without deploying: `bazel run //pkg:deploy_prd -- --validate-only` (regional resources only; see below).
3. Deploy with `bazel run //pkg:deploy_prd`. The command waits until the new generation is ready, then prints the ready revision.
4. To roll back, deploy the previous manifest, or pass the previous digest with `--image=repo@sha256:...`.

If you deploy through a pipeline instead, such as Google Cloud Deploy or your own CI, consume `<name>.render.json` from `bazel build` with that pipeline's tooling. The JSON is a Cloud Run v2 API resource body. It is not input for `gcloud run services replace`, which expects the v1 knative YAML.

## Multi-region Services

1.x deployed `regions = [...]` Services with `gcloud run multi-region-services replace`. In 2.0, `cloudrun_deploy(regions = [...])` with two or more regions deploys a Service as one managed multi-region Service at `locations/global`, with `multiRegionSettings.regions`. Jobs and WorkerPools are deployed separately in each region.

> [!WARNING]
> Cloud Run ignores `validateOnly` at `locations/global`: a "validation" request really creates or updates the Service. `cloudrun-deploy` refuses `--validate-only` for multi-region Services. Do not send such a request yourself while you test the migration.

## Behavior changes to review

- Invalid configuration now fails the build: unknown or output-only fields, invalid unions, invalid resource combinations, missing `cpuIdle`, and unresolved image placeholders.
- Empty environment values are kept. Cross-project secret references keep their project identifier.
- WorkerPools no longer get a request timeout. The disk-backed `emptyDir` medium (`medium: DISK`) is not yet in the `cloud.google.com/go/run` descriptors, so use `medium: MEMORY`.
- Service and WorkerPool updates mask the writable roots present in either the live or the desired resource, so settings you omit are cleared. Job updates replace the whole Job.
- Labels and annotations written by other tools are kept when you omit their map. An explicit map manages that map, and `{}` clears it. Reserved v1 metadata namespaces (`run.googleapis.com/`, `serving.knative.dev/`, and similar) are rejected in configuration and are not copied into v2 requests.
- A deploy waits for the exact generation it wrote to become ready, so an older ready revision cannot satisfy it.
