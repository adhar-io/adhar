/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package dev provides `adhar dev` — the developer inner loop.
//
// The platform's normal path to production is deliberately slow and auditable:
// build with buildpacks, sign, push to Harbor, commit, let ArgoCD reconcile. That
// is right for delivery and wrong for iteration. Waiting minutes on a pipeline to
// discover a typo teaches developers to avoid the platform, so they build on their
// laptops instead and find out at merge time that it behaves differently in a
// cluster.
//
// `adhar dev` closes that gap. It runs your working tree in a real namespace on
// the real cluster, next to the real dependencies — the platform Postgres, the
// platform Kafka, the platform identity — and re-syncs on every save in about a
// second. Nothing it creates goes near Git: when the change is right, the normal
// pull request and GitOps flow takes over unchanged.
package dev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

var (
	namespace    string
	appName      string
	runCommand   string
	image        string
	port         int
	localPort    int
	noForward    bool
	keep         bool
	syncDebounce time.Duration
)

// DevCmd is `adhar dev`.
var DevCmd = &cobra.Command{
	Use:   "dev [name]",
	Short: "Run your working tree on the cluster and re-sync on every save",
	Long: `Run the code in front of you on the real cluster, and re-sync it on every save.

'adhar dev' is the inner loop. It creates a per-developer dev namespace, starts
your source there, then watches the working tree: each save is copied into the
running container and the process restarts, usually in about a second. There is no
image build, no registry push and no commit in that loop, because none of them tell
you anything you did not already know from the diff.

What it buys you over running locally is the environment. The code runs beside the
platform's own Postgres, Kafka, object store and identity, with the same service
DNS, the same NetworkPolicies and the same admission rules that will apply in
production — so "works on my machine" and "works on the platform" stop being
different questions.

Nothing here touches Git. When the change is right, open a pull request and the
normal supply chain takes over: buildpacks, a signed image, and GitOps. Use
'adhar push' when you want that full path on demand.

The namespace is yours and it is disposable. It is labelled as a dev namespace so
platform policy and quotas apply, and 'adhar dev --keep=false' (the default)
removes it when you stop.

Examples:
  adhar dev                         # infer the name from the directory
  adhar dev api                     # name it explicitly
  adhar dev api --port 8080         # the port your app listens on
  adhar dev api --local-port 3000   # forward it to a different local port
  adhar dev api --keep              # leave the namespace behind when you stop

Related:
  adhar push <name>    the full build + sign + deploy path (cf push-style)
  adhar application deploy    deploy from a Gitea template, no build`,
	Args: cobra.MaximumNArgs(1),
	RunE: run,
}

func init() {
	DevCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Dev namespace (default: dev-<name>)")
	DevCmd.Flags().StringVar(&image, "image", "", "Base image to run the source in (default: inferred from the project)")
	DevCmd.Flags().StringVar(&runCommand, "command", "", "Command that starts your app (default: inferred from the project)")
	DevCmd.Flags().IntVar(&port, "port", 8080, "Port your app listens on inside the container")
	DevCmd.Flags().IntVar(&localPort, "local-port", 0, "Local port to forward to (default: same as --port)")
	DevCmd.Flags().BoolVar(&noForward, "no-forward", false, "Do not port-forward; just sync")
	DevCmd.Flags().BoolVar(&keep, "keep", false, "Keep the dev namespace when the command exits")
	DevCmd.Flags().DurationVar(&syncDebounce, "debounce", 300*time.Millisecond, "Wait this long after the last change before syncing")
}

// stack describes how to run one kind of project. Chosen by the marker file,
// because that is the thing a developer cannot forget to set.
type stack struct {
	name    string
	marker  string
	image   string
	command string
	// deps are files whose change means a dependency install is needed, not just
	// a restart. Syncing a lockfile without reinstalling leaves the container
	// running against the old tree, which looks like the sync silently failing.
	deps    []string
	install string
}

var stacks = []stack{
	{
		name: "Go", marker: "go.mod",
		image:   "golang:1.26-alpine",
		command: "go run ./...",
		deps:    []string{"go.mod", "go.sum"},
		install: "go mod download",
	},
	{
		name: "Node", marker: "package.json",
		image:   "node:22-alpine",
		command: "npm start",
		deps:    []string{"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock"},
		install: "npm install",
	},
	{
		name: "Python", marker: "pyproject.toml",
		image:   "python:3.13-alpine",
		command: "python -m app",
		deps:    []string{"pyproject.toml", "requirements.txt", "poetry.lock"},
		install: "pip install -e . 2>/dev/null || pip install -r requirements.txt",
	},
	{
		name: "Python", marker: "requirements.txt",
		image:   "python:3.13-alpine",
		command: "python -m app",
		deps:    []string{"requirements.txt"},
		install: "pip install -r requirements.txt",
	},
	{
		name: "Java", marker: "pom.xml",
		image:   "maven:3.9-eclipse-temurin-21",
		command: "mvn -q spring-boot:run",
		deps:    []string{"pom.xml"},
		install: "mvn -q -DskipTests dependency:go-offline",
	},
}

// detectStack picks the stack for dir, or reports that it cannot tell.
func detectStack(dir string) (stack, error) {
	for _, s := range stacks {
		if _, err := os.Stat(filepath.Join(dir, s.marker)); err == nil {
			return s, nil
		}
	}
	return stack{}, fmt.Errorf("could not tell what kind of project this is (looked for %s); pass --image and --command",
		strings.Join(markers(), ", "))
}

