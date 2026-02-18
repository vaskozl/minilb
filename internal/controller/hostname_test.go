package controller

import (
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
)

func TestCanonicalHostname(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"Example.COM", "example.com"},
		{"example.com.", "example.com"},
		{"  Example.COM. ", "example.com"},
		{"", ""},
		{".", ""},
	} {
		if got := CanonicalHostname(tt.in); got != tt.want {
			t.Errorf("CanonicalHostname(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHostnameMatches(t *testing.T) {
	for _, tt := range []struct {
		candidate, query string
		want             bool
	}{
		{"example.com", "example.com", true},
		{"Example.COM", "example.com", true},
		{"example.com.", "example.com", true},
		{"*.example.com", "foo.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "bar.foo.example.com", false},
		{"other.com", "example.com", false},
		{"", "example.com", false},
		{"example.com", "", false},
		{"*.", "foo", false},
	} {
		if got := HostnameMatches(tt.candidate, tt.query); got != tt.want {
			t.Errorf("HostnameMatches(%q, %q) = %v, want %v", tt.candidate, tt.query, got, tt.want)
		}
	}
}

func TestContainsMatchingHostname(t *testing.T) {
	for _, tt := range []struct {
		hosts []string
		query string
		want  bool
	}{
		{[]string{"a.com", "b.com"}, "b.com", true},
		{[]string{"a.com"}, "b.com", false},
		{nil, "a.com", false},
		{[]string{"*.example.com"}, "foo.example.com", true},
		{[]string{"*.example.com"}, "a.b.example.com", false},
	} {
		if got := ContainsMatchingHostname(tt.hosts, tt.query); got != tt.want {
			t.Errorf("ContainsMatchingHostname(%v, %q) = %v, want %v", tt.hosts, tt.query, got, tt.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func TestIsReadyEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name string
		ep   *discoveryv1.Endpoint
		want bool
	}{
		{"nil", nil, false},
		{"no addresses", &discoveryv1.Endpoint{}, false},
		{"ready", &discoveryv1.Endpoint{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)},
		}, true},
		{"not ready", &discoveryv1.Endpoint{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(false)},
		}, false},
		{"not serving", &discoveryv1.Endpoint{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Serving: boolPtr(false)},
		}, false},
		{"terminating", &discoveryv1.Endpoint{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Terminating: boolPtr(true)},
		}, false},
		{"conditions nil (defaults ready)", &discoveryv1.Endpoint{
			Addresses: []string{"10.0.0.1"},
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReadyEndpoint(tt.ep); got != tt.want {
				t.Errorf("IsReadyEndpoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHostnamesToStrings(t *testing.T) {
	type H = string
	got := hostnamesToStrings([]H{"a.com", "", "b.com"})
	if len(got) != 2 || got[0] != "a.com" || got[1] != "b.com" {
		t.Errorf("got %v, want [a.com b.com]", got)
	}
	if got := hostnamesToStrings([]H(nil)); got != nil {
		t.Errorf("nil input should return nil, got %v", got)
	}
}
