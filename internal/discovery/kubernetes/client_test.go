package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestClientServiceLister_ListServices(t *testing.T) {
	svc1 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "smp",
			Namespace: "minecraft",
			Annotations: map[string]string{
				"mc-router.alexandergg.com/enabled": "true",
				"mc-router.alexandergg.com/host":    "smp.example.com",
				"mc-router.alexandergg.com/port":    "25565",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "minecraft", Port: 25565},
			},
		},
	}
	svc2 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy",
			Namespace: "minecraft",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeExternalName,
		},
	}
	svc3 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lobby",
			Namespace: "other",
		},
	}

	client := fake.NewSimpleClientset(svc1, svc2, svc3)
	lister := NewClientServiceLister(client)

	ctx := context.Background()
	inputs, err := lister.ListServices(ctx, "minecraft")
	if err != nil {
		t.Fatalf("ListServices error: %v", err)
	}

	if len(inputs) != 1 {
		t.Fatalf("got %d services, want 1 (ExternalName should be skipped)", len(inputs))
	}

	if inputs[0].Name != "smp" {
		t.Errorf("got name %q, want %q", inputs[0].Name, "smp")
	}

	wantPorts := []ServicePort{{Name: "minecraft", Port: 25565}}
	if !reflect.DeepEqual(inputs[0].Ports, wantPorts) {
		t.Errorf("got ports %v, want %v", inputs[0].Ports, wantPorts)
	}

	wantAnnotations := map[string]string{
		"mc-router.alexandergg.com/enabled": "true",
		"mc-router.alexandergg.com/host":    "smp.example.com",
		"mc-router.alexandergg.com/port":    "25565",
	}
	if !reflect.DeepEqual(inputs[0].Annotations, wantAnnotations) {
		t.Errorf("got annotations %v, want %v", inputs[0].Annotations, wantAnnotations)
	}
}

func TestClientServiceLister_ListServices_Error(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "services", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, errors.New("API error")
	})

	lister := NewClientServiceLister(client)
	_, err := lister.ListServices(context.Background(), "default")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "API error" {
		t.Errorf("got error %v, want %v", err, "API error")
	}
}

func TestToServiceInput(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "n",
				Namespace:   "ns",
				Annotations: map[string]string{"a": "v"},
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Name: "p", Port: 80}},
			},
		}
		input, ok := ToServiceInput(svc)
		if !ok {
			t.Fatal("expected ok, got false")
		}
		if input.Name != "n" || input.Namespace != "ns" {
			t.Errorf("unexpected name/namespace: %s/%s", input.Name, input.Namespace)
		}
		if !reflect.DeepEqual(input.Annotations, map[string]string{"a": "v"}) {
			t.Errorf("unexpected annotations: %v", input.Annotations)
		}
		if len(input.Ports) != 1 || input.Ports[0].Name != "p" || input.Ports[0].Port != 80 {
			t.Errorf("unexpected ports: %v", input.Ports)
		}

		// Mutability check
		svc.Annotations["a"] = "mutated"
		if input.Annotations["a"] == "mutated" {
			t.Errorf("ToServiceInput leaked mutable annotation state")
		}
	})

	t.Run("externalName", func(t *testing.T) {
		svc := &corev1.Service{
			Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeExternalName,
			},
		}
		_, ok := ToServiceInput(svc)
		if ok {
			t.Fatal("expected ExternalName to be skipped")
		}
	})

	t.Run("nil", func(t *testing.T) {
		_, ok := ToServiceInput(nil)
		if ok {
			t.Fatal("expected nil to be skipped")
		}
	})
}

func TestClientServiceLister_BuildDiscoveredRoutes_Integration(t *testing.T) {
	svc1 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "smp",
			Namespace: "minecraft",
			Annotations: map[string]string{
				"mc-router.alexandergg.com/enabled": "true",
				"mc-router.alexandergg.com/host":    "smp.example.com",
				"mc-router.alexandergg.com/port":    "25565",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "minecraft", Port: 25565},
			},
		},
	}
	svc2 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lobby",
			Namespace: "minecraft",
			Annotations: map[string]string{
				"mc-router.alexandergg.com/enabled": "true",
				"mc-router.alexandergg.com/host":    "lobby.example.com",
				"mc-router.alexandergg.com/port":    "25565",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "minecraft", Port: 25565},
			},
		},
	}

	client := fake.NewSimpleClientset(svc1, svc2)
	lister := NewClientServiceLister(client)

	inputs, err := lister.ListServices(context.Background(), "minecraft")
	if err != nil {
		t.Fatalf("ListServices error: %v", err)
	}

	result := BuildDiscoveredRoutes(inputs, Options{})
	if len(result.Routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(result.Routes))
	}

	hosts := []string{result.Routes[0].Host, result.Routes[1].Host}
	sortStrings(hosts)
	wantHosts := []string{"lobby.example.com", "smp.example.com"}
	if !reflect.DeepEqual(hosts, wantHosts) {
		t.Errorf("got hosts %v, want %v", hosts, wantHosts)
	}
}

func sortStrings(v []string) {
	importSort := func(s []string) {
		for i := 1; i < len(s); i++ {
			for j := i; j > 0 && s[j] < s[j-1]; j-- {
				s[j], s[j-1] = s[j-1], s[j]
			}
		}
	}
	importSort(v)
}
