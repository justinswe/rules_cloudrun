"""Cloud Run deployment rules and utilities."""

load("//cloudrun:config.bzl", _cloudrun_service_config = "cloudrun_service_config")
load("@rules_shell//shell:sh_binary.bzl", "sh_binary")

def _create_multi_region_aggregate_target(name, regional_targets):
    """
    Creates an aggregate target that can deploy to all regions at once.
    
    Args:
        name: Name of the aggregate target
        regional_targets: List of regional deployment targets
    """
    # Create a shell script that runs all regional deployments
    script_content = """#!/bin/bash
set -euo pipefail

echo "Deploying to multiple regions..."
echo "Regional targets: {targets}"
echo

# Run each regional deployment
{deployment_commands}

echo
echo "Multi-region deployment complete!"
""".format(
        targets = " ".join(regional_targets),
        deployment_commands = "\n".join([
            'echo "Deploying to region via target: {}"'.format(target) +
            '\nbazel run {} "$@"'.format(target) + '\necho'
            for target in regional_targets
        ])
    )
    
    native.genrule(
        name = name + "_script",
        outs = [name + "_script.sh"],
        cmd = "echo '{}' > $@".format(script_content.replace("'", "'\\''")),
        executable = True,
    )
    
    sh_binary(
        name = name,
        srcs = [":" + name + "_script"],
        data = regional_targets,
    )

def _cloudrun_deploy_impl(ctx):
    """Implementation for generating complete gcloud run deploy command script."""
    output = ctx.actions.declare_file(ctx.label.name + ".sh")

    inputs = []
    args = [output.path]

    # Service Name
    args.append(ctx.attr.service_name)
    
    # Region
    args.append(ctx.attr.region)
    
    # Source
    args.append(ctx.attr.source)
    
    # Service Account
    args.append(ctx.attr.service_account)
    
    # Job
    args.append(str(ctx.attr.job))

    # Top Level Flags
    if ctx.file.top_level_flags:
        inputs.append(ctx.file.top_level_flags)
        args.append(ctx.file.top_level_flags.path)
    else:
        args.append("")

    # Run Config Flags
    if ctx.file.run_config_flags:
        inputs.append(ctx.file.run_config_flags)
        args.append(ctx.file.run_config_flags.path)
    else:
        args.append("")

    # Env Vars Flags
    if ctx.file.env_vars_flags:
        inputs.append(ctx.file.env_vars_flags)
        args.append(ctx.file.env_vars_flags.path)
    else:
        args.append("")

    # Secrets Flags
    if ctx.file.secrets_flags:
        inputs.append(ctx.file.secrets_flags)
        args.append(ctx.file.secrets_flags.path)
    else:
        args.append("")

    # Additional Flags
    args.append(" ".join(ctx.attr.additional_flags))

    ctx.actions.run(
        outputs = [output],
        inputs = inputs,
        executable = ctx.executable._assembler,
        arguments = args,
        mnemonic = "AssembleCloudRunCommand",
        progress_message = "Generating Cloud Run deployment script for %s" % ctx.label.name,
    )

    return [DefaultInfo(
        files = depset([output]),
        executable = output,
        runfiles = ctx.runfiles(files = inputs),
    )]

