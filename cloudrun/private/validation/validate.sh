#!/usr/bin/env bash

# Validates an apphosting YAML config against the allowed schema for a given resource type.
# Usage: validate.sh --yq <path> --config <path> --resource-type service|job|worker
set -euo pipefail

YQ="" CONFIG="" RESOURCE_TYPE="service"
while [[ $# -gt 0 ]]; do
  case $1 in
    --yq) YQ="$2"; shift 2;;
    --config) CONFIG="$2"; shift 2;;
    --resource-type) RESOURCE_TYPE="$2"; shift 2;;
    *) echo "ERROR: Unknown argument: $1" >&2; exit 1;;
  esac
done

[[ -z "$YQ" ]] && { echo "ERROR: --yq is required" >&2; exit 1; }
[[ -z "$CONFIG" ]] && { echo "ERROR: --config is required" >&2; exit 1; }

# Verify config parses as valid YAML
if ! "$YQ" '.' "$CONFIG" > /dev/null 2>&1; then
  echo "ERROR: Failed to parse config file '$CONFIG' as YAML" >&2
  exit 1
fi

ERRORS=()

# ── Allowed keys per resource type ────────────────────────────────────────────

ALLOWED_TOP="runConfig env serviceAccount cloudsqlConnector"

case "$RESOURCE_TYPE" in
  service)
    ALLOWED_RUN="cpu memoryMiB minInstances maxInstances concurrency network subnet vpcConnector vpcEgress"
    ;;
  job)
    ALLOWED_RUN="cpu memoryMiB taskCount parallelism maxRetries timeoutSeconds"
    ;;
  worker)
    ALLOWED_RUN="cpu memoryMiB minInstances maxInstances network subnet vpcConnector vpcEgress"
    ;;
  *)
    echo "ERROR: Unknown resource type '$RESOURCE_TYPE'" >&2
    exit 1
    ;;
esac

# ── Top-level keys ───────────────────────────────────────────────────────────

TOP_KEYS=$("$YQ" 'keys | .[]' "$CONFIG" 2>/dev/null || true)
for key in $TOP_KEYS; do
  found=false
  for allowed in $ALLOWED_TOP; do
    if [[ "$key" == "$allowed" ]]; then
      found=true
      break
    fi
  done
  if [[ "$found" == "false" ]]; then
    ERRORS+=("Unknown top-level key '$key'. Allowed: $ALLOWED_TOP")
  fi
done

# ── runConfig keys ───────────────────────────────────────────────────────────

HAS_RUN_CONFIG=$("$YQ" '.runConfig | type' "$CONFIG" 2>/dev/null || echo "!!null")
if [[ "$HAS_RUN_CONFIG" == "!!map" ]]; then
  RUN_KEYS=$("$YQ" '.runConfig | keys | .[]' "$CONFIG" 2>/dev/null || true)
  for key in $RUN_KEYS; do
    found=false
    for allowed in $ALLOWED_RUN; do
      if [[ "$key" == "$allowed" ]]; then
        found=true
        break
      fi
    done
    if [[ "$found" == "false" ]]; then
      ERRORS+=("Unknown runConfig key '$key'. Allowed: $ALLOWED_RUN")
    fi
  done

  # ── Numeric field validation ─────────────────────────────────────────────
  for field in cpu memoryMiB minInstances maxInstances concurrency taskCount parallelism maxRetries timeoutSeconds; do
    val=$("$YQ" ".runConfig.$field // \"\"" "$CONFIG" 2>/dev/null || echo "")
    if [[ -n "$val" ]]; then
      if ! [[ "$val" =~ ^[0-9]+$ ]]; then
        ERRORS+=("runConfig.$field must be a positive integer, got '$val'")
      fi
    fi
  done

  # cpu must be 1, 2, 4, or 8
  cpu_val=$("$YQ" '.runConfig.cpu // ""' "$CONFIG" 2>/dev/null || echo "")
  if [[ -n "$cpu_val" && "$cpu_val" =~ ^[0-9]+$ ]]; then
    case "$cpu_val" in
      1|2|4|8) ;;
      *) ERRORS+=("runConfig.cpu must be 1, 2, 4, or 8, got '$cpu_val'");;
    esac
  fi

  # memoryMiB range: 128–32768
  mem_val=$("$YQ" '.runConfig.memoryMiB // ""' "$CONFIG" 2>/dev/null || echo "")
  if [[ -n "$mem_val" && "$mem_val" =~ ^[0-9]+$ ]]; then
    if (( mem_val < 128 || mem_val > 32768 )); then
      ERRORS+=("runConfig.memoryMiB must be 128–32768, got '$mem_val'")
    fi
  fi

  # network/subnet co-dependency
  net=$("$YQ" '.runConfig.network // ""' "$CONFIG" 2>/dev/null || echo "")
  sub=$("$YQ" '.runConfig.subnet // ""' "$CONFIG" 2>/dev/null || echo "")
  if [[ -n "$net" && -z "$sub" ]]; then
    ERRORS+=("runConfig.network requires runConfig.subnet")
  fi
  if [[ -z "$net" && -n "$sub" ]]; then
    ERRORS+=("runConfig.subnet requires runConfig.network")
  fi
