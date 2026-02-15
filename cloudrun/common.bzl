"""Shared utilities for rules_cloudrun."""

IMAGE_OVERRIDE_PLACEHOLDER = "rules-cloudrun.invalid/override-required:latest"

def extract_env_name(label_str, config_format):
    """Extract environment name from a config label using config_format pattern.

    Args:
        label_str: Bazel label string like ":apphosting.dev.yaml"
        config_format: Pattern like "apphosting.*.yaml" where * is the env name.

    Returns:
        The extracted environment name (e.g. "dev").
    """
    parts = config_format.split("*")
    if len(parts) != 2:
        fail("config_format must contain exactly one '*', got: " + config_format)

    prefix = parts[0]
    suffix = parts[1]

    # Get filename from label
    if ":" in label_str:
        filename = label_str.split(":")[-1]
    else:
        filename = label_str.split("/")[-1]

    if not filename.startswith(prefix):
        fail("Config '{}' does not match config_format '{}': expected prefix '{}'".format(
            filename,
            config_format,
            prefix,
        ))
    if not filename.endswith(suffix):
        fail("Config '{}' does not match config_format '{}': expected suffix '{}'".format(
            filename,
            config_format,
            suffix,
        ))

    env = filename[len(prefix):len(filename) - len(suffix)]
    if not env:
        fail("Config '{}' matches config_format '{}' but env name is empty".format(
            filename,
            config_format,
        ))

    return env
