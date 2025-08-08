# Cloud Run Deployment Rules for Bazel

A Bazel ruleset for deploying applications to Google Cloud Run with environment-specific configurations.

## Overview

The `cloudrun_deploy` rule provides a simple way to deploy the same application image to multiple Cloud Run environments (dev, staging, production) with different configurations. It uses the same apphosting-style YAML configuration format as Firebase App Hosting, making it familiar for developers already using Firebase. The rule automatically generates `gcloud run deploy` commands with environment-specific settings for CPU, memory, scaling, environment variables, and secrets.

## Key Features

- **Multi-environment deployments**: Deploy the same application to multiple environments with different configurations
- **Firebase App Hosting compatibility**: Uses the same apphosting-style YAML configuration format as Firebase
- **Configuration management**: Use YAML files to define environment-specific settings
- **Secret management**: Integrate with Google Secret Manager for sensitive data
- **Cloud SQL integration**: Automatic Cloud SQL connector configuration

## Quick Start

### 1. Add to your MODULE.bazel

Since `rules_cloudrun` is not yet available in the Bazel Central Registry, you'll need to add it as a git repository dependency:

```starlark
# MODULE.bazel
module(
    name = "my_project",
    version = "1.0.0",
)

# Add rules_cloudrun from git repository
git_override(
    module_name = "rules_cloudrun",
    remote = "https://github.com/justinswe/rules_cloudrun.git",
    commit = "570e5ea4f4b65838bf305b771123efa12275124a",
)
```

### 2. Load the rule in your BUILD.bazel

```starlark
load("@rules_cloudrun//:defs.bzl", "cloudrun_deploy")
```

### 3. Create configuration files

Create YAML configuration files for your environments using the Firebase App Hosting configuration format:

**config.yaml** (base configuration):
```yaml
runConfig:
  minInstances: 0
  maxInstances: 3
  concurrency: 1000
  cpu: 1
  memoryMiB: 512

env:
  - variable: DATABASE_URL
    value: "postgresql://localhost:5432/myapp"
  - variable: LOG_LEVEL
    value: "info"
```

**config.prod.yaml** (production overrides):
```yaml
runConfig:
  memoryMiB: 2048
  cpu: 2
  minInstances: 1
  maxInstances: 10

env:
  - variable: API_KEY
    secret: projects/my-prod-project/secrets/API_KEY
  - variable: ENVIRONMENT
    value: production

serviceAccount: myapp-prod@my-prod-project.iam.gserviceaccount.com
```

### 4. Define deployment targets

```starlark
# Single environment deployment
cloudrun_deploy(
    name = "deploy_prod",
    service_name = "myapp",
    config = ":config.prod.yaml",
    base_config = ":config.yaml",
    region = "us-central1",
    source = ".",
)

# Multi-environment deployment
cloudrun_deploy(
    name = "deploy_all",
    service_name = "myapp",
    base_config = ":config.yaml",
    env_configs = {
        "dev": ":config.dev.yaml",
        "staging": ":config.staging.yaml", 
        "prod": ":config.prod.yaml",
    },
    region = "us-central1",
    source = ".",
)
```

### 5. Deploy your application

```bash
# Deploy to production
bazel run //:deploy_prod

# Deploy to all environments
bazel run //:deploy_all_dev      # Deploys to dev
bazel run //:deploy_all_staging  # Deploys to staging  
bazel run //:deploy_all_prod     # Deploys to prod
```

## Multi-Environment Deployment

The key advantage of this ruleset is the ability to deploy the same application with different configurations across multiple environments. This is achieved through:

### Configuration Inheritance

- **Base configuration**: Common settings shared across all environments
- **Environment-specific overrides**: Settings that vary per environment (CPU, memory, secrets, etc.)
- **Automatic merging**: Base config is automatically merged with environment-specific config

### Example Multi-Environment Setup

```starlark
cloudrun_deploy(
    name = "deploy_myapp",
    service_name = "myapp-service",
    base_config = ":base-config.yaml",
    env_configs = {
        "dev": ":dev-config.yaml",
        "staging": ":staging-config.yaml",
        "prod": ":prod-config.yaml",
    },
    region = "us-central1",
    source = ".",
)
```

This creates three deployment targets:
- `bazel run //:deploy_myapp_dev`
- `bazel run //:deploy_myapp_staging` 
- `bazel run //:deploy_myapp_prod`

Each target deploys the same application code but with environment-appropriate:
- Resource allocation (CPU, memory, instances)
- Environment variables and secrets
- Service accounts and permissions
- Database connections
- Scaling parameters

## Configuration Options

### Service Configuration (`runConfig`)

```yaml
runConfig:
  cpu: 1                    # CPU allocation
  memoryMiB: 512           # Memory in MiB
  minInstances: 0          # Minimum instances
  maxInstances: 10         # Maximum instances  
  concurrency: 1000        # Requests per instance
```

### Environment Variables and Secrets

```yaml
env:
  - variable: PUBLIC_VAR
    value: "some-value"
  - variable: SECRET_VAR
    secret: projects/my-project/secrets/SECRET_NAME
```

### Service Account and Cloud SQL

```yaml
serviceAccount: myapp@my-project.iam.gserviceaccount.com
cloudsqlConnector: my-project:region:instance-name
```

## Advanced Usage

### CI/CD Pipeline Integration

When using this in your CI/CD pipeline, you'll typically want to deploy the same pre-built container image across all environments (dev → staging → prod). You can pass the image URL as a runtime argument:

```bash
# In your CI/CD pipeline, after building and pushing your image
IMAGE_URL="gcr.io/my-project/myapp:${BUILD_ID}"

# Deploy to dev
bazel run //:deploy_myapp_dev -- --image="${IMAGE_URL}"

# Deploy to staging (same image)
bazel run //:deploy_myapp_staging -- --image="${IMAGE_URL}"

# Deploy to prod (same image)  
bazel run //:deploy_myapp_prod -- --image="${IMAGE_URL}"
```
Your BUILD.bazel targets don't need to specify the image - it's provided at deployment time:

```starlark
cloudrun_deploy(
    name = "deploy_myapp_dev",
    service_name = "myapp",
    config = ":config.dev.yaml", 
    base_config = ":config.yaml",
    region = "us-central1",
    # No source or image specified - provided via --image flag
)
```

### Container Image Deployment (Static)

You can also deploy from a pre-built container image statically:

```starlark
cloudrun_deploy(
    name = "deploy_from_image",
    service_name = "myapp",
    config = ":config.yaml",
    region = "us-central1",
    additional_flags = [
        "--allow-unauthenticated",
    ],
)
```

### Additional gcloud Flags

Pass additional flags to the `gcloud run deploy` command:

```starlark
cloudrun_deploy(
    name = "deploy_with_flags",
    service_name = "myapp", 
    config = ":config.yaml",
    additional_flags = [
        "--allow-unauthenticated",
        "--max-instances=20",
        "--timeout=300",
    ],
)
```

## Examples

See the [docs/examples](docs/examples) directory for complete working examples including:

- Simple single-environment deployment
- Multi-environment deployment with shared base configuration
- Container image deployment
- Integration with Google Secret Manager and Cloud SQL
