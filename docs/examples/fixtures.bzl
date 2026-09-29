"""A deterministic OCI provider fixture for manifest examples."""

load("@rules_img//img:providers.bzl", "ImageIndexInfo", "ImageManifestInfo")

def _example_image_impl(ctx):
    config = ctx.actions.declare_file(ctx.label.name + ".config.json")
    ctx.actions.write(config, json.encode({
        "os": ctx.attr.os,
        "architecture": ctx.attr.architecture,
        "config": {"Entrypoint": ["/app/server"]},
    }))
    digest = ctx.actions.declare_file(ctx.label.name + "_digest")
    ctx.actions.write(digest, ctx.attr.digest + "\n")
    return [
        DefaultInfo(files = depset([config])),
        ImageManifestInfo(os = ctx.attr.os, architecture = ctx.attr.architecture, config = config),
        # rules_img image targets expose their digest in this output group.
        OutputGroupInfo(digest = depset([digest])),
    ]

example_image = rule(
    implementation = _example_image_impl,
    attrs = {
        "os": attr.string(default = "linux"),
        "architecture": attr.string(default = "amd64"),
        "digest": attr.string(default = "sha256:46e099f6d3eab8fc3246ad867aace15a5503a4cf6c7c54f2ffac5c28ea1facad"),
    },
)

def _example_index_impl(ctx):
    return [ImageIndexInfo(manifests = [image[ImageManifestInfo] for image in ctx.attr.images])]

example_index = rule(
    implementation = _example_index_impl,
    attrs = {"images": attr.label_list(providers = [ImageManifestInfo])},
)
