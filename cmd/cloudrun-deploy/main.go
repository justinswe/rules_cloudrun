// Command cloudrun-deploy applies a rendered Cloud Run v2 manifest through the Cloud Run v2 API.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/justinswe/rules_cloudrun/internal/deploy"
	"github.com/justinswe/rules_cloudrun/internal/runapi"
	"github.com/justinswe/std/app"
	"github.com/justinswe/std/errors"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2/google"
)

func main() {
	if err := app.RunCobraCommand(context.Background(), newCommand()); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// newCommand declares the deployment inputs as flags.
func newCommand() *cobra.Command {
	var (
		options      deploy.Options
		manifestPath string
		labels       map[string]string
		timeout      time.Duration
	)
	command := &cobra.Command{
		Use:           "cloudrun-deploy",
		Short:         "Apply a rendered Cloud Run v2 manifest",
		Long:          "Creates or updates a Cloud Run Service, Job, or WorkerPool from a rendered manifest and waits until it is ready.\nTwo or more --region values deploy a Service as one multi-region Service at locations/global.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return errors.New("rollout-timeout must be positive")
			}
			body, err := deploy.ReadManifest(manifestPath)
			if err != nil {
				return err
			}
			options.Manifest = body
			options.Kind = deploy.Kind(options.Kind)
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			api, err := cloudRunAPI(ctx, labels)
			if err != nil {
				return err
			}
			return deploy.Run(ctx, api, options, cmd.OutOrStdout())
		},
	}
	flags := command.Flags()
	flags.StringVar(&manifestPath, "manifest", "", "Rendered manifest JSON")
	flags.StringVar(&options.Project, "project", "", "Cloud Run project")
	flags.StringArrayVar(&options.Regions, "region", nil, "Cloud Run region; repeat for a multi-region service")
	flags.StringVar(&options.Name, "name", "", "Cloud Run resource ID")
	flags.StringVar(&options.Kind, "kind", "service", "service, job, or workerpool")
	flags.StringVar(&options.Image, "image", "", "Image override pinned by digest (repo@sha256:...)")
	flags.StringVar(&options.ImageContainer, "image-container", "", "Container that receives --image; needed for multiple containers")
	flags.StringToStringVar(&labels, "label", nil, "Label key=value set on the resource; repeatable")
	flags.DurationVar(&timeout, "rollout-timeout", 30*time.Minute, "Maximum time to apply and wait for readiness")
	flags.BoolVar(&options.ValidateOnly, "validate-only", false, "Validate with Cloud Run without deploying; refused for multi-region services")
	return command
}

// cloudRunAPI authenticates a Cloud Run v2 REST client with Application Default Credentials.
func cloudRunAPI(ctx context.Context, labels map[string]string) (*runapi.API, error) {
	client, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, errors.Wrap(err, "authenticate Cloud Run client")
	}
	return &runapi.API{Client: client, BaseURL: "https://run.googleapis.com", Labels: labels}, nil
}
