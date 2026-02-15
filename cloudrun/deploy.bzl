"""Cloud Run deploy rule — assembles self-contained deploy scripts.

The deploy script is fully assembled during `bazel build`. The rendered
manifest YAML is embedded inline, all gcloud arguments are resolved as
build-time constants, and no runtime runfiles resolution (rlocation /
RUNFILES_DIR / runfiles.bash) is required.  `bazel run` simply executes
the resulting self-contained shell script -> `exec gcloud ...`.
"""

def _runfiles_path(ctx, file):
    """Compute the runfiles-relative path for a file.

    Used to build the path segment after ${BASH_SOURCE[0]}.runfiles/ in deploy
    scripts.  For files in external repos short_path starts with '../<repo>/'
    and we strip the leading '../'.  For local files we prepend workspace_name.
    """
    if file.short_path.startswith("../"):
        return file.short_path[3:]
    return ctx.workspace_name + "/" + file.short_path

# Shell command executed during the build action to assemble the deploy
# script.  It reads the rendered manifest from $BZL_MANIFEST and produces
# the executable at $BZL_OUTPUT.  Build-time values are injected via env.
#
# The output script is split into two heredoc sections:
#   PART1_END  - unquoted heredoc; env vars ($BZL_*) are expanded by bash
#                so their values become readonly constants in the output.
#   PART2_END  - quoted heredoc; written verbatim so runtime shell syntax
#                ($1, $MANIFEST, ${BASH_SOURCE[0]}, etc.) is preserved.
_ASSEMBLE_DEPLOY_COMMAND = """\
set -euo pipefail

cat > "$BZL_OUTPUT" << PART1_END
#!/usr/bin/env bash
set -euo pipefail

# ── Build-time constants (resolved during bazel build) ───────────────────────
readonly SERVICE_NAME="${BZL_SERVICE_NAME}"
readonly RESOURCE_TYPE="${BZL_RESOURCE_TYPE}"
readonly GCLOUD_TRACK="${BZL_GCLOUD_TRACK}"
readonly GCLOUD_SUBCMD="${BZL_GCLOUD_SUBCMD}"
readonly REGION_FLAG="${BZL_REGION_FLAG}"
readonly PROJECT_FLAG="${BZL_PROJECT_FLAG}"
readonly PUSH_RUNFILE="${BZL_PUSH_RUNFILE}"

# ── Embedded manifest (generated during bazel build) ─────────────────────────
MANIFEST=\\$(mktemp -t cloudrun-XXXXXX.yaml)
trap 'rm -f "\\$MANIFEST"' EXIT
cat > "\\$MANIFEST" << 'CLOUDRUN_MANIFEST_EOF'
PART1_END

cat "$BZL_MANIFEST" >> "$BZL_OUTPUT"

cat >> "$BZL_OUTPUT" << 'PART2_END'
CLOUDRUN_MANIFEST_EOF

# ── Parse runtime arguments ──────────────────────────────────────────────────
IMAGE_OVERRIDE=""
EXTRA_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --image=*) IMAGE_OVERRIDE="${1#--image=}"; shift;;
    --image)
      [[ $# -lt 2 ]] && { echo "ERROR: --image requires a value" >&2; exit 1; }
      IMAGE_OVERRIDE="$2"; shift 2;;
    *) EXTRA_ARGS+=("$1"); shift;;
  esac
done

# ── Apply image override ─────────────────────────────────────────────────────
if [[ -n "$IMAGE_OVERRIDE" ]]; then
  if [[ ! "$IMAGE_OVERRIDE" =~ ^.+@sha256:[a-fA-F0-9]{64}$ ]]; then
    echo "ERROR: --image must be a digest reference (repo@sha256:<64 hex chars>)" >&2
    exit 1
  fi
  sed "s|image: .*|image: ${IMAGE_OVERRIDE}|" "$MANIFEST" > "$MANIFEST.tmp"
  mv "$MANIFEST.tmp" "$MANIFEST"
fi

# ── Push image ───────────────────────────────────────────────────────────────
if [[ -n "$PUSH_RUNFILE" && -z "$IMAGE_OVERRIDE" && "${SKIP_PUSH:-}" != "1" ]]; then
  PUSH_BIN="${BASH_SOURCE[0]}.runfiles/$PUSH_RUNFILE"
  if [[ ! -x "$PUSH_BIN" ]]; then
    echo "ERROR: push binary not found: $PUSH_BIN" >&2
    exit 1
  fi
  echo "Pushing image via $PUSH_BIN"
  "$PUSH_BIN"
  echo ""
elif [[ -n "$PUSH_RUNFILE" && -n "$IMAGE_OVERRIDE" ]]; then
  echo "Runtime image override provided; skipping push step."
  echo ""
fi

# ── Deploy ───────────────────────────────────────────────────────────────────
echo "Deploying Cloud Run $RESOURCE_TYPE: $SERVICE_NAME"
if [[ -n "$PROJECT_FLAG" ]]; then
  echo "  Project: ${PROJECT_FLAG#--project=}"
fi
if [[ -n "$REGION_FLAG" ]]; then
  echo "  Region:  ${REGION_FLAG#--region*=}"
fi
if [[ -n "$IMAGE_OVERRIDE" ]]; then
  echo "  Image:   $IMAGE_OVERRIDE (runtime override)"
fi
echo ""

# Ensure required gcloud track components are installed.
if [[ -n "$GCLOUD_TRACK" ]]; then
  gcloud components install "$GCLOUD_TRACK" --quiet 2>/dev/null || true
fi

GCLOUD_ARGS=()
[[ -n "$GCLOUD_TRACK" ]] && GCLOUD_ARGS+=("$GCLOUD_TRACK")
GCLOUD_ARGS+=(run "$GCLOUD_SUBCMD" replace "$MANIFEST")

# Worker pools: 'replace' does not accept --region; pass via env var instead.
if [[ "$RESOURCE_TYPE" == "worker" && -n "$REGION_FLAG" ]]; then
  export CLOUDSDK_RUN_REGION="${REGION_FLAG#--region=}"
else
  [[ -n "$REGION_FLAG" ]] && GCLOUD_ARGS+=("$REGION_FLAG")
fi

[[ -n "$PROJECT_FLAG" ]] && GCLOUD_ARGS+=("$PROJECT_FLAG")

GCLOUD_ARGS+=(--quiet)
if [[ "${#EXTRA_ARGS[@]}" -gt 0 ]]; then
  GCLOUD_ARGS+=("${EXTRA_ARGS[@]}")
fi

exec gcloud "${GCLOUD_ARGS[@]}"
PART2_END

chmod +x "$BZL_OUTPUT"
"""

