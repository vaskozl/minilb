package dns

import (
	"fmt"
	"math/rand"
	"net"
	"strings"

	"github.com/miekg/dns"
	"k8s.io/klog/v2"

	"github.com/vaskozl/minilb/internal/config"
	"github.com/vaskozl/minilb/internal/k8s"
)

var server *dns.Server

func Run() {
	// Create a DNS server
	server = &dns.Server{Addr: *config.Listen, Net: "udp"}

	// Setup DNS handler
	dns.HandleFunc(".", handleDNSRequest)

	// Start DNS server
	go func() {
		err := server.ListenAndServe()
		if err != nil {
			klog.Fatalf("Error starting DNS server: %v", err)
		}
	}()
	klog.Infof("DNS server started on %s", server.Addr)
}

func handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	suffix := "." + *config.Domain

	if r.Question[0].Qtype == dns.TypeA {

		name := strings.TrimSuffix(r.Question[0].Name, ".")

		if !strings.HasSuffix(name, suffix) {
			tmp, err := k8s.GetAddressForHostname(name)
			if err != nil {
				klog.Errorf("%s: %s", name, err.Error())
				w.WriteMsg(m)
				return
			}
			name = tmp
		}
		name = strings.TrimSuffix(name, suffix)

		parts := strings.SplitN(name, ".", 2)
		if len(parts) != 2 {
			klog.Warningf("Invalid domain format: %s", name)
			w.WriteMsg(m)
			return
		}
		serviceName, namespace := parts[0], parts[1]

		endpoints, err := k8s.GetEndpoints(serviceName, namespace)
		if err != nil {
			klog.Errorf("Error getting Endpoints for %s: %v", serviceName, err)
			w.WriteMsg(m)
			return
		}

		for _, subset := range endpoints.Subsets {
			for _, address := range subset.Addresses {
				rr := dns.TypeA
				ip := net.ParseIP(address.IP)
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: rr, Class: dns.ClassINET, Ttl: uint32(*config.TTL)},
					A:   ip,
				})
			}
		}

		// Shuffle the responses so we get some load balancing
		shuffleDNSAnswers(m.Answer)

		klog.InfoS(fmt.Sprintf("%+v", m.Answer),
			"svc", serviceName,
			"ns", namespace,
		)
	}

	w.WriteMsg(m)
	klog.V(2).Infof("%v", m)
}

func shuffleDNSAnswers(answers []dns.RR) {
	for i := range answers {
		j := rand.Intn(i + 1)
		answers[i], answers[j] = answers[j], answers[i]
	}
}
