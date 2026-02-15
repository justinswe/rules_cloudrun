"""Cloud Run Service macro — generates Knative Service manifests and deploy targets."""

load("//cloudrun:common.bzl", "extract_env_name")
load("//cloudrun:deploy.bzl", "cloudrun_deploy_target")
load("//cloudrun:render.bzl", "cloudrun_render")

def cloudrun_service(
        name,
        service_name,
        region = "",
        regions = [],
        image = "",
        image_target = "",
        image_repo = "",
        base_config = None,
        config = None,
        configs = [],
        config_format = "apphosting.*.yaml",
        project_id = "",
        timeout_seconds = 300,
        **kwargs):
    """Generates Knative Service manifests and deploy targets from apphosting YAML.

    Image can be specified in three ways:
      1. image: A full container image URL string (e.g. "gcr.io/proj/app:latest").
      2. image_repo + image_target: Fully qualified image repository plus Bazel image target.
         The manifest image is pinned as image_repo@sha256:... using image_target.digest,
         and .deploy targets invoke image_target.push directly from runfiles.
      3. If neither image nor image_repo is provided, a deterministic placeholder
         image is rendered and must be overridden at deploy-time with --image.

    Region can be specified in two ways:
      1. region: A single GCP region string (e.g. "us-west1").
      2. regions: A list of GCP regions for multi-region deployment.
         Uses gcloud run multi-region-services for simultaneous deployment.

    Args:
        name: Target name prefix.
        service_name: Cloud Run service name.
        region: Single GCP region. Mutually exclusive with regions.
        regions: List of GCP regions for multi-region deployment.
        image: Optional full container image URL. Mutually exclusive with image_repo.
        image_target: Bazel label for the OCI image target (default: ":image").
            The push and digest targets are derived as image_target + ".push" and ".digest".
        image_repo: Fully qualified image repository URL without tag/digest
            (e.g. "us-central1-docker.pkg.dev/proj/repo/app").
        base_config: Optional base apphosting YAML (merged under each config).
        config: Single config (mutually exclusive with configs).
        configs: List of env-specific configs (mutually exclusive with config).
        config_format: Filename pattern for env extraction. Default: "apphosting.*.yaml".
        project_id: GCP project ID. Use {} for env substitution.
        timeout_seconds: Request timeout. Default: 300.
        **kwargs: Additional attributes passed to underlying rules.
    """

    # ── Validation ────────────────────────────────────────────────────────
    if not service_name:
        fail("service_name is required")
    if region and regions:
        fail("Cannot specify both 'region' and 'regions'")
    if not region and not regions:
        fail("Must specify either 'region' or 'regions'")
    if image and image_repo:
        fail("Cannot specify both 'image' and 'image_repo'")
    if image_repo and "@" in image_repo:
        fail("image_repo must not include a digest")
    if config and configs:
        fail("Cannot specify both 'config' and 'configs'")
    if not config and not configs:
        fail("Must specify either 'config' or 'configs'")

    # ── Resolve regions ───────────────────────────────────────────────────
    effective_regions = regions if regions else [region]

    # Use the first region for the manifest label (ignored by multi-region command)
    primary_region = effective_regions[0]

    # ── Resolve image URL ─────────────────────────────────────────────────
    resolved_image = image
    resolved_image_repo = ""
    image_digest = None
    push_executable = None
    if image_repo:
        resolved_image = ""
        resolved_image_repo = image_repo.rstrip("/")
        target = image_target if image_target else ":image"
        push_executable = target + ".push"
        image_digest = target + ".digest"

    visibility = kwargs.pop("visibility", None)
    tags = kwargs.pop("tags", [])

    # Single-env: create targets without env suffix and return early
    if not configs:
        cloudrun_render(
            name = name + ".render",
            config = config,
            base_config = base_config,
            service_name = service_name,
            region = primary_region,
            image = resolved_image,
            image_repo = resolved_image_repo,
            image_digest = image_digest,
            timeout_seconds = timeout_seconds,
            resource_type = "service",
            visibility = visibility,
            tags = tags,
        )

        cloudrun_deploy_target(
            name = name + ".deploy",
            manifest = ":" + name + ".render",
            project_id = project_id,
            push_executable = push_executable,
            regions = effective_regions,
            resource_type = "service",
            service_name = service_name,
            visibility = visibility,
            tags = tags + ["cloudrun_deploy"],
        )
        return

    # Multi-env: iterate configs, extract env names, create per-env targets
    for cfg in configs:
        env = extract_env_name(cfg, config_format)
        target_name = "{}_{}".format(name, env)
        resolved_project = project_id.replace("{}", env) if project_id else ""

        cloudrun_render(
            name = target_name + ".render",
            config = cfg,
            base_config = base_config,
            service_name = service_name,
            region = primary_region,
            image = resolved_image,
            image_repo = resolved_image_repo,
            image_digest = image_digest,
            timeout_seconds = timeout_seconds,
            resource_type = "service",
            visibility = visibility,
            tags = tags,
        )

        cloudrun_deploy_target(
            name = target_name + ".deploy",
            manifest = ":" + target_name + ".render",
            project_id = resolved_project,
            push_executable = push_executable,
            regions = effective_regions,
            resource_type = "service",
            service_name = service_name,
            visibility = visibility,
            tags = tags + ["cloudrun_deploy"],
        )
