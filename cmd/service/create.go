package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var selector string

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create new service",
	Long: `Create a ClusterIP or NodePort Kubernetes Service from flags.

Examples:
  adhar service create --name=api --type=ClusterIP --port=80 --target-port=8080 --selector=app=api
  adhar service create --name=web --type=NodePort --port=80 --selector=app=web`,
	RunE: runCreate,
}

func init() {
	createCmd.Flags().StringVar(&selector, "selector", "", "Pod selector as key=value pairs (comma separated)")
}

func runCreate(cmd *cobra.Command, args []string) error {
	if serviceName == "" {
		return fmt.Errorf("--name is required for service creation")
	}
	if serviceType == "" {
		return fmt.Errorf("--type is required for service creation")
	}

	svcType, err := parseServiceType(serviceType)
	if err != nil {
		return err
	}
	if port == "" {
		return fmt.Errorf("--port is required for service creation")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("--port must be an integer: %w", err)
	}

	target := intstr.FromInt(portNum)
	if targetPort != "" {
		if tp, err := strconv.Atoi(targetPort); err == nil {
			target = intstr.FromInt(tp)
		} else {
			target = intstr.FromString(targetPort)
		}
	}

	sel, err := parseSelector(selector)
	if err != nil {
		return err
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("🌐 Creating %s service %s/%s on port %d", svcType, ns, serviceName, portNum))

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: ns,
			Labels:    map[string]string{"adhar.io/managed-by": "adhar-service"},
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: sel,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       int32(portNum),
					TargetPort: target,
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	created, err := clientset.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating service %s/%s: %w", ns, serviceName, err)
	}

	logger.Info(fmt.Sprintf("✅ Service %s/%s created (ClusterIP: %s)", ns, created.Name, clusterIP(*created)))
	return nil
}

func parseServiceType(t string) (corev1.ServiceType, error) {
	switch strings.ToLower(t) {
	case "clusterip":
		return corev1.ServiceTypeClusterIP, nil
	case "nodeport":
		return corev1.ServiceTypeNodePort, nil
	default:
		return "", fmt.Errorf("unsupported service type %q (use ClusterIP or NodePort)", t)
	}
}

func parseSelector(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			return nil, fmt.Errorf("invalid selector %q (expected key=value)", pair)
		}
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out, nil
}
