package main

import (
	"context"

	"valinor/internal/deploy"

	"github.com/spf13/cobra"
)

var (
	logsFollow bool
	logsLines  int
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Manually download the latest release and restart the service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.RunUpdate(context.Background(), instanceName)
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service status, version, and important paths",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.PrintStatus(context.Background(), instanceName)
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show service logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.RunLogs(context.Background(), deploy.LogOptions{
			Follow: logsFollow,
			Lines:  logsLines,
		}, instanceName)
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the installed service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.RunRestart(context.Background(), instanceName)
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the installed service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.RunUninstall(context.Background(), instanceName)
	},
}

var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Restore the previous binary and restart the service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.RunRollback(context.Background(), instanceName)
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output")
	logsCmd.Flags().IntVarP(&logsLines, "lines", "n", 100, "Number of log lines to show")
}
