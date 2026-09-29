"""Providers shared by Cloud Run v2 manifest and deploy rules."""

CloudRunManifestInfo = provider(
    doc = "A validated Cloud Run v2 request body and its deployment identity.",
    fields = {
        "manifest": "Native JSON request body.",
        "resource_type": "service, job, or worker.",
        "resource_name": "Cloud Run resource ID.",
        "image_container": "Container selected for image overrides.",
        "image_repo": "Bazel image repository, when configured.",
    },
)
