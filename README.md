# rules_cloudrun

Bazel rules that render native [Cloud Run v2](https://cloud.google.com/run/docs/reference/rest/v2) request bodies for Services, Jobs, and WorkerPools, and validate them at build time.

- `generate_manifest` turns YAML written in Cloud Run v2 field names into deterministic JSON. The build fails on invalid configuration.
- Images are pinned by digest from a [rules_img](https://github.com/bazel-contrib/rules_img) image target.
- `cloudrun_deploy` is optional. It applies a rendered manifest straight to the Cloud Run v2 API with `bazel run`. Rendering never depends on it.

The rendered JSON is the Cloud Run v2 API resource body. Deployment pipelines such as Google Cloud Deploy, or your own CI, can use it with their own tooling.

> **Upgrading from 1.x?** Version 2.0.0 replaces the knative YAML rules (`cloudrun_service`, `cloudrun_job`, and `cloudrun_worker`). See [docs/migration.md](docs/migration.md) and the [CHANGELOG](CHANGELOG.md).

## Install

Add the module to `MODULE.bazel`. It is not in the Bazel Central Registry, so pin it to the release tag with an override.

```starlark
bazel_dep(name = "rules_cloudrun", version = "2.0.0")
git_override(
    module_name = "rules_cloudrun",
    remote = "https://github.com/justinswe/rules_cloudrun.git",
    tag = "v2.0.0",
)

# For digest-pinned images built with rules_img.
bazel_dep(name = "rules_img", version = "0.3.13")
```

`archive_override` works as well. Add an `integrity` value for the archive.

```starlark
archive_override(
    module_name = "rules_cloudrun",
    strip_prefix = "rules_cloudrun-2.0.0",
    urls = ["https://github.com/justinswe/rules_cloudrun/archive/refs/tags/v2.0.0.tar.gz"],
)
```

Rendering and validation run entirely inside Bazel and need no credentials or network access.

## Quick start

### Service

`manifest.yaml` is a Cloud Run v2 `Service` body, minus the name and anything Cloud Run sets itself:

```yaml
ingress: INGRESS_TRAFFIC_ALL
scaling:
  maxInstanceCount: 3
template:
  timeout: 300s
  containers:
    - name: app
      resources:
        limits: { cpu: '1', memory: 512Mi }
        cpuIdle: true
      env:
        - name: LOG_LEVEL
          value: info
        - name: API_KEY
          valueSource:
            secretKeyRef:
              secret: projects/my-project/secrets/API_KEY
              version: '3'
```

`BUILD.bazel`, where `:image` is a rules_img `image_manifest` or `image_index` for `linux/amd64`:

```starlark
load("@rules_cloudrun//:defs.bzl", "generate_manifest")

generate_manifest(
    name = "manifest",
    config = "manifest.yaml",
    image_container = "app",
    image_repo = "us-west1-docker.pkg.dev/my-project/apps/web",
    image_target = ":image",
    resource_name = "web",
)
```

Run `bazel build //web:manifest.render` to write `bazel-bin/web/manifest.render.json`. That file holds the `app` container's image as `us-west1-docker.pkg.dev/my-project/apps/web@sha256:...`, where the digest is read from the `digest` output group of `:image`.

### Jobs and worker pools

Set `resource_type = "job"` for a Job body:

```yaml
template:
  taskCount: 1
  template:
    timeout: 600s
    maxRetries: 3
    containers:
      - name: app
```

Set `resource_type = "worker"` for a WorkerPool body:

```yaml
launchStage: BETA
scaling:
  manualInstanceCount: 1
template:
  containers:
    - name: app
      volumeMounts: [{ name: scratch, mountPath: /tmp/work }]
  volumes:
    - name: scratch
      emptyDir: { medium: MEMORY, sizeLimit: 256Mi }
```

- Deploying a Job updates its definition. It does not start an execution unless the configuration sets `startExecutionToken` or `runExecutionToken`.
- `emptyDir` volumes must use `medium: MEMORY`. `medium: DISK` is rejected until the `cloud.google.com/go/run` descriptors include it.

These three snippets are built by [docs/examples](docs/examples/BUILD.bazel), and `//tests:tests_test` keeps them identical to the files that are built.

### Several environments

```starlark
generate_manifest(
    name = "manifest",
    base_config = "manifest.yaml",
    configs = ["manifest.dev.yaml", "manifest.prd.yaml"],  # manifest_dev.render, manifest_prd.render
    image_container = "app",
    image_repo = "us-west1-docker.pkg.dev/my-project/apps/web",
    image_target = ":image",
    resource_name = "web",
)
```

## Rule reference

`@rules_cloudrun//:defs.bzl` exports:

| Symbol | Kind | Purpose |
| --- | --- | --- |
| `generate_manifest` | macro | Creates one `.render` target for each configuration. |
| `cloudrun_render` | rule | The render action behind `generate_manifest`. It returns `CloudRunManifestInfo`. |
| `CloudRunManifestInfo` | provider | `manifest`, `resource_type`, `resource_name`, `image_container`, and `image_repo` |
| `extract_env_name` | function | Extracts `dev` from `:manifest.dev.yaml` using the `manifest.*.yaml` pattern. |
| `cloudrun_deploy` | rule | Optional. A `bazel run` target that applies one rendered manifest. See [Deploying (optional)](#deploying-optional). |

### `generate_manifest`

| Argument | Default | Description |
| --- | --- | --- |
| `name` | required | With `config`, the target is `<name>.render`. With `configs`, each environment gets `<name>_<env>.render`. |
| `resource_name` | required | The Cloud Run resource ID: 1-49 characters, lowercase letters, digits, and hyphens. |
| `resource_type` | `"service"` | `service`, `job`, or `worker`. |
| `config` / `configs` | none | Set exactly one of these: a single overlay, or one overlay for each environment. |
| `base_config` | none | Shared YAML merged beneath every config. |
| `config_format` | `"manifest.*.yaml"` | A `prefix*suffix` pattern. The `*` part becomes the environment name. |
| `image` | `""` | A literal image reference. Cannot be combined with `image_repo` or `image_target`. |
| `image_repo` + `image_target` | none | Pins `image_repo@<digest of image_target>`. Must be set together. The repository must not include a tag or digest. |
| `image_container` | `""` | The container that receives the image. Required when the body has more than one container. |

Standard attributes such as `visibility` and `tags` are forwarded to every `.render` target.

Image modes:

- **Digest-pinned build:** set `image_repo` and `image_target`. The image must be a rules_img `ImageManifestInfo`, or an `ImageIndexInfo` with exactly one `linux/amd64` variant. Its config is checked for an executable command.
- **Literal:** set `image = "..."`. It works best as an immutable `repo@sha256:` reference.
- **Supplied at deploy time:** set `image = "rules-cloudrun.invalid/override-required:latest"`, then have your deployment replace that container's image. With `cloudrun_deploy`, pass `--image=repo@sha256:...`.
- **From the configuration:** containers can name their own images, for example sidecars.

## Configuration format

- Write Cloud Run v2 JSON field names in YAML: `containers`, `valueSource.secretKeyRef`, `scaling`, `vpcAccess`, `volumes`, and so on. The accepted fields and enum values are listed in the [field inventory](internal/manifest/testdata/field_inventory.txt). See the [Services](https://cloud.google.com/run/docs/reference/rest/v2/projects.locations.services), [Jobs](https://cloud.google.com/run/docs/reference/rest/v2/projects.locations.jobs), and [WorkerPools](https://cloud.google.com/run/docs/reference/rest/v2/projects.locations.workerPools) references.
- Leave out `name` and fields that Cloud Run sets itself (`uid`, `etag`, `conditions`, and so on). The deployment supplies the resource name, project, and region.
- `base_config` merges beneath each overlay following [RFC 7396](https://www.rfc-editor.org/rfc/rfc7396). Mappings merge, and **arrays replace completely**. The build rejects an array that is set in both the base and an overlay, so put `containers` in the overlays only. `null` in an overlay removes an inherited key. `null` in the base is rejected.
- Quote string values, including `"false"`, `"0"`, and CPU values such as `'1'`. YAML merge keys (`<<`), explicit tags, and YAML 1.1 octal numbers are rejected.
- Omitted fields take Cloud Run's API defaults. A Service that sets `resources` must also set `cpuIdle` explicitly: `true` gives request-based billing, `false` gives instance-based billing.
- Secret references keep their project and version. Rendering never reads secret values.

## Validation

Every `.render` action validates configuration as an ordinary Bazel action, so invalid configuration fails `bazel build` and CI without credentials or network access. The checks are:

- Structure, taken from the `cloud.google.com/go/run` protobuf descriptors (`runpb`): unknown fields, output-only fields, required fields, oneofs, enums, integer encodings, and durations.
- A catalogue of rules with stable IDs such as `[compute.memory]` and `[probe.startup-budget]`. They cover CPU and memory combinations, GPUs, probes, containers, environment variables, volumes, networking, identity, and encryption.
- Image checks for the pinned image: Linux amd64, and an executable command.

See [docs/validation.md](docs/validation.md) for the full rule table and its limits.

## Deploying (optional)

`cloudrun_deploy` wraps the `//cmd/cloudrun-deploy` binary. That binary creates or updates the resource through the Cloud Run v2 REST API, waits for the operation, and then waits until the exact generation it wrote is ready. Nothing is deployed unless you declare a `cloudrun_deploy` target and `bazel run` it. `generate_manifest` never creates one.

```starlark
load("@rules_cloudrun//:defs.bzl", "cloudrun_deploy")

cloudrun_deploy(
    name = "deploy_dev",
    labels = {"team": "web"},
    manifest = ":manifest_dev.render",
    project = "my-project-dev",
    regions = ["us-west1"],
)
```

```bash
bazel run //web:deploy_dev                                           # create or update, then wait until ready
bazel run //web:deploy_dev -- --validate-only                        # validateOnly request, single region only
bazel run //web:deploy_dev -- --image=us-west1-docker.pkg.dev/my-project/apps/web@sha256:<64-hex-digest>
```

| Attribute | Description |
| --- | --- |
| `manifest` | A `.render` target. |
| `project` | The Cloud Run project. |
| `regions` | One region deploys a regional resource. For a Service, two or more deploy one multi-region Service at `locations/global` with `multiRegionSettings.regions`. Jobs and WorkerPools are deployed separately in each region. |
| `resource_name`, `kind` | These default to the manifest's `resource_name` and `resource_type`. |
| `labels` | Labels set on the resource. |

Pass extra arguments after `--`. The binary also works without Bazel:

```text
cloudrun-deploy --manifest=manifest.render.json --project=my-project --region=us-west1 \
  --name=web --kind=service [--image=repo@sha256:...] [--image-container=app] \
  [--label=key=value ...] [--rollout-timeout=30m] [--validate-only]
```

- `--kind` is `service`, `job`, or `workerpool` (`worker` is also accepted).
- `--image` replaces the selected container's image, and it must be a `repo@sha256:` digest reference.
- Updates follow the resource's etag and are retried on conflicts, rate limits, and server errors. Labels and annotations written by other tools are kept when the manifest omits their map.
- The binary prints each resource's name, its generation, and its ready revision or latest execution.
- Authentication uses Application Default Credentials.

> [!WARNING]
> **Never send `validateOnly` requests to `locations/global`.** Cloud Run ignores `validateOnly` for multi-region Services: a "validation" request to `locations/global` really creates or updates the Service. `cloudrun-deploy` refuses `--validate-only` when a Service has two or more regions, and the conformance test never sends multi-region bodies. Do not work around this with `gcloud` or raw API calls.

## Development

```bash
bazel build //...
bazel test //...
bazel run //:gazelle
```

`//internal/manifest:manifest_test` includes `TestConformance`. That test compares the local rules with Cloud Run's regional `validateOnly` endpoint, and it is skipped unless you pass `--test_arg=-conformance-project=<project>` (see [docs/validation.md](docs/validation.md)). After you bump `cloud.google.com/go/run`, `manifest_test` fails and logs the new field inventory. Review it, copy it into `internal/manifest/testdata/field_inventory.txt`, and add rules and rule cases for the new fields.

`internal/runapi` and `internal/deploy` are visible only to `//cmd/cloudrun-deploy`, so the render path cannot depend on the API client.

## License

[MIT](LICENSE)
