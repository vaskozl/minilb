package controller

import (
	"testing"

	v1 "k8s.io/api/core/v1"
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
