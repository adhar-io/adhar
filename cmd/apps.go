/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	// Add the apps command to the root command
	AddCommand(appsCmd)

	// Add subcommands to appsCmd
	appsCmd.AddCommand(appsCreateCmd)
	appsCmd.AddCommand(appsListCmd)
	appsCmd.AddCommand(appsDeployCmd)
	appsCmd.AddCommand(appsDeleteCmd)
	appsCmd.AddCommand(appsStatusCmd)

	// Define flags for the apps command
	appsCreateCmd.Flags().StringP("template", "t", "", "Name of the application template to use (required)")
	appsCreateCmd.MarkFlagRequired("template")
	appsCreateCmd.Flags().StringP("output-dir", "d", ".", "Directory to create the application in")
	appsListCmd.Flags().StringP("output", "o", "table", "Output format (table, json, yaml)")
	appsDeployCmd.Flags().StringP("image", "i", "", "Container image to deploy (required)")
	appsDeployCmd.MarkFlagRequired("image")
	appsDeployCmd.Flags().BoolP("wait", "w", false, "Wait for the deployment to complete")
	appsDeleteCmd.Flags().BoolP("force", "f", false, "Force delete the application and its resources")
	appsStatusCmd.Flags().BoolP("watch", "w", false, "Watch for status changes")
}

// appsCmd represents the base command for application lifecycle management
var appsCmd = &cobra.Command{
	Use:     "apps",
	Aliases: []string{"app"},
	Short:   "Manage the application development lifecycle",
	Long:    `Provides commands to create, list, deploy, delete, and check the status of applications within the Adhar platform.`,
	Run: func(cmd *cobra.Command, args []string) {
		// By default, if no subcommand is given, show help for apps
		cmd.Help()
	},
}

// appsCreateCmd represents the command to create a new application
var appsCreateCmd = &cobra.Command{
	Use:   "create [org] [space] [app-name]",
	Short: "Create a new application from a template",
	Long:  `Scaffolds a new application based on a specified template within a given organization and space.`,
	Args:  cobra.ExactArgs(3), // Expects org, space, and app-name
	Example: `  # Create a spring-boot-api named 'my-api' in 'myorg/myspace'
  adhar apps create myorg myspace my-api --template spring-boot-api

  # Create a nextjs-frontend named 'web-ui'
  adhar apps create myorg myspace web-ui --template nextjs-frontend --output-dir ./my-apps`, // Added output-dir example
	Run: func(cmd *cobra.Command, args []string) {
		org := args[0]
		space := args[1]
		appName := args[2]
		template, _ := cmd.Flags().GetString("template")
		outputDir, _ := cmd.Flags().GetString("output-dir")

		// Placeholder for actual create logic
		fmt.Printf("Creating application '%s' in org '%s', space '%s' using template '%s' in directory '%s'...\n", appName, org, space, template, outputDir)
		// TODO: Implement application creation logic (e.g., clone template, update placeholders)
	},
}

// appsListCmd represents the command to list applications
var appsListCmd = &cobra.Command{
	Use:     "list [org] [space]",
	Aliases: []string{"ls"},
	Short:   "List applications in an organization and space",
	Long:    `Retrieves and displays a list of applications managed by Adhar within the specified organization and space.`,
	Args:    cobra.ExactArgs(2), // Expects org and space
	Example: `  # List all applications in 'myorg/myspace'
  adhar apps list myorg myspace

  # List applications in JSON format
  adhar apps list myorg myspace -o json`, // Added output format example
	Run: func(cmd *cobra.Command, args []string) {
		org := args[0]
		space := args[1]
		outputFormat, _ := cmd.Flags().GetString("output")

		// Placeholder for actual list logic
		fmt.Printf("Listing applications in org '%s', space '%s' (output: %s)...\n", org, space, outputFormat)
		// TODO: Implement logic to fetch and display applications from the cluster/config
	},
}

// appsDeployCmd represents the command to deploy an application
var appsDeployCmd = &cobra.Command{
	Use:   "deploy [org] [space] [app-name]",
	Short: "Deploy an application to a target environment",
	Long:  `Deploys a specified version (container image) of an application to the target Kubernetes environment associated with the organization and space.`,
	Args:  cobra.ExactArgs(3), // Expects org, space, and app-name
	Example: `  # Deploy the latest image of 'my-api' to 'myorg/myspace'
  adhar apps deploy myorg myspace my-api --image=mycompany/my-api:latest

  # Deploy a specific version and wait for rollout
  adhar apps deploy myorg myspace my-api --image=mycompany/my-api:v1.2.0 --wait`, // Added wait flag example
	Run: func(cmd *cobra.Command, args []string) {
		org := args[0]
		space := args[1]
		appName := args[2]
		image, _ := cmd.Flags().GetString("image")
		wait, _ := cmd.Flags().GetBool("wait")

		// Placeholder for actual deploy logic
		fmt.Printf("Deploying application '%s' in org '%s', space '%s' with image '%s' (wait: %t)...\n", appName, org, space, image, wait)
		// TODO: Implement deployment logic (e.g., update Kubernetes Deployment/Application CR)
	},
}

// appsDeleteCmd represents the command to delete an application
var appsDeleteCmd = &cobra.Command{
	Use:     "delete [org] [space] [app-name]",
	Aliases: []string{"rm"},
	Short:   "Delete an application",
	Long:    `Removes an application's configuration and potentially its deployed resources from the Adhar platform and target environment.`,
	Args:    cobra.ExactArgs(3), // Expects org, space, and app-name
	Example: `  # Delete the application 'my-api' from 'myorg/myspace'
  adhar apps delete myorg myspace my-api

  # Force deletion even if resources are protected
  adhar apps delete myorg myspace my-api --force`, // Added force flag example
	Run: func(cmd *cobra.Command, args []string) {
		org := args[0]
		space := args[1]
		appName := args[2]
		force, _ := cmd.Flags().GetBool("force")

		// Placeholder for actual delete logic
		fmt.Printf("Deleting application '%s' in org '%s', space '%s' (force: %t)...\n", appName, org, space, force)
		// TODO: Implement deletion logic (e.g., delete Kubernetes resources, remove config)
	},
}

// appsStatusCmd represents the command to check the status of an application
var appsStatusCmd = &cobra.Command{
	Use:   "status [org] [space] [app-name]",
	Short: "Check the deployment status of an application",
	Long:  `Retrieves and displays the current status of an application's deployment in the target environment.`,
	Args:  cobra.ExactArgs(3), // Expects org, space, and app-name
	Example: `  # Check the status of 'my-api' in 'myorg/myspace'
  adhar apps status myorg myspace my-api

  # Watch the status updates
  adhar apps status myorg myspace my-api -w`, // Added watch flag example
	Run: func(cmd *cobra.Command, args []string) {
		org := args[0]
		space := args[1]
		appName := args[2]
		watch, _ := cmd.Flags().GetBool("watch")

		// Placeholder for actual status logic
		fmt.Printf("Checking status for application '%s' in org '%s', space '%s' (watch: %t)...\n", appName, org, space, watch)
		// TODO: Implement status checking logic (e.g., query Kubernetes API for deployment status)
	},
}
