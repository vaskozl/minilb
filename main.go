package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/miekg/dns"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kubeconfig = flag.String("kubeconfig", "", "Path to a kubeconfig file")
	domain     = "k8s" // Change this to your domain
)

func main() {
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Fatalf("Error building kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating clientset: %v", err)
	}

	PrintNodeRoutes(clientset)

	// Create a DNS server
	dnsServer := &dns.Server{Addr: ":53", Net: "udp"}

	// Setup DNS handler
	dns.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true

		if r.Question[0].Qtype == dns.TypeA {

			name := strings.TrimSuffix(r.Question[0].Name, "."+domain+".")
			parts := strings.SplitN(name, ".", 2)
			if len(parts) != 2 {
				log.Printf("Invalid domain format: %s", name)
				w.WriteMsg(m)
				return
			}
			serviceName, namespace := parts[0], parts[1]

			endpoints, err := getEndpoints(clientset, serviceName, namespace)
			if err != nil {
				log.Printf("Error getting Endpoints for %s: %v", serviceName, err)
				w.WriteMsg(m)
				return
			}

			for _, subset := range endpoints.Subsets {
				for _, address := range subset.Addresses {
					rr := dns.TypeA
					ip := net.ParseIP(address.IP)
					m.Answer = append(m.Answer, &dns.A{
						Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: rr, Class: dns.ClassINET, Ttl: 5},
						A:   ip,
					})
				}
			}
		}

		// Shuffle the responses so we get some load balancing
		for i := range m.Answer {
			j := rand.Intn(i + 1)
			m.Answer[i], m.Answer[j] = m.Answer[j], m.Answer[i]
		}

		w.WriteMsg(m)
		fmt.Printf("%v", m)
	})

	// Start DNS server
	go func() {
		err := dnsServer.ListenAndServe()
		if err != nil {
			log.Fatalf("Error starting DNS server: %v", err)
		}
	}()
	log.Printf("DNS server started on %s", dnsServer.Addr)

	// Wait for termination signal
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	// Shutdown DNS server
	dnsServer.Shutdown()
}

func getEndpoints(clientset *kubernetes.Clientset, serviceName string, namespace string) (*v1.Endpoints, error) {
	endpoints, err := clientset.CoreV1().Endpoints(namespace).Get(context.Background(), serviceName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return endpoints, nil
}
