/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package bucket provides `adhar bucket` — self-service S3 object storage.
//
// A bucket is requested as a namespaced Crossplane CompositeStorage with
// `type: object`, which is the same contract the Console uses. On this platform
// that resolves to a bucket in RustFS, the primary in-cluster S3 store; on a cloud
// platform the same request resolves to the cloud's object store. The application
// never learns which, because it reads the endpoint and credentials out of a
// Secret either way.
package bucket

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/spf13/cobra"
)

const (
	// xrKind and xrPlural address the CompositeStorage XRD.
	xrKind   = "CompositeStorage"
	xrPlural = "compositestorages"
	// feature is the composition-selector category, matching the XRD's labels.
	feature = "storage"
	// objectType is what makes a CompositeStorage a bucket rather than a volume.
	objectType = "object"
)

var (
	bucketName      string
	bucketNamespace string
	bucketSize      string
	versioning      bool
	outputFormat    string
)

// BucketCmd is `adhar bucket`.
var BucketCmd = &cobra.Command{
	Use:     "bucket",
	Aliases: []string{"buckets", "objectstore"},
	Short:   "Self-service S3 object storage for your applications",
	Long: `Request and manage S3 buckets without filing a ticket.

A bucket is an ordinary Kubernetes resource in your namespace, so who may create
one is decided by RBAC you already have. The platform provisions it and publishes
the endpoint, bucket name and credentials as a Secret; bind that Secret to a
workload and the application is done — it never needs to know whether the bytes
land in the in-cluster store or a cloud one.

Examples:
  adhar bucket create uploads                     # a bucket named uploads
  adhar bucket create uploads --versioning        # keep old versions of objects
  adhar bucket create backups --size 100Gi        # request a quota
  adhar bucket list
  adhar bucket status uploads
  adhar bucket credentials uploads                # where the keys live
  adhar bucket delete uploads

Related:
  adhar application bind <app> <bucket>   mount the bucket's Secret into a workload
  adhar storage                    block and file volumes rather than objects`,
}

func init() {
	BucketCmd.PersistentFlags().StringVarP(&bucketNamespace, "namespace", "n", "", "Namespace (default: the platform namespace)")
	BucketCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format (table, json, yaml)")

	createCmd.Flags().StringVar(&bucketSize, "size", "", "Requested capacity, e.g. 100Gi (omit for no quota)")
	createCmd.Flags().BoolVar(&versioning, "versioning", false, "Keep previous versions of overwritten objects")

	BucketCmd.AddCommand(createCmd, listCmd, statusCmd, credentialsCmd, deleteCmd)
}

