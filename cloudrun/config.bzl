"""Cloud Run service configuration parsing and deployment rules."""

# Provider to pass parsed configuration data
CloudRunConfigInfo = provider(
    "Information about parsed Cloud Run configuration",
    fields = {
        "run_config_flags": "File containing run configuration flags",
        "env_vars_flags": "File containing environment variables flags",
        "secrets_flags": "File containing secrets flags",
    },
)

def cloudrun_service_config(name, config, service_name, region = "", source = "", additional_flags = [], base_config = None):
    """
    Macro to parse Cloud Run service configuration and generate deployment command.

    Args:
        name: Name of the target
        config: Label pointing to the service configuration YAML file
        service_name: Name of the Cloud Run service to deploy
        region: GCP region for deployment (optional)
        source: Source directory or image for deployment (optional)
        additional_flags: Additional gcloud run deploy flags (optional)
        base_config: Label pointing to base configuration YAML file for defaults (optional)

    The config YAML file can contain:
        - runConfig: Cloud Run service configuration (cpu, memory, instances, etc.)
        - env: Environment variables and secrets
        - serviceAccount: Service account email for the Cloud Run service
        - cloudsqlConnector: Cloud SQL instance connection name (project:region:instance)

    Example:
        cloudrun_service_config(
            name = "lavndrapi_deploy",
            config = "//lavndrapi:svc_cfg.dev.yaml",
            base_config = "//lavndrapi:svc_cfg.yaml",
            service_name = "lavndrapi",
            region = "us-central1",
            source = ".",
            additional_flags = ["--allow-unauthenticated"],
        )
    """

    # Parse top-level configuration
    parse_top_level_config(
        name = name + "_top_level",
        config = config,
        base_config = base_config,
    )

    # Parse run configuration
    parse_run_config(
        name = name + "_run_config",
        config = config,
        base_config = base_config,
    )

    # Parse environment variables
    parse_env_vars(
        name = name + "_env_vars",
        config = config,
        base_config = base_config,
    )

    # Parse secrets
    parse_secrets(
        name = name + "_secrets",
        config = config,
        base_config = base_config,
    )

def _parse_run_config_impl(ctx):
    """Implementation for parsing runConfig section from service config."""
    output = ctx.actions.declare_file(ctx.label.name + ".txt")

    inputs = [ctx.file.config]
    config_files = [ctx.file.config.path]

    # Add base config if provided
    if ctx.file.base_config:
        inputs.insert(0, ctx.file.base_config)
        config_files.insert(0, ctx.file.base_config.path)

    # Use the external script file
    script_file = ctx.executable._script

    arguments = config_files + [output.path]

    ctx.actions.run(
        inputs = inputs,
        outputs = [output],
        executable = script_file,
        arguments = arguments,
        mnemonic = "ParseRunConfig",
        progress_message = "Parsing runConfig from %s" % ctx.file.config.short_path,
    )

    return [DefaultInfo(files = depset([output]))]

parse_run_config = rule(
    implementation = _parse_run_config_impl,
    attrs = {
        "config": attr.label(
            allow_single_file = [".yaml", ".yml"],
            mandatory = True,
            doc = "Service configuration YAML file",
        ),
        "base_config": attr.label(
            allow_single_file = [".yaml", ".yml"],
            doc = "Base configuration YAML file for defaults",
        ),
        "_script": attr.label(
            default = "//tools/cloudrun/private:parse_run_config_script",
            executable = True,
            cfg = "exec",
        ),
    },
    doc = "Parses runConfig section and generates gcloud run deploy flags",
)

def _parse_env_vars_impl(ctx):
    """Implementation for parsing env variables from service config."""
    output = ctx.actions.declare_file(ctx.label.name + ".txt")

    inputs = [ctx.file.config]
    config_files = [ctx.file.config.path]

    # Add base config if provided
    if ctx.file.base_config:
        inputs.insert(0, ctx.file.base_config)
        config_files.insert(0, ctx.file.base_config.path)

    # Use the external script file
    script_file = ctx.executable._script

    arguments = config_files + [output.path]

    ctx.actions.run(
        inputs = inputs,
        outputs = [output],
        executable = script_file,
        arguments = arguments,
        mnemonic = "ParseEnvVars",
        progress_message = "Parsing environment variables from %s" % ctx.file.config.short_path,
    )

    return [DefaultInfo(files = depset([output]))]

