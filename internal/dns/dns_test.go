package dns

import (
	"errors"
	"net"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/miekg/dns"
)

func TestParseServiceName(t *testing.T) {
	for _, tt := range []struct {
		in      string
		svc, ns string
		ok      bool
	}{
		{"myapp.default", "myapp", "default", true},
		{"svc.ns.extra", "svc", "ns.extra", true},
		{"nope", "", "", false},
		{"", "", "", false},
		{".ns", "", "", false},
		{"svc.", "", "", false},
	} {
		svc, ns, ok := ParseServiceName(tt.in)
		if svc != tt.svc || ns != tt.ns || ok != tt.ok {
			t.Errorf("ParseServiceName(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.in, svc, ns, ok, tt.svc, tt.ns, tt.ok)
		}
	}
}

type mockResolver struct {
	addr    string
	addrErr error
	ips     []string
	ipsErr  error
}

func (m mockResolver) GetAddressForHostname(string) (string, error) {
	return m.addr, m.addrErr
}

func (m mockResolver) GetEndpointIPs(string, string, discoveryv1.AddressType) ([]string, error) {
	return m.ips, m.ipsErr
}

type recorderWriter struct {
	msg *dns.Msg
}

func (w *recorderWriter) LocalAddr() net.Addr       { return nil }
func (w *recorderWriter) RemoteAddr() net.Addr      { return nil }
func (w *recorderWriter) WriteMsg(m *dns.Msg) error { w.msg = m; return nil }
func (w *recorderWriter) Write([]byte) (int, error) { return 0, nil }
func (w *recorderWriter) Close() error              { return nil }
func (w *recorderWriter) TsigStatus() error         { return nil }
func (w *recorderWriter) TsigTimersOnly(bool)       {}
func (w *recorderWriter) Hijack()                   {}

func TestHandlerDirect(t *testing.T) {
	h := &Handler{
		Domain:   "minilb",
		TTL:      5,
		Resolver: mockResolver{ips: []string{"10.0.0.1", "10.0.0.2"}},
	}

	r := new(dns.Msg)
	r.SetQuestion("web.default.minilb.", dns.TypeA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if w.msg == nil {
		t.Fatal("no response written")
	}
	if w.msg.Rcode != dns.RcodeSuccess {
		t.Errorf("Rcode = %d, want SUCCESS", w.msg.Rcode)
	}
	if len(w.msg.Answer) != 2 {
		t.Fatalf("got %d answers, want 2", len(w.msg.Answer))
	}
	for _, rr := range w.msg.Answer {
		a, ok := rr.(*dns.A)
		if !ok {
			t.Fatal("answer is not A record")
		}
		if a.Hdr.Ttl != 5 {
			t.Errorf("TTL = %d, want 5", a.Hdr.Ttl)
		}
	}
}

func TestHandlerAAAA(t *testing.T) {
	h := &Handler{
		Domain:   "minilb",
		TTL:      5,
		Resolver: mockResolver{ips: []string{"fd00::1", "fd00::2"}},
	}

	r := new(dns.Msg)
	r.SetQuestion("web.default.minilb.", dns.TypeAAAA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if w.msg == nil {
		t.Fatal("no response written")
	}
	if w.msg.Rcode != dns.RcodeSuccess {
		t.Errorf("Rcode = %d, want SUCCESS", w.msg.Rcode)
	}
	if len(w.msg.Answer) != 2 {
		t.Fatalf("got %d answers, want 2", len(w.msg.Answer))
	}
	for _, rr := range w.msg.Answer {
		aaaa, ok := rr.(*dns.AAAA)
		if !ok {
			t.Fatal("answer is not AAAA record")
		}
		if aaaa.Hdr.Ttl != 5 {
			t.Errorf("TTL = %d, want 5", aaaa.Hdr.Ttl)
		}
	}
}

func TestHandlerHostnameLookup(t *testing.T) {
	h := &Handler{
		Domain:   "minilb",
		TTL:      5,
		Resolver: mockResolver{addr: "web.default.minilb", ips: []string{"10.0.0.1"}},
	}

	r := new(dns.Msg)
	r.SetQuestion("myapp.example.com.", dns.TypeA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if w.msg == nil {
		t.Fatal("no response written")
	}
	if len(w.msg.Answer) != 1 {
		t.Fatalf("got %d answers, want 1", len(w.msg.Answer))
	}
}

func TestHandlerHostnameLookupNXDOMAIN(t *testing.T) {
	h := &Handler{
		Domain:   "minilb",
		TTL:      5,
		Resolver: mockResolver{addrErr: errors.New("not found")},
	}

	r := new(dns.Msg)
	r.SetQuestion("unknown.example.com.", dns.TypeA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if w.msg == nil {
		t.Fatal("no response written")
	}
	if w.msg.Rcode != dns.RcodeNameError {
		t.Errorf("Rcode = %d, want NXDOMAIN (%d)", w.msg.Rcode, dns.RcodeNameError)
	}
	if len(w.msg.Answer) != 0 {
		t.Errorf("expected no answers, got %d", len(w.msg.Answer))
	}
}

func TestHandlerEndpointErrorNXDOMAIN(t *testing.T) {
	h := &Handler{
		Domain:   "minilb",
		TTL:      5,
		Resolver: mockResolver{ipsErr: errors.New("no endpoints")},
	}

	r := new(dns.Msg)
	r.SetQuestion("web.default.minilb.", dns.TypeA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if w.msg.Rcode != dns.RcodeNameError {
		t.Errorf("Rcode = %d, want NXDOMAIN (%d)", w.msg.Rcode, dns.RcodeNameError)
	}
}

func TestHandlerInvalidFormatNXDOMAIN(t *testing.T) {
	h := &Handler{
		Domain:   "minilb",
		TTL:      5,
		Resolver: mockResolver{},
	}

	r := new(dns.Msg)
	r.SetQuestion("nonamespace.minilb.", dns.TypeA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if w.msg.Rcode != dns.RcodeNameError {
		t.Errorf("Rcode = %d, want NXDOMAIN (%d)", w.msg.Rcode, dns.RcodeNameError)
	}
}

func TestHandlerSOA(t *testing.T) {
	h := &Handler{Domain: "minilb", TTL: 5, Resolver: mockResolver{}}

	r := new(dns.Msg)
	r.SetQuestion("minilb.", dns.TypeSOA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if w.msg == nil {
		t.Fatal("no response written")
	}
	if len(w.msg.Answer) != 1 {
		t.Fatalf("got %d answers, want 1", len(w.msg.Answer))
	}
	soa, ok := w.msg.Answer[0].(*dns.SOA)
	if !ok {
		t.Fatal("answer is not SOA record")
	}
	if soa.Ns != "ns.minilb." {
		t.Errorf("SOA.Ns = %q, want ns.minilb.", soa.Ns)
	}
}

func TestHandlerSkipsInvalidIPs(t *testing.T) {
	h := &Handler{
		Domain:   "minilb",
		TTL:      5,
		Resolver: mockResolver{ips: []string{"10.0.0.1", "not-an-ip", "10.0.0.2"}},
	}

	r := new(dns.Msg)
	r.SetQuestion("web.default.minilb.", dns.TypeA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if len(w.msg.Answer) != 2 {
		t.Errorf("expected 2 answers (skipping invalid IP), got %d", len(w.msg.Answer))
	}
}

func TestHandlerAAAASkipsIPv4(t *testing.T) {
	h := &Handler{
		Domain:   "minilb",
		TTL:      5,
		Resolver: mockResolver{ips: []string{"10.0.0.1", "fd00::1"}},
	}

	r := new(dns.Msg)
	r.SetQuestion("web.default.minilb.", dns.TypeAAAA)

	w := &recorderWriter{}
	h.ServeDNS(w, r)

	if len(w.msg.Answer) != 1 {
		t.Errorf("expected 1 AAAA answer, got %d", len(w.msg.Answer))
	}
}
