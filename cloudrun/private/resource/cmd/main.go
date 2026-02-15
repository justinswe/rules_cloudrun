package main

import (
	"fmt"
	"os"

	"github.com/justinswe/rules_cloudrun/cloudrun/private/resource"
	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	options := &resource.RenderOptions{}
	command := &cobra.Command{
		Use:           "resource_manifest",
		Short:         "Render Cloud Run Knative manifests",
		RunE:          runRenderer(options),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags := command.Flags()
	flags.StringVar(&options.ConfigPath, "config", "", "Merged apphosting config path")
	flags.StringVar(&options.ServiceName, "service-name", "", "Cloud Run service or worker name")
	flags.StringVar(&options.Region, "region", "", "Cloud Run region")
	flags.StringVar(&options.Image, "image", "", "Fully qualified image reference")
	flags.IntVar(&options.TimeoutSeconds, "timeout", 300, "Request timeout in seconds")
	flags.StringVar(&options.ResourceType, "resource-type", "service", "Cloud Run resource type")
	flags.StringVar(&options.OutputPath, "output", "", "Output manifest path")
	_ = command.MarkFlagRequired("config")
	_ = command.MarkFlagRequired("service-name")
	_ = command.MarkFlagRequired("region")
	_ = command.MarkFlagRequired("image")
	_ = command.MarkFlagRequired("output")

	return command
}

func runRenderer(options *resource.RenderOptions) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		renderer := resource.NewRenderer(nil)
		return renderer.RenderManifest(*options)
	}
}
