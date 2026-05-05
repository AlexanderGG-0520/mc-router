package kubernetes

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ServiceLister is the interface that wraps the ListServices method.
type ServiceLister interface {
	ListServices(ctx context.Context, namespace string) ([]ServiceInput, error)
}

// ClientServiceLister is a ServiceLister that uses the Kubernetes client-go.
type ClientServiceLister struct {
	client kubernetes.Interface
}

// NewClientServiceLister returns a new ClientServiceLister.
func NewClientServiceLister(client kubernetes.Interface) *ClientServiceLister {
	return &ClientServiceLister{client: client}
}

// ListServices lists Services in the given namespace and converts them to ServiceInput.
func (l *ClientServiceLister) ListServices(ctx context.Context, namespace string) ([]ServiceInput, error) {
	services, err := l.client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	res := make([]ServiceInput, 0, len(services.Items))
	for i := range services.Items {
		svc := &services.Items[i]
		if input, ok := ToServiceInput(svc); ok {
			res = append(res, input)
		}
	}
	return res, nil
}

// ToServiceInput converts a Kubernetes Service to a ServiceInput.
// It returns false if the Service should be skipped (e.g. ExternalName).
func ToServiceInput(svc *corev1.Service) (ServiceInput, bool) {
	if svc == nil {
		return ServiceInput{}, false
	}

	// Initial implementation skips ExternalName Services to stick to service.namespace.svc.cluster.local backends.
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return ServiceInput{}, false
	}

	input := ServiceInput{
		Name:        svc.Name,
		Namespace:   svc.Namespace,
		Annotations: cloneMap(svc.Annotations),
		Ports:       make([]ServicePort, 0, len(svc.Spec.Ports)),
	}

	for _, port := range svc.Spec.Ports {
		input.Ports = append(input.Ports, ServicePort{
			Name: port.Name,
			Port: int(port.Port),
		})
	}

	return input, true
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	res := make(map[string]string, len(m))
	for k, v := range m {
		res[k] = v
	}
	return res
}
