"""Cloud Run render rule — generates manifests from apphosting YAML."""

load("//cloudrun:common.bzl", "IMAGE_OVERRIDE_PLACEHOLDER")

def _cloudrun_render_impl(ctx):
    output = ctx.outputs.manifest
    yq = ctx.file._yq
    validate_script = ctx.file._validate
    generate_bin = ctx.executable._generate
    config = ctx.file.config

    inputs = [yq, validate_script, config]
    has_pinned_image = bool(ctx.file.image_digest)

    if has_pinned_image and not ctx.attr.image_repo:
        fail("image_repo is required when image_digest is set")
    if ctx.attr.image and has_pinned_image:
        fail("Only one of image or image_digest may be set")

    # Config merging step
    if ctx.file.base_config:
        inputs.append(ctx.file.base_config)
        merge_cmd = '"{yq}" eval-all \'. as $item ireduce ({{}}; . *+ $item)\' "{base}" "{overlay}" > "$MERGED"'.format(
            yq = yq.path,
            base = ctx.file.base_config.path,
            overlay = config.path,
        )
    else:
        merge_cmd = 'cp "{config}" "$MERGED"'.format(config = config.path)

    image_ref = ctx.attr.image if ctx.attr.image else IMAGE_OVERRIDE_PLACEHOLDER
    image_resolve_cmd = 'IMAGE_REF="{image}"'.format(image = image_ref)
    if has_pinned_image:
        digest_file = ctx.file.image_digest
        inputs.append(digest_file)
        image_resolve_cmd = """\
DIGEST=$(tr -d '[:space:]' < "{digest_path}")
if [[ -z "$DIGEST" ]]; then
  echo "ERROR: image digest file is empty: {digest_path}" >&2
  exit 1
fi
if [[ "$DIGEST" != sha256:* ]]; then
  echo "ERROR: image digest must start with 'sha256:', got '$DIGEST'" >&2
  exit 1
fi
IMAGE_REF="{image_repo}@$DIGEST"
""".format(
            digest_path = digest_file.path,
            image_repo = ctx.attr.image_repo,
        )

    cmd = """\
set -euo pipefail
MERGED=$(mktemp)
trap 'rm -f "$MERGED"' EXIT

# Step 1: Merge configs (base + overlay)
{merge_cmd}

# Step 2: Validate merged config
bash "{validate}" --yq "{yq}" --config "$MERGED" --resource-type "{resource_type}"

# Step 3: Resolve image reference
{image_resolve_cmd}

# Step 4: Generate manifest
"{generate}" --config "$MERGED" \\
  --service-name "{service_name}" \\
  --region "{region}" \\
  --image "$IMAGE_REF" \\
  --resource-type "{resource_type}" \\
  --timeout "{timeout}" \\
  --output "{output}"
""".format(
        merge_cmd = merge_cmd,
        validate = validate_script.path,
        generate = generate_bin.path,
        yq = yq.path,
        resource_type = ctx.attr.resource_type,
        image_resolve_cmd = image_resolve_cmd,
        service_name = ctx.attr.service_name,
        region = ctx.attr.region,
        timeout = ctx.attr.timeout_seconds,
        output = output.path,
    )

    ctx.actions.run_shell(
        command = cmd,
        inputs = inputs,
        tools = [generate_bin],
        outputs = [output],
        mnemonic = "CloudRunRender",
        progress_message = "Rendering Cloud Run manifest for %s" % ctx.attr.service_name,
    )

    return [DefaultInfo(files = depset([output]))]

cloudrun_render = rule(
    implementation = _cloudrun_render_impl,
    attrs = {
        "config": attr.label(mandatory = True, allow_single_file = [".yaml", ".yml"]),
        "base_config": attr.label(allow_single_file = [".yaml", ".yml"]),
        "service_name": attr.string(mandatory = True),
        "region": attr.string(mandatory = True),
        "image": attr.string(default = ""),
        "image_repo": attr.string(default = ""),
        "image_digest": attr.label(allow_single_file = True),
        "timeout_seconds": attr.int(default = 300),
        "resource_type": attr.string(default = "service"),
        "_validate": attr.label(
            default = "//cloudrun/private/validation:validate.sh",
            allow_single_file = True,
        ),
        "_generate": attr.label(
            default = "//cloudrun/private/resource/cmd:resource_manifest",
            executable = True,
            cfg = "exec",
        ),
        "_yq": attr.label(
            default = "@yq//:yq",
            allow_single_file = True,
        ),
    },
    outputs = {"manifest": "%{name}.yaml"},
)
