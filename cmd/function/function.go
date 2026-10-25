/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package function

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
)

const (
	xrKind   = "Function"
	xrPlural = "functions"
	feature  = "function"
)

var (
	functionNamespace string
	outputFormat      string

	runtime           string
	image             string
	trigger           string
	eventSource       string
	port              int
	minScale          int
	maxScale          int
	targetConcurrency int
	timeoutSeconds    int
)

// FunctionCmd is `adhar function`.
var FunctionCmd = &cobra.Command{
	Use:     "function",
	Aliases: []string{"fn", "functions"},
	Short:   "Create and manage serverless Functions on the platform",
	Long: `Functions are first-class platform resources that run on Knative Serving.

They scale to zero when idle and scale out on demand under load. The same
Function contract is used by CLI and Console, and is reconciled by the
control-plane through Crossplane compositions.`,
}

func init() {
	FunctionCmd.PersistentFlags().StringVarP(&functionNamespace, "namespace", "n", "", "Namespace (default: adhar-system)")
	FunctionCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format (table, json, yaml)")

	createCmd.Flags().StringVar(&runtime, "runtime", "node20", "Runtime (node20, python312, go122, custom)")
	createCmd.Flags().StringVar(&image, "image", "", "OCI image to run (required)")
	createCmd.Flags().StringVar(&trigger, "trigger", "http", "Trigger type (http, cloudevent, topic, bucket, cron)")
	createCmd.Flags().StringVar(&eventSource, "event-source", "", "Event source descriptor, e.g. topic://orders.v1")
	createCmd.Flags().IntVar(&port, "port", 8080, "Container port")
	createCmd.Flags().IntVar(&minScale, "min-scale", 0, "Minimum pods (0 enables scale-to-zero)")
	createCmd.Flags().IntVar(&maxScale, "max-scale", 20, "Maximum pods for autoscaling")
	createCmd.Flags().IntVar(&targetConcurrency, "target-concurrency", 100, "Target concurrency per pod")
	createCmd.Flags().IntVar(&timeoutSeconds, "timeout", 30, "Per-request timeout in seconds")
	_ = createCmd.MarkFlagRequired("image")

	FunctionCmd.AddCommand(createCmd, listCmd, statusCmd, deleteCmd)
}

func ns() string {
	if functionNamespace != "" {
		return functionNamespace
	}
	return globals.AdharSystemNamespace
}

func ctx(cmd *cobra.Command) context.Context {
	if cmd == nil {
		return context.Background()
	}
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a Function resource",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if runtime != "node20" && runtime != "python312" && runtime != "go122" && runtime != "custom" {
			return fmt.Errorf("invalid --runtime %q", runtime)
		}
		if minScale < 0 || maxScale < 1 || minScale > maxScale {
			return fmt.Errorf("invalid scale bounds: min=%d max=%d", minScale, maxScale)
		}
		if targetConcurrency < 1 {
			return fmt.Errorf("--target-concurrency must be >= 1")
		}
		if timeoutSeconds < 1 || timeoutSeconds > 900 {
			return fmt.Errorf("--timeout must be between 1 and 900 seconds")
		}

		params := map[string]interface{}{
			"runtime":           runtime,
			"image":             image,
			"trigger":           trigger,
			"eventSource":       eventSource,
			"port":              port,
			"minScale":          minScale,
			"maxScale":          maxScale,
			"targetConcurrency": targetConcurrency,
			"timeoutSeconds":    timeoutSeconds,
		}
		obj := helpers.NewXR(xrKind, name, ns(), feature, nil, map[string]interface{}{"parameters": params})

		if err := helpers.ApplyXR(ctx(cmd), xrPlural, obj); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("function %s already exists in %s", name, ns())
			}
			return fmt.Errorf("creating function %s: %w", name, err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Function %s requested in namespace %s", name, ns())))
		return nil
	},
}