fi

# ── env entries ──────────────────────────────────────────────────────────────

ENV_COUNT=$("$YQ" '.env | length // 0' "$CONFIG" 2>/dev/null || echo "0")
if [[ "$ENV_COUNT" =~ ^[0-9]+$ ]] && (( ENV_COUNT > 0 )); then
  for ((i=0; i<ENV_COUNT; i++)); do
    var=$("$YQ" ".env[$i].variable // \"\"" "$CONFIG")

    # Use has() to check key existence — the // operator treats false/0 as falsy
    has_val=$("$YQ" ".env[$i] | has(\"value\")" "$CONFIG")
    has_secret=$("$YQ" ".env[$i] | has(\"secret\")" "$CONFIG")
    secret=$("$YQ" ".env[$i].secret // \"\"" "$CONFIG")

    if [[ -z "$var" ]]; then
      ERRORS+=("env entry at index $i is missing 'variable'")
      continue
    fi

    if [[ "$has_val" == "true" && "$has_secret" == "true" ]]; then
      ERRORS+=("env '$var' has both 'value' and 'secret' — must have exactly one")
    elif [[ "$has_val" != "true" && "$has_secret" != "true" ]]; then
      ERRORS+=("env '$var' has neither 'value' nor 'secret' — must have exactly one")
    fi

    if [[ -n "$secret" ]]; then
      if ! [[ "$secret" =~ ^projects/[0-9]+/secrets/[a-zA-Z_][a-zA-Z0-9_-]*$ ]]; then
        ERRORS+=("env '$var' secret must match 'projects/<num>/secrets/<name>', got '$secret'")
      fi
    fi

    # Check for unknown env entry keys
    ENV_KEYS=$("$YQ" ".env[$i] | keys | .[]" "$CONFIG" 2>/dev/null || true)
    for ek in $ENV_KEYS; do
      case "$ek" in
        variable|value|secret|availability) ;;
        *) ERRORS+=("env '$var' has unknown key '$ek'. Allowed: variable, value, secret, availability");;
      esac
    done
  done
fi

# ── serviceAccount ───────────────────────────────────────────────────────────

SA=$("$YQ" '.serviceAccount // ""' "$CONFIG" 2>/dev/null || echo "")
if [[ -n "$SA" ]]; then
  if ! [[ "$SA" =~ ^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$ ]]; then
    ERRORS+=("serviceAccount must be a valid email, got '$SA'")
  fi
fi

# ── Report ───────────────────────────────────────────────────────────────────

if [[ ${#ERRORS[@]} -gt 0 ]]; then
  echo "ERROR: Config validation failed for '$CONFIG' (resource_type=$RESOURCE_TYPE):" >&2
  for err in "${ERRORS[@]}"; do
    echo "  - $err" >&2
  done
  exit 1
fi
