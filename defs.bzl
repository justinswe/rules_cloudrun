"""Public API for Cloud Run v2 manifests and their optional direct deployment."""

load("//rules:deploy.bzl", _cloudrun_deploy = "cloudrun_deploy")
load("//rules:manifest.bzl", _cloudrun_render = "cloudrun_render", _extract_env_name = "extract_env_name", _generate_manifest = "generate_manifest")
load("//rules:providers.bzl", _CloudRunManifestInfo = "CloudRunManifestInfo")

CloudRunManifestInfo = _CloudRunManifestInfo
cloudrun_deploy = _cloudrun_deploy
cloudrun_render = _cloudrun_render
extract_env_name = _extract_env_name
generate_manifest = _generate_manifest
