/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// This is a port-forward built on client-go rather than a `kubectl port-forward`
// child process.
//
// `adhar dev` does shell out to kubectl, and deliberately: its sync loop needs cp
// and exec as well, and a developer debugging a sync wants to run the identical
// command by hand. Nothing here is like that. A forward that exists for the
// lifetime of one HTTP call should not depend on another binary being installed,
// should not leave an orphan process if the CLI is killed mid-answer, and must
// report "the gateway has no ready pod" as that, rather than as kubectl's exit
// status. Doing it in-process gives all three.

type forwarder struct {
	localPort int
	stopCh    chan struct{}
	doneCh    chan struct{}
}

func (f *forwarder) Close() {
	if f == nil || f.stopCh == nil {
		return
	}
	close(f.stopCh)
	select {
	case <-f.doneCh:
	case <-time.After(2 * time.Second):
	}
	f.stopCh = nil
}

// forwardService opens a local port onto one pod backing a Service.
//
// The Service is resolved to a pod, and the Service port to that pod's container
// port, because port-forward is a pod-level subresource: forwarding "the
// Service's 8080" without translating it hits the container on the wrong port
// whenever targetPort differs from port, which for the gateway it may.
func forwardService(ctx context.Context, p *platform, name string, port int32) (*forwarder, error) {
	svc, err := p.clients.CoreV1().Services(p.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("the AI data plane Service %s/%s is not there — the ai/agentgateway package is disabled by default; enable it in your environment config and run `adhar upgrade`: %w", p.ns, name, err)
	}

	selector := labelSelector(svc.Spec.Selector)
	if selector == "" {
		return nil, fmt.Errorf("Service %s/%s has no selector, so there is no pod to forward to", p.ns, name)
	}
	pod, err := p.pickPod(ctx, selector)
	if err != nil {
		return nil, fmt.Errorf("the AI data plane has no running pod (%w) — check `kubectl -n %s get pods -l %s`", err, p.ns, selector)
	}

	target, err := targetPort(svc, pod, port)
	if err != nil {
		return nil, err
	}

	// Port 0 lets the kernel pick a free local port. A fixed port would collide
	// with a developer's own forward, and a collision here reads as "the gateway
	// is broken" when it is nothing of the sort.
	local, err := freePort()
	if err != nil {
		return nil, err
	}

	req := p.clients.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(pod.Namespace).Name(pod.Name).
		SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(p.rest)
	if err != nil {
		return nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, &url.URL{
		Scheme: req.URL().Scheme, Host: req.URL().Host, Path: req.URL().Path, RawQuery: req.URL().RawQuery,
	})

	f := &forwarder{localPort: local, stopCh: make(chan struct{}), doneCh: make(chan struct{})}
	ready := make(chan struct{})
	ports := []string{fmt.Sprintf("%d:%d", local, target)}
	pf, err := portforward.New(dialer, ports, f.stopCh, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, err
	}
	errCh := make(chan error, 1)
	go func() {
		defer close(f.doneCh)
		errCh <- pf.ForwardPorts()
	}()

	select {
	case <-ready:
		return f, nil
	case err := <-errCh:
		return nil, fmt.Errorf("port-forwarding to %s/%s: %w", pod.Namespace, pod.Name, err)
	case <-time.After(20 * time.Second):
		f.Close()
		return nil, fmt.Errorf("port-forward to %s/%s did not become ready within 20s", pod.Namespace, pod.Name)
	case <-ctx.Done():
		f.Close()
		return nil, ctx.Err()
	}
}

// targetPort maps a Service port onto the pod's container port, resolving a named
// targetPort through the pod's container definitions.
func targetPort(svc *corev1.Service, pod *corev1.Pod, port int32) (int32, error) {
	for _, sp := range svc.Spec.Ports {
		if sp.Port != port {
			continue
		}
		switch sp.TargetPort.Type {
		case intstr.Int:
			if v := sp.TargetPort.IntVal; v != 0 {
				return v, nil
			}
			return sp.Port, nil
		case intstr.String:
			for _, ctr := range pod.Spec.Containers {
				for _, cp := range ctr.Ports {
					if cp.Name == sp.TargetPort.StrVal {
						return cp.ContainerPort, nil
					}
				}
			}
			return 0, fmt.Errorf("Service %s targets port %q, which no container in pod %s declares", svc.Name, sp.TargetPort.StrVal, pod.Name)
		}
	}
	return 0, fmt.Errorf("Service %s does not expose port %d", svc.Name, port)
}

// labelSelector renders a Service selector, sorted so the same Service always
// produces the same string — it appears in error messages a user is meant to paste
// into kubectl, and a map-order-dependent one would differ run to run.
func labelSelector(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding a free local port: %w", err)
	}
	defer func() { _ = l.Close() }()
	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(portStr)
}
