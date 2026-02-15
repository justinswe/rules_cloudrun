"""Public API for rules_cloudrun."""

load("//cloudrun:service.bzl", _cloudrun_service = "cloudrun_service")
load("//cloudrun:job.bzl", _cloudrun_job = "cloudrun_job")
load("//cloudrun:worker.bzl", _cloudrun_worker = "cloudrun_worker")

cloudrun_service = _cloudrun_service
cloudrun_job = _cloudrun_job
cloudrun_worker = _cloudrun_worker
