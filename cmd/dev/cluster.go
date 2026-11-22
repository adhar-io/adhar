/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package dev

import (
	"adhar-io/adhar/cmd/helpers"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// This file drives the cluster through kubectl rather than client-go.
//
// That is deliberate for the three things the inner loop needs — cp, exec and
// port-forward. Each is a non-trivial protocol (tar over an exec stream, SPDY
// port forwarding), kubectl is already a documented prerequisite of this CLI and
// is used the same way by the provider integration steps, and a developer
// debugging a sync problem can run the identical command by hand.

func requireKubectl() error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl is required on PATH for 'adhar dev' (it drives file sync, exec and port-forward)")
	}
	return nil
}

func kubectl(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "kubectl", args...)
	c.Env = os.Environ()
	return c
}

// ensureNamespace creates the dev namespace if it is absent, labelled so the
// platform's policies, quotas and dashboards treat it as a development space
// rather than an unknown namespace that rules were never written for.
func ensureNamespace(ctx context.Context, ns string) error {
	if err := kubectl(ctx, "get", "namespace", ns).Run(); err == nil {
		return nil
	}
	if out, err := kubectl(ctx, "create", "namespace", ns).CombinedOutput(); err != nil {
		return fmt.Errorf("creating namespace %s: %w: %s", ns, err, strings.TrimSpace(string(out)))
	}
	labels := []string{
		"adhar.io/plane=workload",
		"adhar.io/environment=development",
		"adhar.io/managed-by=adhar-dev",
	}
	for _, l := range labels {
		if out, err := kubectl(ctx, "label", "namespace", ns, l, "--overwrite").CombinedOutput(); err != nil {
			return fmt.Errorf("labelling namespace %s with %s: %w: %s", ns, l, err, strings.TrimSpace(string(out)))
		}
	}
	helpers.Printf("created namespace %s\n", ns)
	return nil
}