func markers() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range stacks {
		if !seen[s.marker] {
			seen[s.marker] = true
			out = append(out, s.marker)
		}
	}
	return out
}

func run(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("reading the working directory: %w", err)
	}

	appName = filepath.Base(dir)
	if len(args) == 1 {
		appName = args[0]
	}
	if err := validateName(appName); err != nil {
		return err
	}
	if namespace == "" {
		namespace = "dev-" + appName
	}
	if localPort == 0 {
		localPort = port
	}

	st, err := detectStack(dir)
	if err != nil && (image == "" || runCommand == "") {
		return err
	}
	if image == "" {
		image = st.image
	}
	if runCommand == "" {
		runCommand = st.command
	}

	if err := requireKubectl(); err != nil {
		return err
	}

	fmt.Printf("dev  %s\n", appName)
	fmt.Printf("  namespace  %s\n", namespace)
	fmt.Printf("  stack      %s (%s)\n", orUnknown(st.name), image)
	fmt.Printf("  command    %s\n", runCommand)
	fmt.Println()

	if err := ensureNamespace(ctx, namespace); err != nil {
		return err
	}
	if !keep {
		// Remove on exit, including on Ctrl-C: a dev namespace that outlives the
		// session quietly holds quota and confuses the next run.
		defer func() {
			fmt.Printf("\nremoving namespace %s (pass --keep to leave it)\n", namespace)
			_ = kubectl(context.Background(), "delete", "namespace", namespace, "--wait=false").Run()
		}()
	}

	if err := applyWorkload(ctx, st); err != nil {
		return err
	}

	pod, err := waitForPod(ctx, namespace, appName)
	if err != nil {
		return err
	}
	fmt.Printf("pod %s is ready\n", pod)

	if err := syncAll(ctx, dir, pod); err != nil {
		return err
	}
	if err := startApp(ctx, pod, st, true); err != nil {
		return err
	}

	if !noForward {
		go forward(ctx, pod)
		fmt.Printf("forwarding http://localhost:%d -> %s:%d\n", localPort, pod, port)
	}
	fmt.Printf("\nwatching %s — save a file to re-sync, Ctrl-C to stop\n\n", dir)

	return watch(ctx, dir, pod, st)
}

// watch is the inner loop: debounce filesystem events, sync, restart.
func watch(ctx context.Context, dir, pod string, st stack) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("starting the file watcher: %w", err)
	}
	defer w.Close()

	if err := addTree(w, dir); err != nil {
		return err
	}

	// Coalesce bursts. Editors write several times per save (temp file, rename,
	// chmod), and a sync per event would restart the app three times a keystroke.
	var timer *time.Timer
	pending := map[string]bool{}
	fire := make(chan struct{}, 1)

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ignored(ev.Name) {
				continue
			}
			// A new directory has to be watched too, or changes inside it are
			// invisible — which reads as the watcher having died.
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					_ = addTree(w, ev.Name)
				}
			}
			pending[ev.Name] = true
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(syncDebounce, func() {
				select {
				case fire <- struct{}{}:
				default:
				}
			})

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "watch error: %v\n", err)

		case <-fire:
			changed := make([]string, 0, len(pending))
			for f := range pending {
				changed = append(changed, f)
			}
			pending = map[string]bool{}
			sort.Strings(changed)

			needsInstall := touchesDeps(changed, dir, st)
			label := "sync"
			if needsInstall {
				label = "sync + dependency install"
			}
			fmt.Printf("%s  %s (%d file(s))\n", time.Now().Format("15:04:05"), label, len(changed))

			if err := syncAll(ctx, dir, pod); err != nil {
				fmt.Fprintf(os.Stderr, "  sync failed: %v\n", err)
				continue
			}
			if err := startApp(ctx, pod, st, needsInstall); err != nil {
				fmt.Fprintf(os.Stderr, "  restart failed: %v\n", err)
			}
		}
	}
}

// touchesDeps reports whether any changed path is a dependency manifest.
func touchesDeps(changed []string, dir string, st stack) bool {
	for _, c := range changed {
		rel, err := filepath.Rel(dir, c)
		if err != nil {
			continue
		}
		for _, d := range st.deps {
			if rel == d {
				return true
			}
		}
	}
	return false
}

// ignoredDirs are never watched or copied. Syncing them is the difference
// between a one-second loop and a thirty-second one, and node_modules alone can
// be hundreds of megabytes.
var ignoredDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"target": true, "dist": true, "build": true, ".next": true,
	"__pycache__": true, ".turbo": true, ".idea": true, ".vscode": true,
	"vendor": true, ".adhar": true,
}

func ignored(path string) bool {
	base := filepath.Base(path)
	// Editor scratch files: saving through one of these would otherwise trigger
	// a sync for a file that is about to disappear.
	if strings.HasSuffix(base, "~") || strings.HasPrefix(base, ".#") ||
		strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".tmp") {
		return true
	}
	for part := range ignoredDirs {
		if strings.Contains(path, string(os.PathSeparator)+part+string(os.PathSeparator)) ||
			base == part {
			return true
		}
	}
	return false
}

func addTree(w *fsnotify.Watcher, root string) error {
	return filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable directory must not stop the whole watch
		}
		if !fi.IsDir() || ignored(path) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return w.Add(path)
	})
}

func validateName(name string) error {
	if name == "" {
		return errors.New("a name is required (or run from a directory whose name can be used)")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return fmt.Errorf("%q is not a usable name: Kubernetes names are lowercase letters, digits and dashes", name)
		}
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