def _cloudrun_deploy_impl(ctx):
    manifest = ctx.file.manifest
    script = ctx.actions.declare_file(ctx.label.name + "_run.sh")

    # ── Resolve gcloud subcommand and release track at analysis time ─────
    resource_type = ctx.attr.resource_type
    gcloud_subcmd = "services"
    gcloud_track = ""
    if resource_type == "job":
        gcloud_subcmd = "jobs"
    elif resource_type == "worker":
        gcloud_subcmd = "worker-pools"
        gcloud_track = "beta"
    if resource_type == "service" and len(ctx.attr.regions) > 1:
        gcloud_subcmd = "multi-region-services"

    # ── Resolve region / project flags at analysis time ──────────────────
    region_flag = ""
    if ctx.attr.regions:
        if resource_type == "service" and len(ctx.attr.regions) > 1:
            region_flag = "--regions=" + ",".join(ctx.attr.regions)
        else:
            region_flag = "--region=" + ctx.attr.regions[0]

    project_flag = ""
    if ctx.attr.project_id:
        project_flag = "--project=" + ctx.attr.project_id

    # ── Resolve push binary runfiles path ────────────────────────────────
    push_runfile = ""
    runfiles = ctx.runfiles(files = [])
    if ctx.attr.push_executable:
        push_runfile = _runfiles_path(ctx, ctx.executable.push_executable)
        runfiles = ctx.runfiles(files = [ctx.executable.push_executable])
        runfiles = runfiles.merge(ctx.attr.push_executable[DefaultInfo].default_runfiles)

    # ── Assemble the self-contained deploy script ────────────────────────
    ctx.actions.run_shell(
        inputs = [manifest],
        outputs = [script],
        env = {
            "BZL_OUTPUT": script.path,
            "BZL_MANIFEST": manifest.path,
            "BZL_SERVICE_NAME": ctx.attr.service_name,
            "BZL_RESOURCE_TYPE": resource_type,
            "BZL_GCLOUD_TRACK": gcloud_track,
            "BZL_GCLOUD_SUBCMD": gcloud_subcmd,
            "BZL_REGION_FLAG": region_flag,
            "BZL_PROJECT_FLAG": project_flag,
            "BZL_PUSH_RUNFILE": push_runfile,
        },
        command = _ASSEMBLE_DEPLOY_COMMAND,
        mnemonic = "CloudRunDeployAssemble",
        progress_message = "Assembling deploy script for %s" % ctx.attr.service_name,
    )

    return [DefaultInfo(
        executable = script,
        runfiles = runfiles,
    )]

cloudrun_deploy_target = rule(
    implementation = _cloudrun_deploy_impl,
    attrs = {
        "manifest": attr.label(mandatory = True, allow_single_file = [".yaml"]),
        "project_id": attr.string(),
        "push_executable": attr.label(executable = True, cfg = "target"),
        "regions": attr.string_list(),
        "resource_type": attr.string(default = "service"),
        "service_name": attr.string(mandatory = True),
    },
    executable = True,
)
