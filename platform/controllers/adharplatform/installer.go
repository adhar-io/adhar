package adharplatform

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/k8s"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// installReadyTimeout bounds how long Install waits for its workloads. It
// used to be a package-level time.After, created once at process start: it
// fired ten minutes after the binary started (not after Install did), could
// be received only once, and every later Install then waited forever.
const installReadyTimeout = 10 * time.Minute

type EmbeddedInstallation struct {
	name         string
	resourcePath string
	namespace    string

	// skips waiting on expected resources to become ready
	skipReadinessCheck bool

	// name and gvk pair for resources that need to be monitored
	monitoredResources map[string]schema.GroupVersionKind
	customization      v1alpha1.PackageCustomization
	resourceFS         embed.FS

	// resources that need to be created without using static manifests or gitops
	unmanagedResources []client.Object
}

func (e *EmbeddedInstallation) installResources(scheme *runtime.Scheme, templateData any) ([]client.Object, error) {
	return k8s.BuildCustomizedObjects(e.customization.FilePath, e.resourcePath, e.resourceFS, scheme, templateData)
}

func (e *EmbeddedInstallation) newNamespace(namespace string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
}

func (e *EmbeddedInstallation) Install(ctx context.Context, resource *v1alpha1.AdharPlatform, cli client.Client, sc *runtime.Scheme, cfg v1alpha1.BuildCustomizationSpec) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	nsClient := client.NewNamespacedClient(cli, e.namespace)
	installObjs, err := e.installResources(sc, cfg)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err = k8s.EnsureNamespace(ctx, nsClient, e.namespace); err != nil {
		return ctrl.Result{}, err
	}

	for i := range e.unmanagedResources {
		err = k8s.EnsureObject(ctx, nsClient, e.unmanagedResources[i], e.namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	sch := runtime.NewScheme()
	appsv1.AddToScheme(sch)

	for _, obj := range installObjs {
		// Create object
		if err = k8s.EnsureObject(ctx, nsClient, obj, e.namespace); err != nil {
			return ctrl.Result{}, err
		}
	}

	// return early if readiness check is disabled
	if e.skipReadinessCheck {
		return ctrl.Result{}, nil
	}

	// wait for expected resources to become available
	waitCtx, cancel := context.WithTimeout(ctx, installReadyTimeout)
	defer cancel()
	// Buffered for every waiter: one error ends the wait, and the waiters that
	// fail after it must not block forever on a send nobody receives.
	errCh := make(chan error, len(installObjs))
	var wg sync.WaitGroup

	for _, obj := range installObjs {
		if gvk, ok := e.monitoredResources[obj.GetName()]; ok {
			if obj.GetObjectKind().GroupVersionKind() != gvk {
				continue
			}

			wg.Add(1)
			go func(obj client.Object, gvk schema.GroupVersionKind) {
				defer wg.Done()

				gvkObj, err := sch.New(gvk)
				if err != nil {
					errCh <- err
					return
				}

				for {
					if gotObj, ok := gvkObj.(client.Object); ok {
						if err := cli.Get(waitCtx, types.NamespacedName{Namespace: e.namespace, Name: obj.GetName()}, gotObj); err != nil {
							errCh <- err
							return
						}

						switch t := gotObj.(type) {
						case *appsv1.Deployment:
							if t.Status.AvailableReplicas >= 1 {
								logger.V(1).Info(t.GetName(), "deployment", t.Status.AvailableReplicas)
								return
							}
						case *appsv1.StatefulSet:
							if t.Status.AvailableReplicas >= 1 {
								logger.V(1).Info(t.GetName(), "statefulset", t.Status.AvailableReplicas)
								return
							}
						}
					}

					logger.Info(fmt.Sprintf("Waiting for %s %s to become ready", gvk.Kind, obj.GetName()))
					// The waiters stop with the wait: they used to sleep-poll
					// forever after Install had already returned.
					select {
					case <-waitCtx.Done():
						return
					case <-time.After(30 * time.Second):
					}
				}
			}(obj, gvk)
		}
	}

	allDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allDone)
	}()

	select {
	case <-waitCtx.Done():
		err := errors.New("Installation timeout exceeded")
		logger.Error(err, fmt.Sprintf("Didn't reconcile %s within 10-minute timeout - components may still be starting up", e.name))
		return ctrl.Result{}, err
	case err := <-errCh:
		logger.Error(err, fmt.Sprintf("failed to reconcile the %s resources", e.name))
		return ctrl.Result{}, err
	case <-allDone:
		select {
		case err := <-errCh:
			logger.Error(err, fmt.Sprintf("failed to reconcile the %s resources", e.name))
			return ctrl.Result{}, err
		default:
		}
		logger.V(1).Info(fmt.Sprintf("%s is ready!", e.name))
	}

	return ctrl.Result{}, nil
}
