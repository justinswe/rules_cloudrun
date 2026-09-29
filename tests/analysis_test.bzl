"""Analysis tests for native render providers and hermetic actions."""

load("@bazel_skylib//lib:unittest.bzl", "analysistest", "asserts", "unittest")
load("//:defs.bzl", "CloudRunManifestInfo", "extract_env_name")

def _render_test_impl(ctx):
    env = analysistest.begin(ctx)
    target = analysistest.target_under_test(env)
    info = target[CloudRunManifestInfo]
    asserts.equals(env, "app", info.resource_name)
    asserts.equals(env, "service", info.resource_type)
    asserts.equals(env, "app", info.image_container)
    actions = analysistest.target_actions(env)
    asserts.equals(env, 1, len(actions))
    asserts.equals(env, "CloudRunRender", actions[0].mnemonic)
    asserts.true(env, "example.com/app:literal$VALUE" in actions[0].argv)
    for flag in ["--config", "--base-config", "--service-name", "--resource-type", "--image", "--image-repo", "--image-digest", "--image-config", "--image-container", "--output", "--log-level", "--debug-format=false"]:
        asserts.true(env, flag in actions[0].argv, "render action must pass " + flag + " explicitly")
    asserts.equals(env, 1, len([f for f in actions[0].inputs.to_list() if f.basename == "manifest.yaml"]))
    asserts.true(env, info.manifest.basename.endswith(".json"))
    return analysistest.end(env)

render_analysis_test = analysistest.make(_render_test_impl)

def _invalid_test_impl(ctx):
    env = analysistest.begin(ctx)
    asserts.expect_failure(env, "image_repo requires image_target")
    return analysistest.end(env)

invalid_image_analysis_test = analysistest.make(_invalid_test_impl, expect_failure = True)

def _env_test_impl(ctx):
    env = unittest.begin(ctx)
    asserts.equals(env, "dev", extract_env_name(":manifest.dev.yaml", "manifest.*.yaml"))
    asserts.equals(env, "prd", extract_env_name("//package:manifest.prd.yaml", "manifest.*.yaml"))
    asserts.equals(env, "stage", extract_env_name("dir/config-stage.yml", "config-*.yml"))
    return unittest.end(env)

environment_name_test = unittest.make(_env_test_impl)

def _runtime_image_test_impl(ctx):
    env = analysistest.begin(ctx)
    action = analysistest.target_actions(env)[0]
    config = [f for f in action.inputs.to_list() if f.basename == "linux_image.config.json"]
    asserts.equals(env, 1, len(config))
    asserts.true(env, "--image-config" in action.argv)
    asserts.true(env, config[0].path in action.argv)
    return analysistest.end(env)

runtime_image_test = analysistest.make(_runtime_image_test_impl)

def _invalid_runtime_image_test_impl(ctx):
    env = analysistest.begin(ctx)
    asserts.expect_failure(env, ctx.attr.message)
    return analysistest.end(env)

invalid_runtime_image_test = analysistest.make(
    _invalid_runtime_image_test_impl,
    expect_failure = True,
    attrs = {"message": attr.string()},
)

def _deploy_test_impl(ctx):
    env = analysistest.begin(ctx)
    info = analysistest.target_under_test(env)[DefaultInfo]
    asserts.true(env, info.files_to_run.executable != None, "cloudrun_deploy must be executable")
    runfiles = [f.short_path for f in info.default_runfiles.files.to_list()]
    asserts.true(env, ctx.attr.manifest in runfiles, "runfiles must include the rendered manifest")
    asserts.equals(env, 1, len([path for path in runfiles if path.endswith("/cloudrun-deploy")]), "runfiles must include the deploy binary")
    scripts = [action.content for action in analysistest.target_actions(env) if action.mnemonic == "FileWrite"]
    asserts.equals(env, 1, len(scripts))
    for arg in ctx.attr.expected_args:
        asserts.true(env, "'" + arg + "'" in scripts[0], "script must pass " + arg)
    return analysistest.end(env)

deploy_test = analysistest.make(
    _deploy_test_impl,
    attrs = {
        "expected_args": attr.string_list(),
        "manifest": attr.string(),
    },
)
