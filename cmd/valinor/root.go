package main

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:          "valinor",
	Short:        "Valinor AI assistant",
	SilenceUsage: true,
}

var (
	instanceName string
	configPath   string
)

func init() {
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)

	rootCmd.PersistentFlags().StringVar(&instanceName, "instance", "", "Instance name (defaults to VALINOR_INSTANCE or \"default\")")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Explicit config file path (advanced; cannot be combined with --instance)")

	rootCmd.AddCommand(
		runCmd,
		initCmd,
		updateCmd,
		statusCmd,
		logsCmd,
		restartCmd,
		uninstallCmd,
		rollbackCmd,
		buildDockerImageCmd,
		versionCmd,
	)
}
