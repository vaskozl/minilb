package controller

import (
	"strings"

	discoveryv1 "k8s.io/api/discovery/v1"
)

// HostnameMatches checks if candidate matches query, supporting single-label wildcard prefixes.
func HostnameMatches(candidate, query string) bool {
	candidate = CanonicalHostname(candidate)
	query = CanonicalHostname(query)
	if candidate == "" || query == "" {
		return false
	}
	if candidate == query {
		return true
	}
	if strings.HasPrefix(candidate, "*.") {
		suffix := candidate[1:] // ".example.com"
		if !strings.HasSuffix(query, suffix) {
			return false
		}
		prefix := query[:len(query)-len(suffix)]
		return len(prefix) > 0 && !strings.Contains(prefix, ".")
	}
	return false
}

func CanonicalHostname(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func ContainsMatchingHostname(hosts []string, query string) bool {
	for _, h := range hosts {
		if HostnameMatches(h, query) {
			return true
		}
	}
	return false
}

func IsReadyEndpoint(ep *discoveryv1.Endpoint) bool {
	if ep == nil || len(ep.Addresses) == 0 {
		return false
	}
	if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
		return false
	}
	if ep.Conditions.Serving != nil && !*ep.Conditions.Serving {
		return false
	}
	if ep.Conditions.Terminating != nil && *ep.Conditions.Terminating {
		return false
	}
	return true
}

func hostnamesToStrings[T ~string](hosts []T) []string {
	if len(hosts) == 0 {
		return nil
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h != "" {
			out = append(out, string(h))
		}
	}
	return out
}
