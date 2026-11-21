/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/batcharchive"
	platformevents "adhar-io/adhar/platform/events"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// The run-summary archive as a second source of `adhar batch history` (design
// 0025 Phase 4). The batch observer writes each finished run's final event to
// RustFS (platform/batcharchive); this reads it back for what the topic's 90
// days no longer hold.
//
// The archive is read with the platform's object-store credential, Secret
// adhar-system/root-creds, through a port-forward to the RustFS pod — both
// with the kubeconfig's own identity, so only someone who may read that Secret
// and forward to that pod (a platform operator) reads it. Anyone else gets the
// bus's history and one line saying the archive was not consulted and why;
// the Console's History view reads the archive for them, filtered to the
// namespaces they may see.

const (
	rustfsService = "rustfs"
	rustfsPort    = 9000
)

var (
	historyArchive    bool
	historyArchiveURL string
)

func init() {
	historyCmd.Flags().BoolVar(&historyArchive, "archive", true, "Also read run summaries archived in the object store, beyond the bus's retention (needs read access to adhar-system/root-creds)")
	historyCmd.Flags().StringVar(&historyArchiveURL, "archive-url", "", "Object-store base URL to read the archive from (default: a port-forward to the platform's RustFS)")
}

// readBatchArchive reads one job's archived runs. A variable so the tests can
// stand in for the store.
var readBatchArchive = func(c context.Context, cl *clients, namespace, job string, since time.Time, limit int) ([]platformevents.Event, []error, error) {
	creds, err := archiveCredentials(c, cl)
	if err != nil {
		return nil, nil, err
	}
	endpoint := strings.TrimRight(historyArchiveURL, "/")
	if endpoint == "" {
		fwd, err := forwardToRustFS(c, cl)
		if err != nil {
			return nil, nil, err
		}
		defer fwd.close()
		endpoint = fmt.Sprintf("http://127.0.0.1:%d", fwd.localPort)
	}
	store := &batcharchive.Client{
		Endpoint:    endpoint,
		Credentials: func(context.Context) (batcharchive.Credentials, error) { return creds, nil },
	}
	return batcharchive.ReadJob(c, store, namespace, job, since, limit)
}

func archiveCredentials(c context.Context, cl *clients) (batcharchive.Credentials, error) {
	s, err := cl.cs.CoreV1().Secrets(globals.AdharSystemNamespace).Get(c, batcharchive.CredentialSecret, metav1.GetOptions{})
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return batcharchive.Credentials{}, fmt.Errorf("reading it needs Secret %s/%s, which your identity may not read (platform operators can; the Console's History view shows it to you)", globals.AdharSystemNamespace, batcharchive.CredentialSecret)
	case apierrors.IsNotFound(err):
		return batcharchive.Credentials{}, fmt.Errorf("this cluster has no object store (Secret %s/%s is absent)", globals.AdharSystemNamespace, batcharchive.CredentialSecret)
	case err != nil:
		return batcharchive.Credentials{}, err
	}
	creds := batcharchive.Credentials{AccessKey: string(s.Data[batcharchive.CredentialAccessKey]), SecretKey: string(s.Data[batcharchive.CredentialSecretKey])}
	if creds.AccessKey == "" || creds.SecretKey == "" {
		return creds, fmt.Errorf("Secret %s/%s carries no %s/%s", globals.AdharSystemNamespace, batcharchive.CredentialSecret, batcharchive.CredentialAccessKey, batcharchive.CredentialSecretKey)
	}
	return creds, nil
}

/* ───────────────────────────── merging ───────────────────────────── */

// historyEvent is one line of history: an event, and whether it came from
// the archive. Embedded, so `-o json` is the CloudEvent as before, with
// `"archived": true` on the ones the bus no longer had.
type historyEvent struct {
	platformevents.Event
	Archived bool `json:"archived,omitempty"`
}

// mergeHistory joins the bus's events with the archive's: an archived event
// the bus still carries is the SAME event (same id — the observer archives
// what it publishes), so it is shown once, as the bus's. Oldest first, the
// newest `limit` (0: all).
func mergeHistory(bus, archived []platformevents.Event, limit int) []historyEvent {
	seen := make(map[string]bool, len(bus))
	out := make([]historyEvent, 0, len(bus)+len(archived))
	for _, e := range bus {
		if e.ID != "" {
			seen[e.ID] = true
		}
		out = append(out, historyEvent{Event: e})
	}
	for _, e := range archived {
		if e.ID != "" && seen[e.ID] {
			continue
		}
		out = append(out, historyEvent{Event: e, Archived: true})
	}
	at := func(e historyEvent) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, e.Time)
		return t
	}
	sort.SliceStable(out, func(i, j int) bool { return at(out[i]).Before(at(out[j])) })
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

/* ─────────────────────────── port-forward ─────────────────────────── */

// A port-forward built on client-go, as `adhar ai` does (cmd/ai/portforward.go):
// it lives for one read, needs no kubectl, and leaves nothing behind if the
// CLI is killed. The Service is resolved to a running pod and its port to the
// container's, because port-forward is a pod subresource.

type forward struct {
	localPort int
	stopCh    chan struct{}
	doneCh    chan struct{}
}

func (f *forward) close() {
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

func forwardToRustFS(c context.Context, cl *clients) (*forward, error) {
	ns := globals.AdharSystemNamespace
	svc, err := cl.cs.CoreV1().Services(ns).Get(c, rustfsService, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("the object store Service %s/%s: %w", ns, rustfsService, err)
	}
	var sel []string
	for k, v := range svc.Spec.Selector {
		sel = append(sel, k+"="+v)
	}
	sort.Strings(sel)
	if len(sel) == 0 {
		return nil, fmt.Errorf("Service %s/%s has no selector, so there is no pod to forward to", ns, rustfsService)
	}
	pods, err := cl.cs.CoreV1().Pods(ns).List(c, metav1.ListOptions{LabelSelector: strings.Join(sel, ",")})
	if err != nil {
		return nil, err
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		if p := &pods.Items[i]; p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			pod = p
			break
		}
	}
	if pod == nil {
		return nil, fmt.Errorf("the object store has no running pod (%s in %s)", strings.Join(sel, ","), ns)
	}
	target := int32(rustfsPort)
	for _, sp := range svc.Spec.Ports {
		if sp.Port != rustfsPort {
			continue
		}
		switch {
		case sp.TargetPort.Type == intstr.Int && sp.TargetPort.IntVal != 0:
			target = sp.TargetPort.IntVal
		case sp.TargetPort.Type == intstr.String:
			for _, ctr := range pod.Spec.Containers {
				for _, cp := range ctr.Ports {
					if cp.Name == sp.TargetPort.StrVal {
						target = cp.ContainerPort
					}
				}
			}
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	local, _ := strconv.Atoi(portStr)

	req := cl.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(cl.cfg)
	if err != nil {
		return nil, err
	}
	u := req.URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, &url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawQuery: u.RawQuery})
	f := &forward{localPort: local, stopCh: make(chan struct{}), doneCh: make(chan struct{})}
	ready := make(chan struct{})
	pf, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", local, target)}, f.stopCh, ready, io.Discard, io.Discard)
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
		if err == nil {
			err = errors.New("the forward ended before it was ready")
		}
		return nil, fmt.Errorf("port-forwarding to %s/%s: %w", pod.Namespace, pod.Name, err)
	case <-time.After(20 * time.Second):
		f.close()
		return nil, fmt.Errorf("port-forward to %s/%s did not become ready within 20s", pod.Namespace, pod.Name)
	case <-c.Done():
		f.close()
		return nil, c.Err()
	}
}
