#!/bin/bash
set -euo pipefail

# Arguments: [base_config] env_config output_file
if [ $# -eq 3 ]; then
    base_config="$1"
    env_config="$2"
    output_file="$3"
else
    base_config=""
    env_config="$1"
    output_file="$2"
fi

# Function to parse secrets from a file
parse_secrets() {
    local file="$1"

    # Parse env section
    in_env=false
    in_env_item=false
    current_var=""
    current_secret=""

    while IFS= read -r line; do
        # Check if we're entering env section
        if [[ "$line" =~ ^[[:space:]]*env:[[:space:]]*$ ]]; then
            in_env=true
            continue
        fi

        # Check if we're leaving env section (new top-level key)
        if [[ "$in_env" == true && "$line" =~ ^[[:space:]]*[a-zA-Z] && ! "$line" =~ ^[[:space:]]{2,} ]]; then
            in_env=false
        fi

        if [[ "$in_env" == true ]]; then
            # Check for list item start
            if [[ "$line" =~ ^[[:space:]]*-[[:space:]]*variable:[[:space:]]*(.+)$ ]]; then
                # Save previous item if complete
                if [[ -n "$current_var" && -n "$current_secret" ]]; then
                    echo "${current_var}=${current_secret}"
                fi
                # Start new item
                current_var="${BASH_REMATCH[1]}"
                current_secret=""
                in_env_item=true
            elif [[ "$in_env_item" == true ]]; then
                if [[ "$line" =~ ^[[:space:]]+secret:[[:space:]]*(.+)$ ]]; then
                    current_secret="${BASH_REMATCH[1]}"
                fi
            fi
        fi
    done < "$file"

    # Save last item
    if [[ -n "$current_var" && -n "$current_secret" ]]; then
        echo "${current_var}=${current_secret}"
    fi
}

# Collect all secrets in an array
declare -a all_secrets_array
all_secrets_array=()

# Parse base config first (if provided) and collect secrets
if [ -n "$base_config" ] && [ -f "$base_config" ]; then
    while IFS= read -r secret; do
        if [[ -n "$secret" ]]; then
            all_secrets_array+=("$secret")
        fi
    done < <(parse_secrets "$base_config")
fi

# Parse environment config and collect secrets (will override base secrets)
while IFS= read -r secret; do
    if [[ -n "$secret" ]]; then
        all_secrets_array+=("$secret")
    fi
done < <(parse_secrets "$env_config")

# Join array elements with commas and output
if [ ${#all_secrets_array[@]} -gt 0 ]; then
    # Use printf to join array elements with commas
    joined_secrets=$(printf "%s," "${all_secrets_array[@]}")
    # Remove trailing comma
    joined_secrets=${joined_secrets%,}
    echo -n "--set-secrets=${joined_secrets}"
else
    echo -n ""
fi > "$output_file"
