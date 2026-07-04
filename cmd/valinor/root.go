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

func init() {
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)

	rootCmd.AddCommand(
		runCmd,
		initCmd,
		installCmd,
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