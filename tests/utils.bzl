"""Testing utilities for cloudrun rules."""

load("@rules_shell//shell:sh_test.bzl", "sh_test")

def cloudrun_manifest_test(name, render_target, expected):
    """Tests that a rendered manifest matches a golden file.

    Args:
        name: Test target name.
        render_target: The .render target that produces the manifest YAML.
        expected: Label pointing to the expected golden file.
    """
    native.genrule(
        name = name + "_gen_test",
        outs = [name + "_test.sh"],
        srcs = [render_target, expected],
        cmd = """
cat > $@ <<'TESTEOF'
#!/bin/bash
set -euo pipefail
ACTUAL="$(rootpath {render})"
EXPECTED="$(rootpath {expected})"

if ! diff -u "$$EXPECTED" "$$ACTUAL"; then
    echo ""
    echo "FAIL: Manifest does not match golden file."
    echo "  Actual:   {render}"
    echo "  Expected: {expected}"
    exit 1
fi

echo "PASS: Manifest matches golden file."
TESTEOF
""".format(
            render = render_target,
            expected = expected,
        ),
        executable = True,
    )

    sh_test(
        name = name,
        srcs = [":" + name + "_gen_test"],
        data = [render_target, expected],
    )

def cloudrun_deploy_script_test(name, deploy_target, expected_substrings = [], unexpected_substrings = []):
    """Tests a generated deploy script for required and forbidden text.

    Args:
        name: Test target name.
        deploy_target: The .deploy target to inspect.
        expected_substrings: Substrings that must be present in the script.
        unexpected_substrings: Substrings that must be absent from the script.
    """
    checks = ""
    for text in expected_substrings:
        safe = text.replace("$", "$$").replace("'", "'\\''")
        checks += """
if ! grep -Fq -- '{text}' "$$SCRIPT_PATH"; then
    echo "FAIL: missing expected text: {text}"
    FAIL=1
fi
""".format(text = safe)

    for text in unexpected_substrings:
        safe = text.replace("$", "$$").replace("'", "'\\''")
        checks += """
if grep -Fq -- '{text}' "$$SCRIPT_PATH"; then
    echo "FAIL: found forbidden text: {text}"
    FAIL=1
fi
""".format(text = safe)

    native.genrule(
        name = name + "_gen_test",
        outs = [name + "_test.sh"],
        srcs = [deploy_target],
        cmd = """
cat > $@ <<'TESTEOF'
#!/bin/bash
set -euo pipefail
SCRIPT_PATH="$(rootpath {deploy_target})"
FAIL=0

{checks}

if [ "$$FAIL" -ne 0 ]; then
    exit 1
fi

echo "PASS: deploy script checks succeeded."
TESTEOF
""".format(
            deploy_target = deploy_target,
            checks = checks,
        ),
        executable = True,
    )

    sh_test(
        name = name,
        srcs = [":" + name + "_gen_test"],
        data = [deploy_target],
    )

def cloudrun_validation_test(name, config, resource_type = "service", expected_errors = []):
    """Tests that validation fails with expected error messages.

    Args:
        name: Test target name.
        config: Label of the invalid config to validate.
        resource_type: Resource type to validate against.
        expected_errors: List of substrings expected in stderr output.
    """
    error_checks = ""
    for err in expected_errors:
        safe = err.replace("'", "'\\''")
        error_checks += """
    if ! echo "$$STDERR" | grep -Fq '{err}'; then
        echo "  Missing expected error: '{err}'"
        echo "  Actual stderr:"
        echo "$$STDERR"
        FAIL=1
    fi
""".format(err = safe)

    native.genrule(
        name = name + "_gen_test",
        outs = [name + "_test.sh"],
        srcs = [config],
        tools = [
            "//cloudrun/private/validation:validate",
            "@yq//:yq",
        ],
        cmd = """
cat > $@ <<'TESTEOF'
#!/bin/bash
set -euo pipefail
YQ="$(rootpath @yq//:yq)"
VALIDATE="$(rootpath //cloudrun/private/validation:validate)"
CONFIG="$(rootpath {config})"
FAIL=0

# Validation should fail (exit non-zero)
if STDERR=$$($$VALIDATE --yq $$YQ --config $$CONFIG --resource-type {resource_type} 2>&1); then
    echo "FAIL: Validation should have failed but succeeded."
    exit 1
fi

echo "Validation correctly failed. Checking error messages..."

{error_checks}

if [ $$FAIL -ne 0 ]; then
    echo "FAIL: Not all expected errors were present."
    exit 1
fi

echo "PASS: Validation failed with all expected errors."
TESTEOF
""".format(
            config = config,
            resource_type = resource_type,
            error_checks = error_checks,
        ),
        executable = True,
    )

    sh_test(
        name = name,
        srcs = [":" + name + "_gen_test"],
        data = [
            config,
            "//cloudrun/private/validation:validate",
            "@yq//:yq",
        ],
    )
