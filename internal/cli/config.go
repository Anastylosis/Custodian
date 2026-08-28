package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/spf13/cobra"
)

// newConfigCmd builds the `custodian config` subcommand tree.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Write, locate, print and check the config file",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write a default config file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := flagConfigPath
			if path == "" {
				path = config.DefaultConfigInitPath()
			}
			if err := config.EnsureDir(path); err != nil {
				return fmt.Errorf("creating config directory: %w", err)
			}
			if err := config.WriteDefault(path); err != nil {
				if errors.Is(err, os.ErrExist) {
					return fmt.Errorf("%s already exists; refusing to overwrite (delete or move it first)", path)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote default config to %s\n", path)
			fmt.Fprintf(cmd.OutOrStdout(), "edit stash.url to point at your Stash instance, then run `custodian doctor`\n")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config file path in use",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := flagConfigPath
			if path == "" {
				path = config.DefaultConfigPath()
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the effective config — defaults plus your overrides",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := flagConfigPath
			if path == "" {
				path = config.DefaultConfigPath()
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			// Round-trip via the loader's struct so the user sees what's
			// actually in effect, not what's literally in the file. This is
			// the easiest way to debug "is my override being applied?".
			out, err := config.MarshalYAML(cfg)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Check the config for retired keys and bad values",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := flagConfigPath
			if path == "" {
				path = config.DefaultConfigPath()
			}
			problems, err := config.Check(path)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(problems) == 0 {
				fmt.Fprintf(out, "%s: ok\n", path)
				return nil
			}
			fmt.Fprintf(out, "%s\n\n", path)
			for _, p := range problems {
				fmt.Fprintf(out, "  %-9s %s\n", p.Key, p.Detail)
			}
			fmt.Fprintf(out, "\n`custodian config init --config <new path>` writes the current shape.\n")
			return fmt.Errorf("%d problem(s) in %s", len(problems), path)
		},
	})

	return cmd
}