// applyWorkload creates the Deployment and Service the loop syncs into.
//
// The container does NOT run the app as its entrypoint. It sleeps, and the app is
// started separately inside it. That is what makes a restart cost a second rather
// than a pod recreation: killing and relaunching a process inside a running
// container skips scheduling, image pull and readiness entirely.
func applyWorkload(ctx context.Context, st stack) error {
	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    app: %[1]s
    adhar.io/managed-by: adhar-dev
spec:
  replicas: 1
  selector:
    matchLabels: {app: %[1]s}
  template:
    metadata:
      labels:
        app: %[1]s
        adhar.io/managed-by: adhar-dev
    spec:
      # One shared namespace on this platform means a <NAME>_PORT env var per
      # Service, which breaks anything that decodes env strictly.
      enableServiceLinks: false
      terminationGracePeriodSeconds: 5
      containers:
        - name: app
          image: %[3]s
          # Sleep, and let the loop start the app inside. See the note above.
          command: ["sh", "-c", "trap 'exit 0' TERM; while true; do sleep 3600 & wait $!; done"]
          workingDir: /workspace
          ports:
            - containerPort: %[4]d
          env:
            - name: PORT
              value: "%[4]d"
            - name: ADHAR_DEV
              value: "true"
%[5]s          # No probes on purpose. The app is not running when the pod starts, so a
          # readiness probe would hold the pod NotReady and a liveness probe would
          # kill the container before the first sync ever landed.
          resources:
            requests: {cpu: 100m, memory: 256Mi}
            limits: {memory: 2Gi}
          volumeMounts:
            - {name: workspace, mountPath: /workspace}
      volumes:
        - name: workspace
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    app: %[1]s
    adhar.io/managed-by: adhar-dev
spec:
  selector: {app: %[1]s}
  ports:
    - name: http
      port: %[4]d
      targetPort: %[4]d
`, appName, namespace, image, port, envYAML(st.env))

	c := kubectl(ctx, "apply", "-f", "-")
	c.Stdin = strings.NewReader(manifest)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("applying the dev workload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// envYAML renders extra container env entries at the indentation of the dev
// Deployment's env list, in a stable order.
func envYAML(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "            - name: %s\n              value: %q\n", k, env[k])
	}
	return b.String()
}

// waitForPod returns the name of the running pod for the app.
func waitForPod(ctx context.Context, ns, app string) (string, error) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		out, err := kubectl(ctx, "get", "pods", "-n", ns,
			"-l", "app="+app,
			"-o", "jsonpath={.items[0].metadata.name} {.items[0].status.phase}").Output()
		if err == nil {
			parts := strings.Fields(string(out))
			if len(parts) == 2 && parts[1] == "Running" {
				return parts[0], nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return "", fmt.Errorf("the dev pod in %s did not start within 3 minutes; check 'kubectl -n %s describe pod -l app=%s'", ns, ns, app)
}

// syncAll copies the working tree into the container.
//
// The whole tree is copied rather than the changed files. It sounds wasteful and
// is not: kubectl cp streams a tar, the excluded directories are the large ones,
// and a per-file copy gets deletions and renames wrong — leaving a stale file
// behind that makes the app behave in ways the source does not explain.
func syncAll(ctx context.Context, dir, pod string) error {
	tarArgs := []string{"-C", dir, "-cf", "-"}
	for d := range ignoredDirs {
		tarArgs = append(tarArgs, "--exclude", d)
	}
	tarArgs = append(tarArgs, ".")

	tarCmd := exec.CommandContext(ctx, "tar", tarArgs...)
	untar := kubectl(ctx, "exec", "-i", "-n", namespace, pod, "--", "sh", "-c", "mkdir -p /workspace && tar -C /workspace -xf -")

	pipe, err := tarCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("preparing the sync stream: %w", err)
	}
	untar.Stdin = pipe
	var stderr strings.Builder
	untar.Stderr = &stderr

	if err := untar.Start(); err != nil {
		return fmt.Errorf("starting the remote extract: %w", err)
	}
	if err := tarCmd.Run(); err != nil {
		return fmt.Errorf("reading the working tree: %w", err)
	}
	if err := untar.Wait(); err != nil {
		return fmt.Errorf("extracting into the pod: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// startApp (re)starts the app process inside the container.
//
// The script must run under any POSIX sh — busybox ash on the Alpine images and
// dash on Debian/Ubuntu ones (a user can pass any --image). So no bash-isms:
// `printf %q` and `exec -a` are both missing from dash, and busybox has no %q,
// which once left the app never starting at all.
//
// The app runs in a process group of its own (setsid), and a restart stops that
// whole group, not one process: `mvn spring-boot:run` and `npm start` fork the
// real server as a child, and killing only the parent leaves the child holding
// the port. The group id lives in /tmp inside the container, so it can never
// outlive the pod it describes. `kill -SIG -<pgid>` is the one spelling both
// shells accept: dash reads `-s TERM -<pgid>` and `-- -<pgid>` as bad options.
func startApp(ctx context.Context, pod string, st stack, install bool) error {
	c := kubectl(ctx, "exec", "-i", "-n", namespace, pod, "--",
		"sh", "-c", startScript(runCommand, st, install))
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// startScript is the POSIX sh that startApp runs in the container: an optional
// dependency install, then stop the previous run's process group and start the
// app in a new one. Split out so a test can run it under a real sh.
func startScript(run string, st stack, install bool) string {
	script := "RUN=" + shellQuote(run) + "\n"
	if install && st.install != "" {
		script += st.install + " >/tmp/adhar-dev-install.log 2>&1 || { echo 'dependency install failed; see /tmp/adhar-dev-install.log'; exit 1; }\n"
	}
	script += `
if [ -f /tmp/adhar-dev.pgid ]; then
  pg=$(cat /tmp/adhar-dev.pgid)
  kill -TERM -"$pg" 2>/dev/null
  i=0
  while kill -0 -"$pg" 2>/dev/null && [ "$i" -lt 20 ]; do sleep 0.25; i=$((i+1)); done
  kill -KILL -"$pg" 2>/dev/null
fi
printf '%s\n' "$RUN" > /tmp/adhar-dev-run.sh
# setsid: a new session and process group, so the app survives this exec
# session ending and a restart can stop everything it started.
setsid sh /tmp/adhar-dev-run.sh >/tmp/adhar-dev-app.log 2>&1 </dev/null &
echo $! > /tmp/adhar-dev.pgid
sleep 0.3
echo started
`
	return script
}

// forward keeps a port-forward alive for the life of the session, reconnecting
// when it drops. A single kubectl port-forward exits whenever the connection
// blips, and a dead forward with no message reads as the app having crashed.
func forward(ctx context.Context, pod string) {
	for ctx.Err() == nil {
		c := kubectl(ctx, "port-forward", "-n", namespace, pod,
			fmt.Sprintf("%d:%d", localPort, port))
		c.Stdout = nil
		c.Stderr = nil
		_ = c.Run()
		if ctx.Err() != nil {
			return
		}
		time.Sleep(time.Second)
	}
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
