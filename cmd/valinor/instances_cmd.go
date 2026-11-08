package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"valinor/internal/config"
	"valinor/internal/deploy"

	"github.com/spf13/cobra"
)

var instancesCmd = &cobra.Command{
	Use:   "instances",
	Short: "List configured Valinor instances",
	RunE: func(cmd *cobra.Command, args []string) error {
		instances, err := config.ListInstances()
		if err != nil {
			return err
		}
		if len(instances) == 0 {
			fmt.Fprintln(os.Stdout, "No instances configured. Run `valinor init --instance <name>`.")
			return nil
		}

		filter := selectedInstanceName()
		if instanceName == "" && os.Getenv("VALINOR_INSTANCE") == "" {
			filter = ""
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPORT\tTELEGRAM\tSERVICE\tCONFIG")
		for _, instance := range instances {
			if filter != "" && instance.Name != filter {
				continue
			}
			cfg, err := config.LoadForInstance(instance.Name)
			if err != nil {
				fmt.Fprintf(w, "%s\t?\t?\t%s\t%s (error: %v)\n", instance.Name, instance.Name, instance.ConfigPath, err)
				continue
			}
			telegram := "disabled"
			if cfg.Telegram.Enabled {
				telegram = "enabled"
			}
			service, stateErr := deploy.ServiceState(cmd.Context(), instance.Name)
			if stateErr != nil {
				service = "unknown"
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n",
				instance.Name, cfg.GatewayPort, telegram, service, instance.ConfigPath)
		}
		return w.Flush()
	},
}

func init() {
	rootCmd.AddCommand(instancesCmd)
}
