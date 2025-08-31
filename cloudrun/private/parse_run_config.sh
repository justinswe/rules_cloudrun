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

# Function to parse runConfig from a file
parse_run_config() {
    local file="$1"

    # Parse runConfig section
    in_run_config=false
    while IFS= read -r line; do
        # Check if we're entering runConfig section
        if [[ "$line" =~ ^[[:space:]]*runConfig:[[:space:]]*$ ]]; then
            in_run_config=true
            continue
        fi

        # Check if we're leaving runConfig section (new top-level key)
        if [[ "$in_run_config" == true && "$line" =~ ^[[:space:]]*[a-zA-Z] && ! "$line" =~ ^[[:space:]]{2,} ]]; then
            in_run_config=false
        fi

        # Parse runConfig values
        if [[ "$in_run_config" == true ]]; then
            if [[ "$line" =~ ^[[:space:]]+([a-zA-Z][a-zA-Z0-9]*)[[:space:]]*:[[:space:]]*(.+)$ ]]; then
                key="${BASH_REMATCH[1]}"
                value="${BASH_REMATCH[2]}"

                # Convert specific camelCase keys to proper gcloud flags
                case "$key" in
                    minInstances)
                        echo "min-instances=$value"
                        ;;
                    maxInstances)
                        echo "max-instances=$value"
                        ;;
                    concurrency)
                        echo "concurrency=$value"
                        ;;
                    cpu)
                        echo "cpu=$value"
                        ;;
                    memoryMiB)
                        echo "memory=${value}Mi"
                        ;;
                esac
            fi
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
    done < <(parse_run_config "$base_config")
fi

# Parse environment config and collect flags (will override base flags)
while IFS= read -r flag_line; do
    if [[ "$flag_line" =~ ^([^=]+)=(.+)$ ]]; then
        flag="--${BASH_REMATCH[1]}=${BASH_REMATCH[2]}"
        all_flags+=("$flag")
    fi
done < <(parse_run_config "$env_config")

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
