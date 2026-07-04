package main

import (
	"context"
	"fmt"
	"os"

	"valinor/internal/buildinfo"
	"valinor/internal/sandbox"

	"github.com/spf13/cobra"
)

var buildDockerImageCmd = &cobra.Command{
	Use:   "build-docker-image",
	Short: "Build the Podman sandbox image for this version",
	RunE: func(cmd *cobra.Command, args []string) error {
		image := buildinfo.SandboxImage()
		fmt.Fprintf(os.Stdout, "Building sandbox image %q with Podman...\n", image)
		if err := sandbox.BuildImage(context.Background(), image, os.Stdout, os.Stderr); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Sandbox image %q built successfully.\n", image)
		return nil
	},
}