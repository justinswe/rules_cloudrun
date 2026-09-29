"""Hermetic Cloud Run v2 manifest rendering."""

load("@rules_img//img:providers.bzl", "ImageIndexInfo", "ImageManifestInfo")
load(":providers.bzl", "CloudRunManifestInfo")

def _cloudrun_render_impl(ctx):
    if ctx.attr.image and ctx.attr.image_repo:
        fail("image conflicts with pinned image inputs")
    if ctx.attr.image_repo and not ctx.attr.image_target:
        fail("image_repo requires image_target, whose digest pins the image")
    digest = _image_digest(ctx.attr.image_target) if ctx.attr.image_repo else None
    output = ctx.outputs.manifest
    args = ctx.actions.args()
    args.add("--config", ctx.file.config)
    args.add("--service-name", ctx.attr.service_name)
    args.add("--resource-type", ctx.attr.resource_type)
    args.add("--output", output)

    # Every flag is explicit, even when empty, so environment variables and .env files cannot fill it.
    image_config = _runtime_image(ctx.attr.image_target).config if ctx.attr.image_target else None
    optional = [ctx.file.base_config, image_config, digest]
    inputs = [ctx.file.config] + [f for f in optional if f]
    args.add("--base-config", ctx.file.base_config or "")
    args.add("--image", ctx.attr.image)
    args.add("--image-container", ctx.attr.image_container)
    args.add("--image-config", image_config or "")
    args.add("--image-digest", digest or "")
    args.add("--image-repo", ctx.attr.image_repo)
    args.add("--log-level", "1")
    args.add("--debug-format=false")
    ctx.actions.run(
        executable = ctx.executable._generate,
        arguments = [args],
        inputs = inputs,
        outputs = [output],
        mnemonic = "CloudRunRender",
        progress_message = "Rendering Cloud Run v2 %s" % ctx.attr.service_name,
    )
    return [
        DefaultInfo(files = depset([output]), runfiles = ctx.runfiles(files = [output])),
        CloudRunManifestInfo(
            manifest = output,
            resource_type = ctx.attr.resource_type,
            resource_name = ctx.attr.service_name,
            image_container = ctx.attr.image_container,
            image_repo = ctx.attr.image_repo,
        ),
    ]

def _image_digest(target):
    """Returns the digest file rules_img image targets expose in their digest output group."""
    if OutputGroupInfo not in target or not hasattr(target[OutputGroupInfo], "digest"):
        fail("image_target must provide a rules_img digest output group")
    files = target[OutputGroupInfo].digest.to_list()
    if len(files) != 1:
        fail("image_target must provide exactly one digest file")
    return files[0]

def _runtime_image(target):
    if ImageIndexInfo in target:
        manifests = target[ImageIndexInfo].manifests
    elif ImageManifestInfo in target:
        manifests = [target[ImageManifestInfo]]
    else:
        fail("image_target must provide rules_img ImageManifestInfo or ImageIndexInfo for runtime validation")
    supported = [info for info in manifests if info.os == "linux" and info.architecture == "amd64"]
    if len(supported) != 1:
        fail("Cloud Run requires exactly one Linux amd64 image variant")
    return supported[0]

cloudrun_render = rule(
    implementation = _cloudrun_render_impl,
    attrs = {
        "config": attr.label(mandatory = True, allow_single_file = [".yaml", ".yml", ".json"]),
        "base_config": attr.label(allow_single_file = [".yaml", ".yml", ".json"]),
        "service_name": attr.string(mandatory = True),
        "resource_type": attr.string(default = "service", values = ["service", "job", "worker"]),
        "image": attr.string(),
        "image_repo": attr.string(),
        "image_target": attr.label(),
        "image_container": attr.string(),
        "_generate": attr.label(
            default = Label("//cmd/cloudrun-manifest"),
            executable = True,
            cfg = "exec",
        ),
    },
    outputs = {"manifest": "%{name}.json"},
)