type functionStatus struct {
	Name       string   `json:"name" yaml:"name"`
	Namespace  string   `json:"namespace" yaml:"namespace"`
	Runtime    string   `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Image      string   `json:"image,omitempty" yaml:"image,omitempty"`
	Ready      bool     `json:"ready" yaml:"ready"`
	URL        string   `json:"url,omitempty" yaml:"url,omitempty"`
	MinScale   int64    `json:"minScale,omitempty" yaml:"minScale,omitempty"`
	MaxScale   int64    `json:"maxScale,omitempty" yaml:"maxScale,omitempty"`
	Conditions []string `json:"conditions,omitempty" yaml:"conditions,omitempty"`
	Age        string   `json:"age,omitempty" yaml:"age,omitempty"`
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List Functions",
	RunE: func(cmd *cobra.Command, args []string) error {
		items, err := listFunctions(ctx(cmd))
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			return helpers.PrintJSON(items)
		}
		if outputFormat == "yaml" {
			return helpers.PrintYAML(items)
		}
		t := helpers.NewTable("NAME", "READY", "RUNTIME", "SCALE", "URL", "AGE")
		for _, f := range items {
			ready := helpers.StateFailed("False")
			if f.Ready {
				ready = helpers.StateReady("True")
			}
			scale := fmt.Sprintf("%d..%d", f.MinScale, f.MaxScale)
			t.Row(f.Name, ready, dash(f.Runtime), scale, dash(f.URL), f.Age)
		}
		fmt.Println(t.Render())
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show one Function status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := getFunction(ctx(cmd), args[0])
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			return helpers.PrintJSON(f)
		}
		if outputFormat == "yaml" {
			return helpers.PrintYAML(f)
		}
		helpers.Printf("%s %s\n", helpers.IconApp, f.Name)
		fmt.Printf("  namespace    %s\n", f.Namespace)
		fmt.Printf("  ready        %s\n", map[bool]string{true: helpers.StateReady("True"), false: helpers.StateFailed("False")}[f.Ready])
		fmt.Printf("  runtime      %s\n", dash(f.Runtime))
		fmt.Printf("  image        %s\n", dash(f.Image))
		fmt.Printf("  scale        %d..%d\n", f.MinScale, f.MaxScale)
		fmt.Printf("  url          %s\n", dash(f.URL))
		if len(f.Conditions) > 0 {
			fmt.Printf("  conditions   %s\n", strings.Join(f.Conditions, ", "))
		}
		return nil
	},
}

var deleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a Function",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := helpers.DynamicClient()
		if err != nil {
			return err
		}
		if err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).Delete(ctx(cmd), args[0], metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("function %s was not found in %s", args[0], ns())
			}
			return fmt.Errorf("deleting function %s: %w", args[0], err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Function %s deleted from namespace %s", args[0], ns())))
		return nil
	},
}

func listFunctions(c context.Context) ([]functionStatus, error) {
	client, err := helpers.DynamicClient()
	if err != nil {
		return nil, err
	}
	list, err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).List(c, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing functions in %s: %w", ns(), err)
	}
	out := make([]functionStatus, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, fromObject(&list.Items[i]))
	}
	return out, nil
}

func getFunction(c context.Context, name string) (*functionStatus, error) {
	client, err := helpers.DynamicClient()
	if err != nil {
		return nil, err
	}
	obj, err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).Get(c, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("function %s was not found in %s", name, ns())
		}
		return nil, fmt.Errorf("reading function %s: %w", name, err)
	}
	st := fromObject(obj)
	return &st, nil
}

func fromObject(obj *unstructured.Unstructured) functionStatus {
	st := functionStatus{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       duration.HumanDuration(time.Since(obj.GetCreationTimestamp().Time)),
	}
	st.Runtime, _, _ = unstructured.NestedString(obj.Object, "spec", "parameters", "runtime")
	st.Image, _, _ = unstructured.NestedString(obj.Object, "spec", "parameters", "image")
	st.URL, _, _ = unstructured.NestedString(obj.Object, "status", "url")
	st.MinScale, _, _ = unstructured.NestedInt64(obj.Object, "spec", "parameters", "minScale")
	st.MaxScale, _, _ = unstructured.NestedInt64(obj.Object, "spec", "parameters", "maxScale")

	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _, _ := unstructured.NestedString(m, "type")
		s, _, _ := unstructured.NestedString(m, "status")
		if t == "Ready" && s == "True" {
			st.Ready = true
		}
		if t != "" {
			st.Conditions = append(st.Conditions, fmt.Sprintf("%s=%s", t, s))
		}
	}
	return st
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
