# Build-time validation

- Every `.render` action validates input structure before merging, then validates the final native v2 configuration. Null removal cannot hide unknown or output-only fields.
- Structure comes from the `cloud.google.com/go/run` protobuf descriptors pinned in `go.mod`: JSON field names, REQUIRED and OUTPUT_ONLY annotations, unions (oneofs), enum names, int32/int64 encodings, and Duration syntax. `internal/manifest/testdata/field_inventory.txt` records the fields and enum values; a descriptor bump fails the test until the inventory is reviewed.
- Every failure carries a stable rule ID, for example `[compute.memory]`. `manifest_test` requires one rejecting case per rule ID found in the sources, so no rule can exist without a test.
- `cloudrun-deploy` repeats validation after binding a manifest to its destination: resource identity, region list, and selected image container. Name conflicts and regional references pointing at another region fail before any request is sent.
- Bazel-built images must provide an OCI manifest/config (or an index containing Linux amd64), target Linux amd64, and supply an executable command through the image or container configuration.
- Golden tests compare JSON content, scalar types, and array order. Repository formatting cannot change their meaning.

## Runtime configuration checks

| Area                    | Build-time checks                                                                                                                                                                                                                                                                                             |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Resource capabilities   | Jobs reject liveness/readiness probes; workers reject readiness probes; non-service request ports and CPU throttling are rejected.                                                                                                                                                                            |
| Configuration layering  | Arrays set in both the base and an overlay, nulls in the base or with nothing to delete, YAML merge keys (`<<`), explicit YAML tags, YAML 1.1 octal numbers such as `0644`, `etag`, and services whose `resources` omit `cpuIdle`.                                                                            |
| Execution environment   | First generation rejects Jobs, worker pools, GPUs, and second-generation volume types; first-generation services may use sidecars. Services with sidecars need exactly one container with ports.                                                                                                              |
| Compute                 | CPU/memory combinations, sub-1-vCPU rules applied to the instance total, L4 minimum resources, RTX PRO 6000 sizes (20-30 even vCPU, 80 GiB up to 4 GiB per vCPU), GPU container counts, billing mode, and GPU Job constraints.                                                                                |
| Probes                  | One probe action, protocol restrictions, port/range limits, header names, and startup budgets (600 s, or 1800 s when the instance has a GPU). Startup timing fields are at most 240 s; liveness up to 3600 s; readiness timeout up to 300 s with no initial delay. Zero selects the API default.              |
| Containers              | Unique names (RFC 1123, periods allowed), dependencies without cycles, 1000 environment variables and 1000 arguments per resource, reserved names (`PORT` everywhere, `K_*` for services, `CLOUD_RUN_*` for jobs), NUL rejection, absolute working directories, port numbers, and image-reference syntax.     |
| Storage and networking  | Volume unions and references, normalized mount collisions, restricted paths, secret references and required item versions, the reserved `cloudsql` volume name, Cloud SQL connection names, NFS/GCS requirements, connector paths, multi-region services with at least two regions, and region compatibility. |
| Identity and encryption | Resource/revision IDs, launch stages (ALPHA, BETA, GA), Job execution-token lengths, service-account email or account ID, KMS resource names, and key-dependent settings.                                                                                                                                     |

Sources: [Services](https://docs.cloud.google.com/run/docs/reference/rest/v2/projects.locations.services), [Jobs](https://docs.cloud.google.com/run/docs/configuring/jobs/healthchecks), [Worker probes](https://docs.cloud.google.com/run/docs/configuring/workerpools/healthchecks), [GPU resources](https://docs.cloud.google.com/run/docs/configuring/services/gpu), [Container contract](https://docs.cloud.google.com/run/docs/container-contract).

## Conformance with Cloud Run

`TestConformance` sends every rejecting rule case and every accepted fixture to Cloud Run's regional `validateOnly` create endpoint. Accepted fixtures must return HTTP 200. Each rejected case must return HTTP 400 with the message substring recorded in `serverReasons`. That way, a case rejected for an unrelated reason fails. Run it after changing rules or bumping `cloud.google.com/go/run`, against a project where you may create Cloud Run resources:

```bash
bazel test //internal/manifest:manifest_test --test_filter=TestConformance \
  --test_arg=-conformance-project=<project> --test_env=HOME --spawn_strategy=local --test_output=all
```

`TestConformanceTablesCoverRules` checks offline that every rule case is classified in exactly one table:

- `serverReasons`: Cloud Run enforces the rule, and the value is part of its rejection message.
- `policyRules`: deliberately stricter than `validateOnly`. Examples are NUL bytes, duplicate environment names, `..` in secret item paths, and explicit `cpuIdle`.
- `localOnlyRules`: a regional create cannot exercise the rule. Examples are resource names and KMS-dependent settings.

> [!WARNING]
> Cloud Run ignores `validateOnly` for multi-region services: a request to `locations/global` creates or updates the service. The test never sends bodies with `multiRegionSettings`, and multi-region rules are local-only. `cloudrun-deploy` refuses `--validate-only` for them. Without `-conformance-project` the test is skipped, so normal CI stays offline.

## Normal build and CI

- Validation runs inside `CloudRunRender`. A failed check makes the action exit with an error, so Bazel cannot produce a manifest from invalid configuration.
- Your existing `bazel build //...` or `bazel test //...` CI command builds these targets. Configuration validation needs no extra CI step, cloud credentials, or network access.

```bash
bazel build //web:manifest_dev.render
bazel test //...
```

## Limits of these checks

- Build-time validation checks the configuration and the OCI image metadata that is available. Cloud state is checked by the provider at deployment: IAM grants, secret versions, network existence, organization policy, capacity, and regional quota.
- Builds do not start containers, invoke probes, execute Jobs, or prove business behavior. Application startup and external dependencies need runtime tests.
- IAM, quotas, dependencies, and platform state can change after a merge. Deployment therefore validates again, and it waits for the exact completed generation to reconcile before it reports success.
- New API capabilities require updates to the runpb descriptors, the runtime policy, and the rule cases.
- The Go descriptors trail the REST API. The following are rejected until `cloud.google.com/go/run` publishes them: `sshEnabled`, `functionalType`, scaling `cpuUtilization` and `concurrencyUtilization`, `delayExecution`, `workloadIdentityConfig`, inlined source, and the `DISK` `emptyDir` medium.
- The build does not check references to other infrastructure, such as secrets, service accounts, and registry read grants for the Cloud Run service agent. Missing references surface at deployment, together with application failures (crashes, not listening on `$PORT`), quotas, organization policy, IAM propagation, and out-of-band edits.
