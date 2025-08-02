#!/bin/bash
set -euo pipefail

# Arguments: service_name region source service_account top_level_input run_config_input env_vars_input secrets_input additional_flags [... other args]
service_name="$1"
region="$2"
source="$3"
service_account="$4"
top_level_input="$5"
run_config_input="$6"
env_vars_input="$7"
secrets_input="$8"
additional_flags="$9"

# Shift past the first 9 arguments to get the remaining gcloud args
shift 9

# Find runfiles directory
RUNFILES_DIR="$0.runfiles/_main"

# Check if --image flag is passed in arguments
USE_IMAGE=false
for arg in "$@"; do
    if [[ "$arg" == --image* ]]; then
        USE_IMAGE=true
        break
    fi
done

# Start building the gcloud command
GCLOUD_CMD="gcloud run deploy $service_name"

# Add region if specified
if [ -n "$region" ]; then
    GCLOUD_CMD="$GCLOUD_CMD --region=$region"
fi

# Add platform
GCLOUD_CMD="$GCLOUD_CMD --platform=managed"

# Add source if specified and no --image flag is being used
if [ -n "$source" ] && [ "$USE_IMAGE" = false ]; then
    GCLOUD_CMD="$GCLOUD_CMD --source=$source"
fi

# Add service account if specified
if [ -n "$service_account" ]; then
    GCLOUD_CMD="$GCLOUD_CMD --service-account=$service_account"
fi

# Add top-level config flags (including serviceAccount from config file)
if [ -n "$top_level_input" ] && [ -f "$top_level_input" ]; then
    top_level_flags=$(cat "$top_level_input")
    if [ -n "$top_level_flags" ]; then
        GCLOUD_CMD="$GCLOUD_CMD $top_level_flags"
    fi
fi

# Add run config flags
if [ -n "$run_config_input" ] && [ -f "$run_config_input" ]; then
    run_config=$(cat "$run_config_input")
    if [ -n "$run_config" ]; then
        GCLOUD_CMD="$GCLOUD_CMD $run_config"
    fi
fi

# Add environment variables
if [ -n "$env_vars_input" ] && [ -f "$env_vars_input" ]; then
    env_vars=$(cat "$env_vars_input")
    if [ -n "$env_vars" ]; then
        GCLOUD_CMD="$GCLOUD_CMD $env_vars"
    fi
fi

# Add secrets
if [ -n "$secrets_input" ] && [ -f "$secrets_input" ]; then
    secrets=$(cat "$secrets_input")
    if [ -n "$secrets" ]; then
        GCLOUD_CMD="$GCLOUD_CMD $secrets"
    fi
fi

# Add any additional flags
if [ -n "$additional_flags" ]; then
    GCLOUD_CMD="$GCLOUD_CMD $additional_flags"
fi

# Display the command being executed
echo "Executing: $GCLOUD_CMD" "$@"
echo

# Execute the gcloud command
exec $GCLOUD_CMD "$@"
