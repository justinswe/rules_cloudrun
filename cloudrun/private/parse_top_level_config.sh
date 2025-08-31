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

# Function to parse top-level config from a file
parse_top_level_config() {
    local file="$1"

    while IFS= read -r line; do
        # Parse top-level keys (not indented, containing a colon)
        if [[ "$line" =~ ^([a-zA-Z][a-zA-Z0-9]*)[[:space:]]*:[[:space:]]*(.+)$ ]]; then
            key="${BASH_REMATCH[1]}"
            value="${BASH_REMATCH[2]}"

            # Convert specific top-level keys to proper gcloud flags
            case "$key" in
                serviceAccount)
                    echo "service-account=$value"
                    ;;
                cloudsqlConnector)
                    echo "add-cloudsql-instances=$value"
                    ;;
            esac
        fi
    done < "$file"
}

# Collect all flags in an array
declare -a all_flags
all_flags=()

# Parse base config first (if provided) and collect flags
if [ -n "$base_config" ] && [ -f "$base_config" ]; then
    while IFS= read -r flag_line; do
        if [[ "$flag_line" =~ ^([^=]+)=(.+)$ ]]; then
            flag="--${BASH_REMATCH[1]}=${BASH_REMATCH[2]}"
            all_flags+=("$flag")
        fi
    done < <(parse_top_level_config "$base_config")
fi

# Parse environment config and collect flags (will override base flags)
while IFS= read -r flag_line; do
    if [[ "$flag_line" =~ ^([^=]+)=(.+)$ ]]; then
        flag="--${BASH_REMATCH[1]}=${BASH_REMATCH[2]}"
        all_flags+=("$flag")
    fi
done < <(parse_top_level_config "$env_config")

# Join array elements with spaces and output
if [ ${#all_flags[@]} -gt 0 ]; then
    # Use printf to join array elements with spaces
    joined_flags=$(printf "%s " "${all_flags[@]}")
    # Remove trailing space
    joined_flags=${joined_flags% }
    echo -n "$joined_flags"
else
    echo -n ""
fi > "$output_file"
echo >> "$output_file"  # Add final newline
