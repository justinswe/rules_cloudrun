"""Cloud Run deployment rules and utilities."""

load("//cloudrun:config.bzl", _cloudrun_service_config = "cloudrun_service_config")

def _cloudrun_deploy_impl(ctx):
    """Implementation for generating complete gcloud run deploy command."""
    output = ctx.actions.declare_file(ctx.label.name + ".sh")

    input_files = []

    # Collect all input files
    if ctx.file.top_level_flags:
        input_files.append(ctx.file.top_level_flags)

    if ctx.file.run_config_flags:
        input_files.append(ctx.file.run_config_flags)

    if ctx.file.env_vars_flags:
        input_files.append(ctx.file.env_vars_flags)

    if ctx.file.secrets_flags:
        input_files.append(ctx.file.secrets_flags)

    # Generate wrapper script that calls the execute script with proper arguments
    wrapper_script = """#!/bin/bash
set -euo pipefail

# Find runfiles directory
RUNFILES_DIR="$0.runfiles/_main"

# Path to the execute script
EXECUTE_SCRIPT="$RUNFILES_DIR/tools/cloudrun/private/execute_script"

# Arguments to pass to execute script
SERVICE_NAME="{service_name}"
REGION="{region}"
SOURCE="{source}"
SERVICE_ACCOUNT="{service_account}"
TOP_LEVEL_INPUT="{top_level_input}"
RUN_CONFIG_INPUT="{run_config_input}"
ENV_VARS_INPUT="{env_vars_input}"
SECRETS_INPUT="{secrets_input}"
ADDITIONAL_FLAGS="{additional_flags}"

# Execute the script with all arguments
exec "$EXECUTE_SCRIPT" "$SERVICE_NAME" "$REGION" "$SOURCE" "$SERVICE_ACCOUNT" "$TOP_LEVEL_INPUT" "$RUN_CONFIG_INPUT" "$ENV_VARS_INPUT" "$SECRETS_INPUT" "$ADDITIONAL_FLAGS" "$@"
""".format(
        service_name = ctx.attr.service_name,
        region = ctx.attr.region,
        source = ctx.attr.source,
        service_account = ctx.attr.service_account,
        top_level_input = input_files[0].short_path if len(input_files) > 0 else "",
        run_config_input = input_files[1].short_path if len(input_files) > 1 else "",
        env_vars_input = input_files[2].short_path if len(input_files) > 2 else "",
        secrets_input = input_files[3].short_path if len(input_files) > 3 else "",
        additional_flags = " ".join(ctx.attr.additional_flags),
    )

    ctx.actions.write(
        output = output,
        content = wrapper_script,
        is_executable = True,
    )

    # Include the execute script and input config files in runfiles
    all_runfiles = input_files + [ctx.executable._execute_script]
    runfiles = ctx.runfiles(files = all_runfiles)

    return [DefaultInfo(
        files = depset([output]),
        executable = output,
        runfiles = runfiles,
    )]

_cloudrun_deploy = rule(
    implementation = _cloudrun_deploy_impl,
    attrs = {
        "service_name": attr.string(
            mandatory = True,
            doc = "Name of the Cloud Run service",
        ),
        "region": attr.string(
            default = "",
            doc = "GCP region for deployment",
        ),
        "source": attr.string(
            default = "",
            doc = "Source directory or image for deployment",
        ),
        "service_account": attr.string(
            default = "",
            doc = "Service account email for the Cloud Run service",
        ),
        "top_level_flags": attr.label(
            allow_single_file = True,
            doc = "File containing top-level configuration flags",
        ),
        "run_config_flags": attr.label(
            allow_single_file = True,
            doc = "File containing run configuration flags",
        ),
        "env_vars_flags": attr.label(
            allow_single_file = True,
            doc = "File containing environment variables flags",
        ),
        "secrets_flags": attr.label(
            allow_single_file = True,
            doc = "File containing secrets flags",
        ),
        "additional_flags": attr.string_list(
            default = [],
            doc = "Additional gcloud run deploy flags",
        ),
        "_execute_script": attr.label(
            default = "//tools/cloudrun/private:execute_script",
            executable = True,
            cfg = "exec",
        ),
    },
    doc = "Generates complete gcloud run deploy command with all flags",
    executable = True,
)

# Generate final deployment command
# _cloudrun_deploy(
#     name = name,
#     service_name = service_name,
#     region = region,
#     source = source,
#     run_config_flags = ":" + name + "_run_config",
#     env_vars_flags = ":" + name + "_env_vars",
#     secrets_flags = ":" + name + "_secrets",
#     additional_flags = additional_flags,
# )

def cloudrun_deploy(name, service_name, region = "", source = "", service_account = "", additional_flags = [], config = None, base_config = None, env_configs = {}):
    """
    Creates a deployable target that generates and executes gcloud run deploy command.

    Args:
        name: Name of the target
        config: Label pointing to the service configuration YAML file
        service_name: Name of the Cloud Run service to deploy
        region: GCP region for deployment (optional)
        source: Source directory or image for deployment (optional)
        service_account: Service account email for the Cloud Run service (optional)
        additional_flags: Additional gcloud run deploy flags (optional)
        base_config: Label pointing to base configuration YAML file for defaults (optional)
        env_configs: Dictionary of environment configurations (optional)

    Example:
        cloudrun_deploy(
            name = "deploy_lavndrapi",
            config = "//lavndrapi:svc_cfg.prd.yaml",
            base_config = "//lavndrapi:svc_cfg.yaml",
            service_name = "lavndrapi",
            region = "us-central1",
            service_account = "my-service@project.iam.gserviceaccount.com",
        )

    Usage:
        bazel run //:deploy_lavndrapi
    """
    if env_configs and config:
        fail("env_configs and base_config cannot be used together")

    if not env_configs and not config:
        fail("env_configs or config must be provided")

    if env_configs and len(env_configs) > 0:
        for env, env_config in env_configs.items():
            cfg_name = "{}_{}".format(name, env)
            _cloudrun_service_config(
                name = "{}_cfg".format(cfg_name),
                config = env_config,
                base_config = base_config if base_config else None,
                service_name = service_name,
                region = region,
                source = source,
                additional_flags = additional_flags,
            )

            top_level_name = "{}_cfg_top_level".format(cfg_name)
            run_config_name = "{}_cfg_run_config".format(cfg_name)
            env_vars_name = "{}_cfg_env_vars".format(cfg_name)
            secrets_name = "{}_cfg_secrets".format(cfg_name)

            _cloudrun_deploy(
                name = "{}_{}".format(name, env),
                service_name = service_name,
                region = region,
                source = source,
                service_account = service_account,
                top_level_flags = ":" + top_level_name,
                run_config_flags = ":" + run_config_name,
                env_vars_flags = ":" + env_vars_name,
                secrets_flags = ":" + secrets_name,
                additional_flags = additional_flags,
            )
        return
