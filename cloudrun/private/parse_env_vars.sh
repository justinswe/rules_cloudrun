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

# Function to parse env vars from a file
parse_env_vars() {
    local file="$1"

    # Parse env section
    in_env=false
    in_env_item=false
    current_var=""
    current_value=""

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
                if [[ -n "$current_var" && -n "$current_value" ]]; then
                    echo "${current_var}=${current_value}"
                fi
                # Start new item
                current_var="${BASH_REMATCH[1]}"
                current_value=""
                in_env_item=true
            elif [[ "$in_env_item" == true ]]; then
                if [[ "$line" =~ ^[[:space:]]+value:[[:space:]]*(.+)$ ]]; then
                    current_value="${BASH_REMATCH[1]}"
                fi
            fi
        fi
    done < "$file"

    # Save last item
    if [[ -n "$current_var" && -n "$current_value" ]]; then
        echo "${current_var}=${current_value}"
    fi
}

# Collect all env vars in an array
declare -a all_vars
all_vars=()

# Parse base config first (if provided) and collect env vars
if [ -n "$base_config" ] && [ -f "$base_config" ]; then
    while IFS= read -r var; do
        if [[ -n "$var" ]]; then
            all_vars+=("$var")
        fi
    done < <(parse_env_vars "$base_config")
fi

# Parse environment config and collect env vars (will override base vars)
while IFS= read -r var; do
    if [[ -n "$var" ]]; then
        all_vars+=("$var")
    fi
done < <(parse_env_vars "$env_config")

# Join array elements with commas and output
if [ ${#all_vars[@]} -gt 0 ]; then
    # Use printf to join array elements with commas
    joined_vars=$(printf "%s," "${all_vars[@]}")
    # Remove trailing comma
    joined_vars=${joined_vars%,}
    echo -n "--set-env-vars=${joined_vars}"
else
    echo -n ""
fi > "$output_file"
