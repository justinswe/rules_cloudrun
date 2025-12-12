"""Testing utilities for cloudrun rules."""

def cloudrun_command_test(name, target, expected_flags = [], unexpected_flags = []):
    """
    Verifies that the generated cloudrun deployment script contains expected flags.

    Args:
        name: Test target name.
        target: The cloudrun_deploy target to test.
        expected_flags: List of strings that must be present in the generated command.
        unexpected_flags: List of strings that must NOT be present.
    """
    
    # Generate the checks Starlark-side to avoid complex escaping for Bash arrays
    checks = ""
    for flag in expected_flags:
        safe_flag = flag.replace("'", "'\\''")
        checks += """
    if ! echo \"$CONTENT\" | grep -Fq -e '{flag}'; then
        echo "  Missing expected flag: '{flag}'"
        CURRENT_FAILED=1
    fi
""".format(flag = safe_flag)

    for flag in unexpected_flags:
        safe_flag = flag.replace("'", "'\\''")
        checks += """
    if echo \"$CONTENT\" | grep -Fq -e '{flag}'; then
        echo "  Found unexpected flag: '{flag}'"
        CURRENT_FAILED=1
    fi
""".format(flag = safe_flag)

    template = """
#!/bin/bash
set -euo pipefail

# PATHS contains space-separated paths from $(rootpaths)
PATHS="%PATHS%"

FOUND_PASSING_FILE=0

for SCRIPT_PATH in $PATHS; do
    echo "Examining artifact: $SCRIPT_PATH"
    
    REAL_PATH="$SCRIPT_PATH"
    if [ ! -f "$REAL_PATH" ]; then
        if [ -f "$RUNFILES_DIR/$SCRIPT_PATH" ]; then
            REAL_PATH="$RUNFILES_DIR/$SCRIPT_PATH"
        elif [ -f "$TEST_SRCDIR/$TEST_WORKSPACE/$SCRIPT_PATH" ]; then
            REAL_PATH="$TEST_SRCDIR/$TEST_WORKSPACE/$SCRIPT_PATH"
        else
            echo "  Skipping: File not found."
            continue
        fi
    fi

    CONTENT=$(cat "$REAL_PATH")
    CURRENT_FAILED=0

    {checks}

    if [ $CURRENT_FAILED -eq 0 ]; then
        echo "PASS: File '$SCRIPT_PATH' matches all criteria."
        FOUND_PASSING_FILE=1
        break
    else
        echo "--- Content of $SCRIPT_PATH ---"
        echo "$CONTENT"
        echo "-----------------------------"
    fi
done

if [ $FOUND_PASSING_FILE -eq 0 ]; then
    echo "FAIL: No output file matched all expectations."
    exit 1
fi

echo "Test passed."
"""

    content_with_checks = template.replace("{checks}", checks)
    
    # Escape $ for Bazel genrule cmd attribute
    bazel_safe_content = content_with_checks.replace("$", "$$")
    
    delimiter = "EOF"

    native.sh_test(
        name = name,
        srcs = [name + "_test_script.sh"],
        data = [target],
    )
    
    native.genrule(
        name = name + "_gen_test",
        outs = [name + "_test_script.sh"],
        srcs = [target],
        cmd = """
cat > $@ <<'{delimiter}'
{content}
{delimiter}
sed -i.bak 's|%PATHS%|$(rootpaths {target})|g' $@
rm $@.bak
""".format(
            delimiter = delimiter,
            content = bazel_safe_content,
            target = target
        ),
        executable = True,
    )
