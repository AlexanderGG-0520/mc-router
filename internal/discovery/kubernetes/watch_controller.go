package kubernetes

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	k8sclient "k8s.io/client-go/kubernetes"
)

// RouteSink accepts a complete replacement set of discovered routes.
type RouteSink interface {
	Update([]DiscoveredRoute)
}

// RouteResultSink accepts the full controller result for callers that need skip stats.
type RouteResultSink interface {
	UpdateResult(Result)
}

type ServiceWatchControllerOptions struct {
	AnnotationPrefix string
}

var (
	ErrServiceListFailed       = errors.New("kubernetes service list failed")
	ErrServiceWatchClosed      = errors.New("kubernetes service watch closed")
	ErrServiceWatchError       = errors.New("kubernetes service watch error")
	ErrServiceWatchSetupFailed = errors.New("kubernetes service watch setup failed")
)

// ServiceWatchController watches namespace-scoped Services and rebuilds discovered routes.
type ServiceWatchController struct {
	client    k8sclient.Interface
	namespace string
	sink      RouteSink
	options   Options
}

// NewServiceWatchController creates a controller for Service annotation discovery.
func NewServiceWatchController(client k8sclient.Interface, namespace string, sink RouteSink, options ServiceWatchControllerOptions) (*ServiceWatchController, error) {
	if client == nil {
		return nil, errors.New("kubernetes client is nil")
	}
	if err := validateNamespace(namespace); err != nil {
		return nil, fmt.Errorf("watch namespace: %w", err)
	}
	if sink == nil {
		return nil, errors.New("route sink is nil")
	}

	controller := &ServiceWatchController{
		client:    client,
		namespace: namespace,
		sink:      sink,
		options: Options{
			AnnotationPrefix: options.AnnotationPrefix,
		},
	}
	return controller, nil
}

// Run lists Services once, starts a namespace-scoped watch, and blocks until ctx is cancelled.
func (c *ServiceWatchController) Run(ctx context.Context) error {
	if c == nil {
		return errors.New("service watch controller is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}

	services, resourceVersion, err := c.initialServices(ctx)
	if err != nil {
		return err
	}

	watcher, err := c.client.CoreV1().Services(c.namespace).Watch(ctx, metav1.ListOptions{ResourceVersion: resourceVersion})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("%w: watch services in namespace %q: %w", ErrServiceWatchSetupFailed, c.namespace, err)
	}
	defer watcher.Stop()

	c.updateSink(services)

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("%w: watch services in namespace %q closed", ErrServiceWatchClosed, c.namespace)
			}
			if err := c.applyWatchEvent(services, event); err != nil {
				return err
			}
			c.updateSink(services)
		}
	}
}

func (c *ServiceWatchController) initialServices(ctx context.Context) (map[string]ServiceInput, string, error) {
	list, err := c.client.CoreV1().Services(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("%w: list services in namespace %q: %w", ErrServiceListFailed, c.namespace, err)
	}

	services := make(map[string]ServiceInput, len(list.Items))
	for i := range list.Items {
		svc := &list.Items[i]
		if input, ok := ToServiceInput(svc); ok {
			services[serviceKey(svc.Namespace, svc.Name)] = input
		}
	}
	return services, list.ResourceVersion, nil
}

func (c *ServiceWatchController) applyWatchEvent(services map[string]ServiceInput, event watch.Event) error {
	if event.Type == watch.Error {
		return fmt.Errorf("%w: watch services in namespace %q returned error event", ErrServiceWatchError, c.namespace)
	}

	svc, ok := event.Object.(*corev1.Service)
	if !ok {
		return nil
	}

	key := serviceKey(svc.Namespace, svc.Name)
	switch event.Type {
	case watch.Added, watch.Modified:
		if input, ok := ToServiceInput(svc); ok {
			services[key] = input
			return nil
		}
		delete(services, key)
	case watch.Deleted:
		delete(services, key)
	case watch.Bookmark:
		return nil
	}
	return nil
}

func (c *ServiceWatchController) updateSink(services map[string]ServiceInput) {
	inputs := make([]ServiceInput, 0, len(services))
	for _, service := range services {
		inputs = append(inputs, service)
	}

	result := BuildDiscoveredRoutes(inputs, c.options)
	if sink, ok := c.sink.(RouteResultSink); ok {
		sink.UpdateResult(result)
		return
	}
	c.sink.Update(result.Routes)
}

func serviceKey(namespace, name string) string {
	return namespace + "/" + name
}
