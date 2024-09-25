package cmd

import (
	"fmt"
	"os"

	"github.com/adhar-io/adhar/pkg/cmd/create"
	"github.com/adhar-io/adhar/pkg/cmd/delete"
	"github.com/adhar-io/adhar/pkg/cmd/get"
	"github.com/adhar-io/adhar/pkg/cmd/helpers"
	"github.com/adhar-io/adhar/pkg/cmd/version"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "adhar",
	Short:   "Adhar CLI",
	Long:    "Adhar Internal Developer Platform - The Open Foundation!",
	Aliases: []string{"a", "ad"},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&helpers.LogLevel, "log-level", "l", "info", helpers.LogLevelMsg)
	rootCmd.PersistentFlags().BoolVarP(&helpers.ColoredOutput, "color", "c", true, helpers.ColoredOutputMsg)
	rootCmd.AddCommand(create.CreateCmd)
	rootCmd.AddCommand(get.GetCmd)
	rootCmd.AddCommand(delete.DeleteCmd)
	rootCmd.AddCommand(version.VersionCmd)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
