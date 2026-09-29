# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.0.0] - 2026-09-29

A rewrite around native Cloud Run v2 request bodies, rendered and validated at build time, with an optional direct deploy. See [docs/migration.md](docs/migration.md).

### Breaking

- `cloudrun_service`, `cloudrun_job`, and `cloudrun_worker` are removed from `//:defs.bzl`. Use `generate_manifest` with `resource_type` set to `service`, `job`, or `worker`. To deploy with Bazel, declare a `cloudrun_deploy` target.
- The configuration schema is now the Cloud Run v2 resource body (`template.containers[].resources.limits`, `scaling`, `vpcAccess`, `valueSource.secretKeyRef`, and so on). The `apphosting.yaml`-style keys are gone: `runConfig`, `env[].variable`/`secret`, `serviceAccount` at the top level, and `cloudsqlConnector`.
- `.render` targets produce JSON (`<name>.render.json`) instead of knative YAML.
- The automatically created per-environment `.deploy` targets (`gcloud run ... replace`) are removed. Deployment is opt-in through `cloudrun_deploy`, and it never pushes images.
- Arrays in overlays replace the base instead of being appended. An array set in both the base and an overlay fails the build.
- `config_format` defaults to `manifest.*.yaml` instead of `apphosting.*.yaml`.
- `image_repo` requires an explicit `image_target`. The digest is read from the target's `digest` output group, not from a derived `:image.digest` target.
- Omitting every image argument no longer renders a placeholder. Set `image = "rules-cloudrun.invalid/override-required:latest"` explicitly.
- `timeout_seconds` is removed. Set `template.timeout` in YAML.
- `project_id`, `region`, and `regions` moved from the manifest rules to `cloudrun_deploy` (`project`, `regions`).
- The minimum Go toolchain is 1.26.4.

### Added

- `generate_manifest` macro and `cloudrun_render` rule. They render deterministic Cloud Run v2 JSON from YAML base and overlay configs, merged following RFC 7396 (mappings merge, arrays replace, `null` deletes).
- Build-time validation against the `cloud.google.com/go/run` (`runpb`) protobuf descriptors: unknown and output-only fields, required fields, oneofs, enums, int32/int64 encodings, and durations. The reviewed field inventory lives in `internal/manifest/testdata/field_inventory.txt`.
- A validation rule catalogue with stable IDs such as `[compute.memory]` and `[probe.startup-budget]`. It covers compute and GPU sizing, probes, containers, environment variables, volumes and mounts, VPC, identity, encryption, traffic splits, and resource-type capabilities. Every rule has a rejecting test case.
- Digest pinning through the rules_img `OutputGroupInfo.digest` of `image_target`. The pinned image's OCI config is checked for Linux amd64 and an executable command.
- `CloudRunManifestInfo` provider (`manifest`, `resource_type`, `resource_name`, `image_container`, and `image_repo`) and the `extract_env_name` helper.
- `cloudrun_deploy` rule, which is optional and never created by `generate_manifest`. It is a `bazel run` target that applies one rendered manifest. The kind and resource name default from `CloudRunManifestInfo`.
- `cloudrun-deploy` binary (`//cmd/cloudrun-deploy`). It applies a rendered manifest through the Cloud Run v2 REST API.
  - Flags: `--manifest`, `--project`, `--region` (repeatable), `--name`, `--kind`, `--image` (digest only), `--image-container`, `--label`, `--rollout-timeout`, and `--validate-only`.
  - It creates or updates the resource, using etag-conditional regional updates and update masks over the writable roots, and it retries conflicts, rate limits, and server errors.
  - It keeps labels and annotations written by other tools, and it waits for the exact generation it wrote to become ready.
  - It prints the generation and the ready revision or latest execution.
- Multi-region Services: two or more regions deploy one managed Service at `locations/global` with `multiRegionSettings`. Jobs and WorkerPools are deployed separately in each region.
- `TestConformance`, an opt-in check that compares local rule verdicts with Cloud Run's regional `validateOnly` endpoint. It is skipped unless `-conformance-project` is set.
- Examples with golden files under `docs/examples` and `tests/fixtures`, plus `docs/validation.md` and `docs/migration.md`.

### Changed

- Rendering is a hermetic Bazel action (`CloudRunRender`). Every flag is passed explicitly, so ambient environment variables and `.env` files cannot change the output. The render path does not depend on the deploy binary or on any Cloud API client.
- Secret references keep their project and require an explicit version. v1 implicitly used `latest`.
- WorkerPools no longer get a request timeout. Jobs never received the old request-timeout default.
- `MODULE.bazel` now depends on `rules_img` 0.3.13. The direct Go dependencies are:
  - `cloud.google.com/go/run` (descriptors only);
  - `google.golang.org/protobuf` and `google.golang.org/genproto/googleapis/api`;
  - `github.com/justinswe/std` and `github.com/spf13/cobra`;
  - `github.com/google/go-containerregistry` (image reference parsing);
  - `k8s.io/apimachinery` (resource quantities);
  - `gopkg.in/yaml.v3`;
  - `golang.org/x/oauth2` (Application Default Credentials for `cloudrun-deploy` only).

### Removed

- The v1 rules and their implementation: `cloudrun/**`, including the knative renderer, `validate.sh`, `common.bzl`, `render.bzl`, and `deploy.bzl`.
- Knative YAML output and the `knative.dev/serving`, `k8s.io/api`, `sigs.k8s.io/yaml`, and `go.uber.org/zap` direct dependencies.
- The `tools/extensions.bzl` module extension and its hermetic `yq`, `skaffold`, and `gcloud_sdk` downloads. Nothing in 2.0 shells out to `gcloud`.
- The `stardoc` dependency and the generated v1 rule docs.
- The stray `base.yaml`, `overlay.yaml`, and `empty.yaml` files at the repository root.

### Migration

- Follow [docs/migration.md](docs/migration.md). It covers the rule and attribute mapping, the configuration key table, and replacing `.deploy` targets with `cloudrun_deploy` or with your own pipeline consuming the rendered JSON.
- **Never send `validateOnly` requests to `locations/global`.** Cloud Run applies them for real to multi-region Services. `cloudrun-deploy --validate-only` is refused for them, and `TestConformance` never sends them.

## [1.0.1] - 2026-03-27

### Added

- Startup, liveness, and readiness probe configuration for services and worker pools. Configuring a probe sets `launch-stage: BETA`.

## [1.0.0] - 2026-02-22

### Added

- `cloudrun_service`, `cloudrun_job`, and `cloudrun_worker` rules. They render knative YAML through a typed Go renderer, with `.render` and `.deploy` targets for each environment.
- Worker pools deploy through `gcloud beta run worker-pools replace`.
- Network and subnet (Direct VPC egress) configuration in manifests.

### Fixed

- `yq` evaluation of configuration YAML files.
- Quiet mode for interactive `gcloud` prompts.

## [0.1.0] - 2025-12-29

### Added

- `cloudrun_deploy` rule that deploys services from `apphosting.yaml` configuration, with examples.
- Cloud Run Job deploy type.
- Multi-region deployment under a single target.
- VPC connector, network, and subnet configuration.

[2.0.0]: https://github.com/justinswe/rules_cloudrun/compare/v1.0.1...v2.0.0
[1.0.1]: https://github.com/justinswe/rules_cloudrun/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/justinswe/rules_cloudrun/compare/v0.1.0...v1.0.0
[0.1.0]: https://github.com/justinswe/rules_cloudrun/releases/tag/v0.1.0