def extract_env_name(label_str, config_format):
    """Extracts the environment name from a config label using a `prefix*suffix` pattern.

    Args:
        label_str: Bazel label string like ":manifest.dev.yaml".
        config_format: Pattern like "manifest.*.yaml" where * is the env name.

    Returns:
        The extracted environment name (e.g. "dev").
    """
    parts = config_format.split("*")
    if len(parts) != 2:
        fail("config_format must contain exactly one '*', got: " + config_format)
    prefix, suffix = parts
    filename = label_str.split(":")[-1] if ":" in label_str else label_str.split("/")[-1]
    if not filename.startswith(prefix):
        fail("Config '{}' does not match config_format '{}': expected prefix '{}'".format(filename, config_format, prefix))
    if not filename.endswith(suffix):
        fail("Config '{}' does not match config_format '{}': expected suffix '{}'".format(filename, config_format, suffix))
    env = filename[len(prefix):len(filename) - len(suffix)]
    if not env:
        fail("Config '{}' matches config_format '{}' but env name is empty".format(filename, config_format))
    return env

def generate_manifest(
        name,
        resource_name,
        resource_type = "service",
        image = "",
        image_repo = "",
        image_target = None,
        image_container = "",
        base_config = None,
        config = None,
        configs = [],
        config_format = "manifest.*.yaml",
        **kwargs):
    """Renders native v2 YAML configuration to deterministic JSON.

    Images come from configuration, image, or image_repo plus image_target.
    Multi-container overrides require image_container. Mapping overlays merge;
    arrays replace, and null removes an inherited key. Timeout belongs in YAML.
    Standard Bazel attributes are forwarded to every generated .render target.

    Args:
        name: Base name; one `<name>_<env>.render` target is created per config.
        resource_name: Cloud Run resource ID.
        resource_type: service, job, or worker.
        image: Literal image reference; conflicts with image_repo.
        image_repo: Repository for the digest-pinned image built by image_target.
        image_target: rules_img image target whose digest pins the image.
        image_container: Container that receives the image override.
        base_config: Shared YAML merged beneath every config.
        config: Single overlay; conflicts with configs.
        configs: Per-environment overlays named by config_format.
        config_format: `prefix*suffix` pattern naming each environment.
        **kwargs: Standard attributes forwarded to each render target.
    """
    if not resource_name or len(resource_name) > 49:
        fail("resource_name must contain 1-49 characters")
    if resource_name[0] not in "abcdefghijklmnopqrstuvwxyz" or resource_name[-1] == "-":
        fail("resource_name must start with a lowercase letter and not end with a hyphen")
    for c in resource_name.elems():
        if c not in "abcdefghijklmnopqrstuvwxyz0123456789-":
            fail("invalid character in resource_name")
    if resource_type not in ["service", "job", "worker"]:
        fail("resource_type must be service, job, or worker")
    if image and (image_repo or image_target):
        fail("image conflicts with image_repo and image_target")
    if bool(image_repo) != bool(image_target):
        fail("image_repo and image_target must be provided together")
    if image_repo and ("@" in image_repo or ":" in image_repo.split("/")[-1] or image_repo.endswith("/")):
        fail("image_repo must not include a tag, digest, or trailing slash")
    if bool(config) == bool(configs):
        fail("specify exactly one of config or configs")
    targets = [(name, config)] if config else []
    seen = {}
    for cfg in configs:
        env = extract_env_name(str(cfg), config_format)
        if env in seen:
            fail("duplicate configuration environment: " + env)
        seen[env] = True
        targets.append((name + "_" + env, cfg))
    for target, cfg in targets:
        cloudrun_render(
            name = target + ".render",
            service_name = resource_name,
            resource_type = resource_type,
            config = cfg,
            base_config = base_config,
            image = image,
            image_repo = image_repo,
            image_target = image_target,
            image_container = image_container,
            **kwargs
        )
