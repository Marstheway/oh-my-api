package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Marstheway/oh-my-api/internal/configmigrate"
)

func runMigrateRules(configPath string, args []string) {
	fs := flag.NewFlagSet("migrate-rules", flag.ExitOnError)
	write := fs.Bool("write", false, "write migrated config back to the config file")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result, err := configmigrate.MigrateFile(configPath, *write)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if result.Message != "" {
		fmt.Fprintln(os.Stderr, result.Message)
	}

	if result.Changed {
		if *write {
			fmt.Fprintf(os.Stderr, "Wrote migrated config to %s\n", configPath)
		} else {
			if _, err := os.Stdout.Write(result.Output); err != nil {
				fmt.Fprintf(os.Stderr, "Error: write stdout: %v\n", err)
				os.Exit(1)
			}
		}
	}
}
