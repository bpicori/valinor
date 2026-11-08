package main

import (
	"context"

	"valinor/internal/deploy"

	"github.com/spf13/cobra"
)

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage the systemd user service for an instance",
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the template unit and enable this instance",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		if err := validateInstanceFlags(); err != nil {
			return err
		}
		return deploy.RunInstall(context.Background(), selectedInstanceName())
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Disable this instance service and keep its data",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		if err := validateInstanceFlags(); err != nil {
			return err
		}
		return deploy.RunUninstall(context.Background(), selectedInstanceName())
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service status for one or all instances",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.PrintStatus(context.Background(), instanceName)
	},
}

var serviceRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart this instance service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.RunRestart(context.Background(), selectedInstanceName())
	},
}

var serviceLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show logs for this instance service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigForService(); err != nil {
			return err
		}
		return deploy.RunLogs(context.Background(), deploy.LogOptions{
			Follow: logsFollow,
			Lines:  logsLines,
		}, selectedInstanceName())
	},
}

func init() {
	serviceLogsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output")
	serviceLogsCmd.Flags().IntVarP(&logsLines, "lines", "n", 100, "Number of log lines to show")
	serviceCmd.AddCommand(serviceInstallCmd, serviceUninstallCmd, serviceStatusCmd, serviceRestartCmd, serviceLogsCmd)
	rootCmd.AddCommand(serviceCmd)
}
