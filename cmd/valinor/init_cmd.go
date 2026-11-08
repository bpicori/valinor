package main

import (
	"errors"
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
		if err := validateInstanceFlags(); err != nil {
			return err
		}

		var (
			initial  config.Config
			savePath string
			err      error
		)
		if path := configPath; path != "" {
			savePath = path
			initial, err = config.LoadFromPath(path)
			if errors.Is(err, config.ErrNotFound) {
				initial, err = config.LoadWizardTemplate()
			}
		} else {
			name := selectedInstanceName()
			instance, resolveErr := config.ResolveInstance(name)
			if resolveErr != nil {
				return resolveErr
			}
			savePath = instance.ConfigPath
			initial, err = config.LoadInitialForInstance(name)
		}
		if err != nil {
			return fmt.Errorf("load initial config: %w", err)
		}
		if _, err := wizard.Run(initial, savePath); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Configuration saved to %s\n", savePath)
		return nil
	},
}
