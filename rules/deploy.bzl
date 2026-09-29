"""Optional direct deployment of a rendered Cloud Run v2 manifest."""

load(":providers.bzl", "CloudRunManifestInfo")

def _rlocation_path(ctx, file):
    if file.short_path.startswith("../"):
        return file.short_path[3:]
    return ctx.workspace_name + "/" + file.short_path

def _shell_quote(value):
    return "'" + value.replace("'", "'\\''") + "'"

def _cloudrun_deploy_impl(ctx):
    info = ctx.attr.manifest[CloudRunManifestInfo]
    binary = ctx.executable._deploy
    args = [
        "--project=" + ctx.attr.project,
        "--name=" + (ctx.attr.resource_name or info.resource_name),
        "--kind=" + (ctx.attr.kind or info.resource_type),
        "--image-container=" + info.image_container,
    ] + ["--region=" + region for region in ctx.attr.regions]
    args += ["--label=%s=%s" % (key, value) for key, value in sorted(ctx.attr.labels.items())]
    executable = ctx.actions.declare_file(ctx.label.name + ".sh")
    ctx.actions.write(
        output = executable,
        content = """#!/usr/bin/env bash
set -euo pipefail

# --- begin runfiles.bash initialization v3 ---
set +e
f=bazel_tools/tools/bash/runfiles/runfiles.bash
source "${{RUNFILES_DIR:-/dev/null}}/$f" 2>/dev/null || \\
  source "$(grep -sm1 "^$f " "${{RUNFILES_MANIFEST_FILE:-/dev/null}}" | cut -f2- -d' ')" 2>/dev/null || \\
  source "$0.runfiles/$f" 2>/dev/null || \\
  source "$(grep -sm1 "^$f " "$0.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null || \\
  {{ echo>&2 "ERROR: cannot find $f"; exit 1; }}
f=; set -e
# --- end runfiles.bash initialization v3 ---

exec "$(rlocation {binary})" --manifest="$(rlocation {manifest})" {args} "$@"
""".format(
            binary = _shell_quote(_rlocation_path(ctx, binary)),
            manifest = _shell_quote(_rlocation_path(ctx, info.manifest)),
            args = " ".join([_shell_quote(arg) for arg in args]),
        ),
        is_executable = True,
    )
    runfiles = ctx.runfiles(files = [binary, info.manifest])
    runfiles = runfiles.merge(ctx.attr._deploy[DefaultInfo].default_runfiles)
    runfiles = runfiles.merge(ctx.attr._runfiles_dep[DefaultInfo].default_runfiles)
    return [DefaultInfo(executable = executable, runfiles = runfiles)]

cloudrun_deploy = rule(
    implementation = _cloudrun_deploy_impl,
    executable = True,
    doc = """Applies a rendered manifest to Cloud Run with `bazel run`; opt in by declaring one.

Extra arguments are passed to cloudrun-deploy, for example
`bazel run :deploy -- --image=repo@sha256:... --validate-only`.
Two or more regions deploy a Service as one multi-region Service at
locations/global, where --validate-only is refused.
""",
    attrs = {
        "manifest": attr.label(mandatory = True, providers = [CloudRunManifestInfo], doc = "A `.render` target."),
        "project": attr.string(mandatory = True, doc = "Cloud Run project."),
        "regions": attr.string_list(mandatory = True, allow_empty = False, doc = "Cloud Run regions."),
        "resource_name": attr.string(doc = "Resource ID; defaults to the manifest's resource_name."),
        "kind": attr.string(values = ["", "service", "job", "worker", "workerpool"], doc = "Resource kind; defaults to the manifest's resource_type."),
        "labels": attr.string_dict(doc = "Labels set on the deployed resource."),
        "_deploy": attr.label(default = Label("//cmd/cloudrun-deploy"), executable = True, cfg = "target"),
        "_runfiles_dep": attr.label(default = Label("@bazel_tools//tools/bash/runfiles")),
    },
)
