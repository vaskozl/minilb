package controller

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	netv1listers "k8s.io/client-go/listers/networking/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1listers "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1"
)

func TestGetAddressForHostname(t *testing.T) {
	c := &Controller{
		serviceMap: map[string]string{
			"app.example.com": "myapp.default.minilb",
		},
	}

	addr, err := c.GetAddressForHostname("app.example.com")
	if err != nil || addr != "myapp.default.minilb" {
		t.Errorf("got (%q, %v), want (myapp.default.minilb, nil)", addr, err)
	}

	addr, err = c.GetAddressForHostname("App.Example.COM.")
	if err != nil || addr != "myapp.default.minilb" {
		t.Errorf("canonical: got (%q, %v), want (myapp.default.minilb, nil)", addr, err)
	}

	_, err = c.GetAddressForHostname("")
	if err == nil {
		t.Error("expected error for empty hostname")
	}

	_, err = c.GetAddressForHostname("unknown.example.com")
	if err == nil {
		t.Error("expected error for unknown hostname")
	}
}

func TestOnDeleteCleansServiceMap(t *testing.T) {
	c := &Controller{
		serviceMap: map[string]string{
			"mqtt.example.com": "mqtt.automation.minilb",
		},
	}

	svc := &v1.Service{}
	svc.Annotations = map[string]string{HostnameAnnotation: "mqtt.example.com"}
	c.onDelete(svc)

	c.mu.RLock()
	_, ok := c.serviceMap["mqtt.example.com"]
	c.mu.RUnlock()
	if ok {
		t.Error("expected hostname to be removed from serviceMap after delete")
	}
}

func TestOnDeleteMultipleHosts(t *testing.T) {
	c := &Controller{
		serviceMap: map[string]string{
			"a.example.com":    "svc.ns.minilb",
			"b.example.com":    "svc.ns.minilb",
			"keep.example.com": "other.ns.minilb",
		},
	}

	svc := &v1.Service{}
	svc.Annotations = map[string]string{HostnameAnnotation: "a.example.com, b.example.com"}
	c.onDelete(svc)

	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.serviceMap["a.example.com"]; ok {
		t.Error("expected a.example.com to be removed")
	}
	if _, ok := c.serviceMap["b.example.com"]; ok {
		t.Error("expected b.example.com to be removed")
	}
	if _, ok := c.serviceMap["keep.example.com"]; !ok {
		t.Error("expected keep.example.com to remain")
	}
}

func TestOnDeleteIgnoresUnrelated(t *testing.T) {
	c := &Controller{
		serviceMap: map[string]string{
			"keep.example.com": "keep.default.minilb",
		},
	}

	svc := &v1.Service{}
	c.onDelete(svc)

	c.mu.RLock()
	_, ok := c.serviceMap["keep.example.com"]
	c.mu.RUnlock()
	if !ok {
		t.Error("unrelated delete should not affect serviceMap")
	}
}

func TestParseHostnames(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{"a.com", []string{"a.com"}},
		{"a.com,b.com", []string{"a.com", "b.com"}},
		{" a.com , b.com ", []string{"a.com", "b.com"}},
		{"a.com,,b.com", []string{"a.com", "b.com"}},
		{"", nil},
		{",,,", nil},
	} {
		got := parseHostnames(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("parseHostnames(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseHostnames(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestNodeInternalIP(t *testing.T) {
	for _, tt := range []struct {
		name  string
		addrs []v1.NodeAddress
		want  string
	}{
		{
			"returns internal IP",
			[]v1.NodeAddress{
				{Type: v1.NodeExternalIP, Address: "1.2.3.4"},
				{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
			},
			"10.0.0.1",
		},
		{
			"no internal IP",
			[]v1.NodeAddress{{Type: v1.NodeExternalIP, Address: "1.2.3.4"}},
			"",
		},
		{"empty list", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeInternalIP(tt.addrs); got != tt.want {
				t.Errorf("NodeInternalIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func newIngressLister(ingresses ...*netv1.Ingress) netv1listers.IngressLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, ing := range ingresses {
		_ = indexer.Add(ing)
	}
	return netv1listers.NewIngressLister(indexer)
}

func newHTTPRouteLister(routes ...*gwapiv1.HTTPRoute) gatewayv1listers.HTTPRouteLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, r := range routes {
		_ = indexer.Add(r)
	}
	return gatewayv1listers.NewHTTPRouteLister(indexer)
}

func TestResolveIngressHostnameExcluded(t *testing.T) {
	ing := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ha-external",
			Namespace:   "default",
			Annotations: map[string]string{ExcludeAnnotation: "true"},
		},
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{{Host: "ha.sko.ai"}},
		},
	}
	ing.Status.LoadBalancer.Ingress = []netv1.IngressLoadBalancerIngress{{IP: "1.2.3.4"}}

	c := &Controller{ingressLister: newIngressLister(ing)}
	addr, err := c.resolveIngressHostname("ha.sko.ai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "" {
		t.Errorf("excluded ingress should not resolve, got %q", addr)
	}
}

func TestResolveIngressHostnameNotExcluded(t *testing.T) {
	ing := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ha-internal",
			Namespace: "default",
		},
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{{Host: "ha.sko.ai"}},
		},
	}
	ing.Status.LoadBalancer.Ingress = []netv1.IngressLoadBalancerIngress{{IP: "10.0.0.1"}}

	c := &Controller{ingressLister: newIngressLister(ing)}
	addr, err := c.resolveIngressHostname("ha.sko.ai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1, got %q", addr)
	}
}

func TestResolveIngressHostnameBothExcludedAndNot(t *testing.T) {
	excluded := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ha-external",
			Namespace:   "default",
			Annotations: map[string]string{ExcludeAnnotation: "true"},
		},
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{{Host: "ha.sko.ai"}},
		},
	}
	excluded.Status.LoadBalancer.Ingress = []netv1.IngressLoadBalancerIngress{{IP: "5.6.7.8"}}

	included := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ha-internal",
			Namespace: "internal",
		},
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{{Host: "ha.sko.ai"}},
		},
	}
	included.Status.LoadBalancer.Ingress = []netv1.IngressLoadBalancerIngress{{IP: "10.0.0.1"}}

	c := &Controller{ingressLister: newIngressLister(excluded, included)}
	addr, err := c.resolveIngressHostname("ha.sko.ai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "10.0.0.1" {
		t.Errorf("expected internal ingress to resolve, got %q", addr)
	}
}

func TestResolveHTTPRouteHostnameExcluded(t *testing.T) {
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ha-external",
			Namespace:   "default",
			Annotations: map[string]string{ExcludeAnnotation: "true"},
		},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{},
			Hostnames:       []gwapiv1.Hostname{"ha.sko.ai"},
		},
	}

	c := &Controller{httpRouteLister: newHTTPRouteLister(route)}
	addr, err := c.resolveHTTPRouteHostname("ha.sko.ai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "" {
		t.Errorf("excluded HTTPRoute should not resolve, got %q", addr)
	}
}