parse_env_vars = rule(
    implementation = _parse_env_vars_impl,
    attrs = {
        "config": attr.label(
            allow_single_file = [".yaml", ".yml"],
            mandatory = True,
            doc = "Service configuration YAML file",
        ),
        "base_config": attr.label(
            allow_single_file = [".yaml", ".yml"],
            doc = "Base configuration YAML file for defaults",
        ),
        "_script": attr.label(
            default = "//tools/cloudrun/private:parse_env_vars_script",
            executable = True,
            cfg = "exec",
        ),
    },
    doc = "Parses env section and generates gcloud environment variables flags",
)

def _parse_secrets_impl(ctx):
    """Implementation for parsing secrets from service config."""
    output = ctx.actions.declare_file(ctx.label.name + ".txt")

    inputs = [ctx.file.config]
    config_files = [ctx.file.config.path]

    # Add base config if provided
    if ctx.file.base_config:
        inputs.insert(0, ctx.file.base_config)
        config_files.insert(0, ctx.file.base_config.path)

    # Use the external script file
    script_file = ctx.executable._script

    arguments = config_files + [output.path]

    ctx.actions.run(
        inputs = inputs,
        outputs = [output],
        executable = script_file,
        arguments = arguments,
        mnemonic = "ParseSecrets",
        progress_message = "Parsing secrets from %s" % ctx.file.config.short_path,
    )

    return [DefaultInfo(files = depset([output]))]

parse_secrets = rule(
    implementation = _parse_secrets_impl,
    attrs = {
        "config": attr.label(
            allow_single_file = [".yaml", ".yml"],
            mandatory = True,
            doc = "Service configuration YAML file",
        ),
        "base_config": attr.label(
            allow_single_file = [".yaml", ".yml"],
            doc = "Base configuration YAML file for defaults",
        ),
        "_script": attr.label(
            default = "//tools/cloudrun/private:parse_secrets_script",
            executable = True,
            cfg = "exec",
        ),
    },
    doc = "Parses env section and generates gcloud secrets flags",
)

def _parse_top_level_config_impl(ctx):
    """Implementation for parsing top-level configuration from service config."""
    output = ctx.actions.declare_file(ctx.label.name + ".txt")

    inputs = [ctx.file.config]
    config_files = [ctx.file.config.path]

    # Add base config if provided
    if ctx.file.base_config:
        inputs.insert(0, ctx.file.base_config)
        config_files.insert(0, ctx.file.base_config.path)

    # Use the external script file
    script_file = ctx.executable._script

    arguments = config_files + [output.path]

    ctx.actions.run(
        inputs = inputs,
        outputs = [output],
        executable = script_file,
        arguments = arguments,
        mnemonic = "ParseTopLevelConfig",
        progress_message = "Parsing top-level config from %s" % ctx.file.config.short_path,
    )

    return [DefaultInfo(files = depset([output]))]

parse_top_level_config = rule(
    implementation = _parse_top_level_config_impl,
    attrs = {
        "config": attr.label(
            allow_single_file = [".yaml", ".yml"],
            mandatory = True,
            doc = "Service configuration YAML file",
        ),
        "base_config": attr.label(
            allow_single_file = [".yaml", ".yml"],
            doc = "Base configuration YAML file for defaults",
        ),
        "_script": attr.label(
            default = "//tools/cloudrun/private:parse_top_level_config_script",
            executable = True,
            cfg = "exec",
        ),
    },
    doc = "Parses top-level configuration and generates gcloud flags. Supports: serviceAccount (--service-account), cloudsqlConnector (--add-cloudsql-instances)",
)
