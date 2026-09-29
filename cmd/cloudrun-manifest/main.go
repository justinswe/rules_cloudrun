// Command cloudrun-manifest renders and validates one Cloud Run v2 manifest as a Bazel build action.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/justinswe/std/app"
	"github.com/justinswe/rules_cloudrun/internal/manifest"
	"github.com/spf13/cobra"
)

func main() {
	if err := app.RunCobraCommand(context.Background(), newRootCommand()); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// newRootCommand declares the render inputs as flags.
func newRootCommand() *cobra.Command {
	options := manifest.RenderOptions{}
	command := &cobra.Command{
		Use:           "cloudrun-manifest",
		Short:         "Render native Cloud Run v2 JSON",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(_ *cobra.Command, _ []string) error { return manifest.RenderFile(options) },
	}

	flags := command.Flags()
	flags.StringVar(&options.ConfigPath, "config", "", "Configuration YAML path")
	flags.StringVar(&options.BaseConfigPath, "base-config", "", "Base YAML path")
	flags.StringVar(&options.ServiceName, "service-name", "", "Cloud Run resource ID")
	flags.StringVar(&options.ResourceType, "resource-type", "service", "service, job, or worker")
	flags.StringVar(&options.Image, "image", "", "Container image override")
	flags.StringVar(&options.ImageRepo, "image-repo", "", "Repository for a digest-pinned image")
	flags.StringVar(&options.ImageDigestPath, "image-digest", "", "Bazel image digest file")
	flags.StringVar(&options.ImageConfigPath, "image-config", "", "OCI image configuration for runtime compatibility checks")
	flags.StringVar(&options.ImageContainer, "image-container", "", "Container selected for image override")
	flags.StringVar(&options.OutputPath, "output", "", "Output JSON path")
	return command
}
