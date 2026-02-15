"""Hermetic tool downloads for rules_cloudrun."""

def _detect_platform(repository_ctx):
    """Detect the host OS and architecture."""
    os_name = repository_ctx.os.name.lower()
    arch = repository_ctx.os.arch

    if "mac" in os_name or "darwin" in os_name:
        os_part = "darwin"
    elif "linux" in os_name:
        os_part = "linux"
    else:
        fail("Unsupported OS: " + os_name)

    if arch == "aarch64" or arch == "arm64":
        arch_part = "arm64"
    elif arch == "x86_64" or arch == "amd64":
        arch_part = "amd64"
    else:
        fail("Unsupported architecture: " + arch)

    return os_part, arch_part

# ─── yq ──────────────────────────────────────────────────────────────────────

def _yq_repo_impl(repository_ctx):
    os_part, arch_part = _detect_platform(repository_ctx)
    version = repository_ctx.attr.version

    url = "https://github.com/mikefarah/yq/releases/download/v{version}/yq_{os}_{arch}".format(
        version = version,
        os = os_part,
        arch = arch_part,
    )

    repository_ctx.download(
        url = url,
        output = "yq",
        executable = True,
    )

    repository_ctx.file("BUILD.bazel", 'exports_files(["yq"], visibility = ["//visibility:public"])\n')

yq_repo = repository_rule(
    implementation = _yq_repo_impl,
    attrs = {"version": attr.string(default = "4.44.6")},
)

# ─── Skaffold ────────────────────────────────────────────────────────────────

def _skaffold_repo_impl(repository_ctx):
    os_part, arch_part = _detect_platform(repository_ctx)
    version = repository_ctx.attr.version

    url = "https://storage.googleapis.com/skaffold/releases/v{version}/skaffold-{os}-{arch}".format(
        version = version,
        os = os_part,
        arch = arch_part,
    )

    repository_ctx.download(
        url = url,
        output = "skaffold",
        executable = True,
    )

    repository_ctx.file("BUILD.bazel", 'exports_files(["skaffold"], visibility = ["//visibility:public"])\n')

skaffold_repo = repository_rule(
    implementation = _skaffold_repo_impl,
    attrs = {"version": attr.string(default = "2.17.2")},
)

# ─── gcloud SDK ──────────────────────────────────────────────────────────────

def _gcloud_repo_impl(repository_ctx):
    os_part, arch_part = _detect_platform(repository_ctx)
    version = repository_ctx.attr.version

    # gcloud uses "arm" not "arm64" for Linux, and "arm" for macOS
    gcloud_arch = arch_part
    if arch_part == "arm64":
        gcloud_arch = "arm"

    url = "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-{version}-{os}-{arch}.tar.gz".format(
        version = version,
        os = os_part,
        arch = gcloud_arch if os_part == "linux" else arch_part,
    )

    repository_ctx.download_and_extract(
        url = url,
        stripPrefix = "google-cloud-sdk",
    )

    repository_ctx.file("BUILD.bazel", """
filegroup(
    name = "sdk",
    srcs = glob(["**/*"]),
    visibility = ["//visibility:public"],
)

sh_binary(
    name = "gcloud",
    srcs = ["bin/gcloud"],
    data = [":sdk"],
    visibility = ["//visibility:public"],
)
""")

gcloud_repo = repository_rule(
    implementation = _gcloud_repo_impl,
    attrs = {"version": attr.string(default = "516.0.0")},
)

# ─── Module extension ────────────────────────────────────────────────────────

def _tools_impl(module_ctx):
    yq_repo(name = "yq")
    skaffold_repo(name = "skaffold")
    gcloud_repo(name = "gcloud_sdk")

tools = module_extension(implementation = _tools_impl)
