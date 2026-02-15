"""Cloud Run Job macro — generates Cloud Run Job manifests and deploy targets."""

load("//cloudrun:common.bzl", "extract_env_name")
load("//cloudrun:deploy.bzl", "cloudrun_deploy_target")
load("//cloudrun:render.bzl", "cloudrun_render")

def cloudrun_job(
        name,
        job_name,
        region,
        image = "",
        image_target = "",
        image_repo = "",
        base_config = None,
        config = None,
        configs = [],
        config_format = "apphosting.*.yaml",
        project_id = "",
        **kwargs):
    """Generates Cloud Run Job manifests and deploy targets.

    Image can be specified as a full URL string via image, or as
    image_repo + image_target for digest-pinned deploy workflows.
    If neither image nor image_repo is provided, a deterministic placeholder
    image is rendered and should be overridden at deploy-time with --image.

    Args:
        name: Target name prefix.
        job_name: Cloud Run job name.
        region: GCP region.
        image: Optional full container image URL. Mutually exclusive with image_repo.
        image_target: Bazel label for OCI image target (default: ":image").
            The push and digest targets are derived as image_target + ".push" and ".digest".
        image_repo: Fully qualified image repository URL without tag/digest.
        base_config: Optional base apphosting YAML.
        config: Single config (mutually exclusive with configs).
        configs: List of env-specific configs.
        config_format: Filename pattern for env extraction.
        project_id: GCP project ID. Use {} for env substitution.
        **kwargs: Additional attributes.
    """
    if not job_name:
        fail("job_name is required")
    if image and image_repo:
        fail("Cannot specify both 'image' and 'image_repo'")
    if image_repo and "@" in image_repo:
        fail("image_repo must not include a digest")
    if config and configs:
        fail("Cannot specify both 'config' and 'configs'")
    if not config and not configs:
        fail("Must specify either 'config' or 'configs'")

    # Resolve image URL
    resolved_image = image
    resolved_image_repo = ""
    image_digest = None
    push_executable = None
    if image_repo:
        resolved_image = ""
        resolved_image_repo = image_repo.rstrip("/")
        target = image_target if image_target else ":image"
        image_digest = target + ".digest"
        push_executable = target + ".push"

    visibility = kwargs.pop("visibility", None)
    tags = kwargs.pop("tags", [])

    # Single-env: create targets without env suffix and return early
    if not configs:
        cloudrun_render(
            name = name + ".render",
            config = config,
            base_config = base_config,
            service_name = job_name,
            region = region,
            image = resolved_image,
            image_repo = resolved_image_repo,
            image_digest = image_digest,
            resource_type = "job",
            visibility = visibility,
            tags = tags,
        )
        cloudrun_deploy_target(
            name = name + ".deploy",
            manifest = ":" + name + ".render",
            project_id = project_id,
            push_executable = push_executable,
            regions = [region],
            resource_type = "job",
            service_name = job_name,
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
            service_name = job_name,
            region = region,
            image = resolved_image,
            image_repo = resolved_image_repo,
            image_digest = image_digest,
            resource_type = "job",
            visibility = visibility,
            tags = tags,
        )
        cloudrun_deploy_target(
            name = target_name + ".deploy",
            manifest = ":" + target_name + ".render",
            project_id = resolved_project,
            push_executable = push_executable,
            regions = [region],
            resource_type = "job",
            service_name = job_name,
            visibility = visibility,
            tags = tags + ["cloudrun_deploy"],
        )
