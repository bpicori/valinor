package main

import (
	"fmt"
	"os"

	"valinor/internal/config"
	"valinor/internal/wizard"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Run the configuration setup wizard",
	RunE: func(cmd *cobra.Command, args []string) error {
		initial, err := config.LoadInitial()
		if err != nil {
			return fmt.Errorf("load initial config: %w", err)
		}
		if _, err := wizard.Run(initial); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Configuration saved to ~/.valinor/config.toml")
		return nil
	},
}