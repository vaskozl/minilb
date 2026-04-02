package dns

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"strings"

	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/miekg/dns"
)

// Resolver looks up hostnames and endpoint IPs for the DNS handler.
type Resolver interface {
	GetAddressForHostname(hostname string) (string, error)
	GetEndpointIPs(serviceName, namespace string, addrType discoveryv1.AddressType) ([]string, error)
}

// Handler serves DNS requests backed by a Resolver.
type Handler struct {
	Domain   string
	TTL      uint32
	Resolver Resolver
	Upstream string // optional upstream resolver for non-handled queries
}

// ServeDNS implements the dns.Handler interface for handling DNS queries.
// It resolves A, AAAA, and SOA records, falling back to upstream resolution if configured.
func (h *Handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	if len(r.Question) > 0 {
		switch r.Question[0].Qtype {
		case dns.TypeA:
			h.handleAddr(m, r, false)
		case dns.TypeAAAA:
			h.handleAddr(m, r, true)
		case dns.TypeSOA:
			h.handleSOA(m, r)
		default:
			h.tryForward(m, r)
		}
	}

	if err := w.WriteMsg(m); err != nil {
		slog.Error("Failed to write DNS response", "err", err)
	}
}

func (h *Handler) handleAddr(m, r *dns.Msg, v6 bool) {
	suffix := "." + h.Domain
	name := strings.TrimSuffix(r.Question[0].Name, ".")

	if !strings.HasSuffix(name, suffix) {
		resolved, err := h.Resolver.GetAddressForHostname(name)
		if err != nil {
			if h.tryForward(m, r) {
				return
			}
			slog.Debug("Hostname not found", "name", name, "err", err)
			m.Rcode = dns.RcodeNameError
			return
		}
		name = resolved
	}
	name = strings.TrimSuffix(name, suffix)

	svc, ns, ok := ParseServiceName(name)
	if !ok {
		slog.Warn("Invalid domain format", "name", name)
		m.Rcode = dns.RcodeNameError
		return
	}

	addrType := discoveryv1.AddressTypeIPv4
	if v6 {
		addrType = discoveryv1.AddressTypeIPv6
	}

	ips, err := h.Resolver.GetEndpointIPs(svc, ns, addrType)
	if err != nil {
		slog.Debug("No endpoints", "svc", svc, "ns", ns, "err", err)
		m.Rcode = dns.RcodeNameError
		return
	}

	qname := r.Question[0].Name
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if v6 {
			if ip.To4() != nil {
				continue
			}
			m.Answer = append(m.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: qname, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: h.TTL},
				AAAA: ip,
			})
		} else {
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: qname, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: h.TTL},
				A:   ip4,
			})
		}
	}

	rand.Shuffle(len(m.Answer), func(i, j int) {
		m.Answer[i], m.Answer[j] = m.Answer[j], m.Answer[i]
	})

	slog.Debug("Resolved", "svc", svc, "ns", ns, "answers", len(m.Answer))
}

func (h *Handler) handleSOA(m, r *dns.Msg) {
	qname := r.Question[0].Name
	if !strings.HasSuffix(strings.TrimSuffix(qname, "."), h.Domain) && qname != h.Domain+"." {
		h.tryForward(m, r)
		return
	}
	m.Answer = append(m.Answer, &dns.SOA{
		Hdr:     dns.RR_Header{Name: h.Domain + ".", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: h.TTL},
		Ns:      "ns." + h.Domain + ".",
		Mbox:    "admin." + h.Domain + ".",
		Serial:  1,
		Refresh: 3600,
		Retry:   600,
		Expire:  86400,
		Minttl:  h.TTL,
	})
}

// tryForward attempts to forward the request to the upstream resolver.
// Returns true if the forward was successful and m was populated.
func (h *Handler) tryForward(m, r *dns.Msg) bool {
	if h.Upstream == "" {
		return false
	}
	resp, err := dns.Exchange(r, h.Upstream)
	if err != nil {
		slog.Debug("Upstream forward failed", "err", err)
		return false
	}
	resp.Id = r.Id
	*m = *resp
	m.Authoritative = false
	return true
}

// ParseServiceName splits "service.namespace" into its parts.
func ParseServiceName(name string) (service, namespace string, ok bool) {
	if i := strings.IndexByte(name, '.'); i > 0 && i < len(name)-1 {
		return name[:i], name[i+1:], true
	}
	return "", "", false
}

// Server wraps UDP and TCP DNS servers for graceful shutdown.
type Server struct {
	udp *dns.Server
	tcp *dns.Server
}

// Run starts UDP and TCP DNS servers in the background.
func Run(handler *Handler, listen string) *Server {
	s := &Server{
		udp: &dns.Server{Addr: listen, Net: "udp", Handler: handler},
		tcp: &dns.Server{Addr: listen, Net: "tcp", Handler: handler},
	}
	go func() {
		if err := s.udp.ListenAndServe(); err != nil {
			slog.Error("UDP DNS server failed", "err", err)
		}
	}()
	go func() {
		if err := s.tcp.ListenAndServe(); err != nil {
			slog.Error("TCP DNS server failed", "err", err)
		}
	}()
	slog.Info("DNS server started", "addr", listen)
	return s
}

// Shutdown gracefully stops both servers.
func (s *Server) Shutdown(ctx context.Context) {
	if err := s.udp.ShutdownContext(ctx); err != nil {
		slog.Error("UDP shutdown error", "err", err)
	}
	if err := s.tcp.ShutdownContext(ctx); err != nil {
		slog.Error("TCP shutdown error", "err", err)
	}
}