_cloudrun_deploy = rule(
    implementation = _cloudrun_deploy_impl,
    attrs = {
        "service_name": attr.string(
            mandatory = True,
            doc = "Name of the Cloud Run service or job",
        ),
        "job": attr.bool(
            default = False,
            doc = "Whether to deploy as a Cloud Run job instead of service",
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
        "_assembler": attr.label(
            default = "//cloudrun/private:assemble_command_script",
            executable = True,
            cfg = "exec",
        ),
    },
    doc = "Generates complete gcloud run deploy command script",
    executable = True,
)

def cloudrun_deploy(name, service_name, region = "", source = "", service_account = "", additional_flags = [], config = None, base_config = None, env_configs = {}, job = False, multi_region = False, regions = []):
    """
    Creates a deployable target that generates and executes gcloud run deploy command.

    Args:
        name: Name of the target
        config: Label pointing to the service configuration YAML file
        service_name: Name of the Cloud Run service or job to deploy
        region: GCP region for deployment (optional, cannot be used with multi_region=True)
        source: Source directory or image for deployment (optional)
        service_account: Service account email for the Cloud Run service or job (optional)
        additional_flags: Additional gcloud run deploy/jobs deploy flags (optional)
        base_config: Label pointing to base configuration YAML file for defaults (optional)
        env_configs: Dictionary of environment configurations (optional)
        job: Whether to deploy as a Cloud Run job instead of service (optional, default False)
        multi_region: Enable multi-region deployment (optional, default False)
        regions: List of GCP regions for multi-region deployment (only used when multi_region=True)

    Examples:
        # Deploy a Cloud Run service to a single region
        cloudrun_deploy(
            name = "deploy_lavndrapi",
            config = "//lavndrapi:svc_cfg.prd.yaml",
            base_config = "//lavndrapi:svc_cfg.yaml",
            service_name = "lavndrapi",
            region = "us-central1",
            service_account = "my-service@project.iam.gserviceaccount.com",
        )

        # Deploy a Cloud Run service to multiple regions
        cloudrun_deploy(
            name = "deploy_lavndrapi_multi",
            config = "//lavndrapi:svc_cfg.prd.yaml",
            base_config = "//lavndrapi:svc_cfg.yaml",
            service_name = "lavndrapi",
            multi_region = True,
            regions = ["us-central1", "europe-west1", "asia-east1"],
            service_account = "my-service@project.iam.gserviceaccount.com",
        )

        # Deploy a Cloud Run job
        cloudrun_deploy(
            name = "deploy_data_processor",
            config = "//jobs:processor_cfg.yaml",
            service_name = "data-processor",
            region = "us-central1",
            job = True,
        )

    Usage:
        # Single region deployment
        bazel run //:deploy_lavndrapi
        
        # Multi-region deployment (individual regions)
        bazel run //:deploy_lavndrapi_multi_us_central1
        bazel run //:deploy_lavndrapi_multi_europe_west1
        
        # Multi-region deployment (all regions at once)
        bazel run //:deploy_lavndrapi_multi_all
    """
    # Validation logic
    if env_configs and config:
        fail("env_configs and base_config cannot be used together")

    if not env_configs and not config:
        fail("env_configs or config must be provided")

    # Multi-region validation
    if multi_region:
        if region:
            fail("Cannot specify both 'region' and 'multi_region=True'. Use 'regions' list for multi-region deployment.")
        if not regions or len(regions) == 0:
            fail("When multi_region=True, 'regions' list must be provided and cannot be empty.")
    else:
        if regions and len(regions) > 0:
            fail("'regions' can only be used when multi_region=True.")
        if not region and not env_configs:
            fail("'region' must be specified for single-region deployment when not using env_configs.")

    # Handle multi-region deployment
    if multi_region:
        regional_targets = []
        
        if env_configs and len(env_configs) > 0:
            # Multi-region with env_configs
            for env, env_config in env_configs.items():
                for target_region in regions:
                    # Normalize region name for target naming (replace hyphens with underscores)
                    normalized_region = target_region.replace("-", "_")
                    cfg_name = "{}_{}_{}".format(name, env, normalized_region)
                    regional_target_name = "{}_{}_{}".format(name, env, normalized_region)
                    
                    _cloudrun_service_config(
                        name = "{}_cfg".format(cfg_name),
                        config = env_config,
                        base_config = base_config if base_config else None,
                        service_name = service_name,
                        region = target_region,
                        source = source,
                        additional_flags = additional_flags,
                    )

                    top_level_name = "{}_cfg_top_level".format(cfg_name)
                    run_config_name = "{}_cfg_run_config".format(cfg_name)
                    env_vars_name = "{}_cfg_env_vars".format(cfg_name)
                    secrets_name = "{}_cfg_secrets".format(cfg_name)

                    _cloudrun_deploy(
                        name = regional_target_name,
                        service_name = service_name,
                        region = target_region,
                        source = source,
                        service_account = service_account,
                        job = job,
                        top_level_flags = ":" + top_level_name,
                        run_config_flags = ":" + run_config_name,
                        env_vars_flags = ":" + env_vars_name,
                        secrets_flags = ":" + secrets_name,
                        additional_flags = additional_flags,
                    )
                    regional_targets.append(":" + regional_target_name)
        else:
            # Multi-region with single config
            for target_region in regions:
                # Normalize region name for target naming (replace hyphens with underscores)
                normalized_region = target_region.replace("-", "_")
                cfg_name = "{}_{}".format(name, normalized_region)
                regional_target_name = "{}_{}".format(name, normalized_region)
                
                _cloudrun_service_config(
                    name = "{}_cfg".format(cfg_name),
                    config = config,
                    base_config = base_config,
                    service_name = service_name,
                    region = target_region,
                    source = source,
                    additional_flags = additional_flags,
                )

                _cloudrun_deploy(
                    name = regional_target_name,
                    service_name = service_name,
                    region = target_region,
                    source = source,
                    service_account = service_account,
                    job = job,
                    top_level_flags = ":" + cfg_name + "_cfg_top_level",
                    run_config_flags = ":" + cfg_name + "_cfg_run_config",
                    env_vars_flags = ":" + cfg_name + "_cfg_env_vars",
                    secrets_flags = ":" + cfg_name + "_cfg_secrets",
                    additional_flags = additional_flags,
                )
                regional_targets.append(":" + regional_target_name)

        # Create aggregate target that deploys to all regions
        _create_multi_region_aggregate_target(
            name = name + "_all",
            regional_targets = regional_targets,
        )
        return

    # Handle single-region deployment
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
                job = job,
                top_level_flags = ":" + top_level_name,
                run_config_flags = ":" + run_config_name,
                env_vars_flags = ":" + env_vars_name,
                secrets_flags = ":" + secrets_name,
                additional_flags = additional_flags,
            )
        return

    # Handle single config case
    if config:
        _cloudrun_service_config(
            name = name + "_cfg",
            config = config,
            base_config = base_config,
            service_name = service_name,
            region = region,
            source = source,
            additional_flags = additional_flags,
        )

        _cloudrun_deploy(
            name = name,
            service_name = service_name,
            region = region,
            source = source,
            service_account = service_account,
            job = job,
            top_level_flags = ":" + name + "_cfg_top_level",
            run_config_flags = ":" + name + "_cfg_run_config",
            env_vars_flags = ":" + name + "_cfg_env_vars",
            secrets_flags = ":" + name + "_cfg_secrets",
            additional_flags = additional_flags,
        )