// ns resolves the namespace a bucket lives in.
func ns() string {
	if bucketNamespace != "" {
		return bucketNamespace
	}
	// The platform namespace: every platform package lives there (ADR-0011), and
	// a bucket requested without a namespace belongs with them.
	return globals.AdharSystemNamespace
}

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Request a new S3 bucket",
	Long: `Request a new S3 bucket.

The name is the Kubernetes resource name and, unless the composition says
otherwise, the bucket name too — so it must be a valid DNS label: lowercase
letters, digits and dashes.

Versioning cannot be turned on after the fact on every backend, so decide it here
if the data matters.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucketName = args[0]
		if err := validateName(bucketName); err != nil {
			return err
		}

		parameters := map[string]interface{}{
			"type": objectType,
		}
		if bucketSize != "" {
			parameters["size"] = bucketSize
		}
		if versioning {
			parameters["versioning"] = true
		}

		// The discriminator is the storage TYPE. Without it a CompositeStorage
		// request is ambiguous — block, file and object storage are composed very
		// differently — and Crossplane would either pick arbitrarily or refuse.
		obj := helpers.NewXR(xrKind, bucketName, ns(), feature,
			map[string]string{"type": objectType},
			map[string]interface{}{"parameters": parameters})

		logger.Info(fmt.Sprintf("🪣 Requesting bucket %s in %s (provider: %s)",
			bucketName, ns(), helpers.ActiveProvider()))

		if err := helpers.ApplyXR(ctx(cmd), xrPlural, obj); err != nil {
			return fmt.Errorf("requesting bucket %s: %w", bucketName, err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Bucket %s requested in namespace %s", bucketName, ns())))
		fmt.Printf("  credentials will appear in a Secret; see: adhar bucket credentials %s\n", bucketName)
		return nil
	},
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List buckets in the namespace",
	RunE: func(cmd *cobra.Command, args []string) error {
		items, err := listBuckets(ctx(cmd))
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Printf("No buckets in namespace %s\n", ns())
			return nil
		}
		switch outputFormat {
		case "json":
			return helpers.PrintJSON(items)
		case "yaml":
			return helpers.PrintYAML(items)
		}

		t := helpers.NewTable(helpers.IconStorage+" NAME", "READY", "SIZE", "VERSIONING", "AGE")
		for _, b := range items {
			t.Row(b.Name, readyLabel(b.Ready), dash(b.Size), yesNo(b.Versioning), b.Age)
		}
		fmt.Println(t.Render())
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show one bucket's readiness and connection details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		b, err := getBucket(ctx(cmd), args[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", helpers.IconStorage, b.Name)
		fmt.Printf("  namespace   %s\n", ns())
		fmt.Printf("  ready       %s\n", readyLabel(b.Ready))
		fmt.Printf("  size        %s\n", dash(b.Size))
		fmt.Printf("  versioning  %s\n", yesNo(b.Versioning))
		fmt.Printf("  endpoint    %s\n", dash(b.Endpoint))
		fmt.Printf("  secret      %s\n", dash(b.SecretName))
		if !b.Ready && b.Message != "" {
			// The composition's own words: a bucket that is not ready is usually
			// waiting on a provider credential or a quota, and paraphrasing that
			// loses the detail that identifies which.
			fmt.Printf("  waiting on  %s\n", b.Message)
		}
		return nil
	},
}

var credentialsCmd = &cobra.Command{
	Use:     "credentials <name>",
	Aliases: []string{"creds"},
	Short:   "Show where the bucket's endpoint and keys are published",
	Long: `Show the Secret holding the bucket's endpoint and access keys.

The values are NOT printed. A bucket credential is a live secret, and echoing it
into a terminal puts it in scrollback, shell history and any CI log that captured
the command. Bind the Secret to a workload instead:

  adhar application bind <app> <bucket>

To read it deliberately, ask for the one key you need:
  kubectl -n <namespace> get secret <secret> -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		b, err := getBucket(ctx(cmd), args[0])
		if err != nil {
			return err
		}
		if b.SecretName == "" {
			return fmt.Errorf("bucket %s has not published a credentials Secret yet (ready: %v)", b.Name, b.Ready)
		}
		fmt.Printf("%s %s\n", helpers.IconSecurity, b.SecretName)
		fmt.Printf("  namespace  %s\n", ns())
		if len(b.SecretKeys) > 0 {
			fmt.Printf("  keys       %s\n", strings.Join(b.SecretKeys, ", "))
		}
		fmt.Printf("\n  bind it:   adhar application bind <app> %s\n", b.Name)
		return nil
	},
}

var deleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a bucket",
	Long: `Delete a bucket.

Whether the OBJECTS go with it is the composition's decision, not this command's:
a composition may retain the underlying bucket so data is not lost to a stray
delete. Check 'adhar bucket status' output and your provider before assuming the
data is gone — or that it is safe.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		client, err := helpers.DynamicClient()
		if err != nil {
			return err
		}
		if err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).
			Delete(ctx(cmd), name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("deleting bucket %s: %w", name, err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Bucket %s deleted from namespace %s", name, ns())))
		return nil
	},
}

func ctx(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("a bucket name is required")
	}
	if len(name) > 63 {
		return fmt.Errorf("bucket name %q is too long (63 characters maximum)", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return fmt.Errorf("bucket name %q is not a valid DNS label: use lowercase letters, digits and dashes", name)
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("bucket name %q may not start or end with a dash", name)
	}
	return nil
}

// readyLabel renders the Crossplane Ready condition with the shared state icons.
func readyLabel(ready bool) string {
	if ready {
		return helpers.StateReady("Ready")
	}
	return helpers.StatePending("Pending")
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// bucketInfo is the flattened view the commands print.
type bucketInfo struct {
	Name       string   `json:"name"`
	Ready      bool     `json:"ready"`
	Size       string   `json:"size,omitempty"`
	Versioning bool     `json:"versioning"`
	Endpoint   string   `json:"endpoint,omitempty"`
	SecretName string   `json:"secretName,omitempty"`
	SecretKeys []string `json:"secretKeys,omitempty"`
	Message    string   `json:"message,omitempty"`
	Age        string   `json:"age"`
}

func listBuckets(c context.Context) ([]bucketInfo, error) {
	client, err := helpers.DynamicClient()
	if err != nil {
		return nil, err
	}
	list, err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).
		List(c, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing buckets in %s: %w", ns(), err)
	}
	var out []bucketInfo
	for i := range list.Items {
		b := flatten(&list.Items[i])
		// A CompositeStorage may be a volume rather than a bucket; only object
		// storage belongs in this command's output.
		if b.isObject {
			out = append(out, b.bucketInfo)
		}
	}
	return out, nil
}

func getBucket(c context.Context, name string) (bucketInfo, error) {
	client, err := helpers.DynamicClient()
	if err != nil {
		return bucketInfo{}, err
	}
	obj, err := client.Resource(helpers.XRGVR(xrPlural)).Namespace(ns()).
		Get(c, name, metav1.GetOptions{})
	if err != nil {
		return bucketInfo{}, fmt.Errorf("reading bucket %s in %s: %w", name, ns(), err)
	}
	f := flatten(obj)
	if !f.isObject {
		return bucketInfo{}, fmt.Errorf("%s is a %s storage request, not a bucket; use 'adhar storage' for it", name, dash(f.storageType))
	}
	return f.bucketInfo, nil
}

type flattened struct {
	bucketInfo
	isObject    bool
	storageType string
}

// flatten reads the fields the commands display out of an unstructured XR.
func flatten(obj *unstructured.Unstructured) flattened {
	params, _, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	stype, _ := params["type"].(string)
	size, _ := params["size"].(string)
	vers, _ := params["versioning"].(bool)

	ready, message := readiness(obj)
	endpoint, _, _ := unstructured.NestedString(obj.Object, "status", "endpoint")
	secret, _, _ := unstructured.NestedString(obj.Object, "status", "connectionSecret")
	if secret == "" {
		// Crossplane's own field, when a composition writes connection details
		// the standard way rather than through a status field of its own.
		secret, _, _ = unstructured.NestedString(obj.Object, "spec", "writeConnectionSecretToRef", "name")
	}

	return flattened{
		bucketInfo: bucketInfo{
			Name:       obj.GetName(),
			Ready:      ready,
			Size:       size,
			Versioning: vers,
			Endpoint:   endpoint,
			SecretName: secret,
			Message:    message,
			Age:        duration.HumanDuration(time.Since(obj.GetCreationTimestamp().Time)),
		},
		isObject:    stype == objectType,
		storageType: stype,
	}
}

// readiness reads the standard Crossplane Ready condition.
func readiness(obj *unstructured.Unstructured) (bool, string) {
	conds, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false, ""
	}
	for _, c := range conds {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			msg, _ := cond["message"].(string)
			if msg == "" {
				msg, _ = cond["reason"].(string)
			}
			return cond["status"] == "True", msg
		}
	}
	return false, ""
}
