package main

import (
	"context"
	"flag"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"path/filepath"

	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/vaskozl/minilb/pkg"
)

var (
	kubeconfig = flag.String("kubeconfig", "", "Path to a kubeconfig file")
	domain     = flag.String("domain", "minilb", "Zone under which to resolve services")
	listen     = flag.String("listen", ":53", "Address and port to listen to")
	logLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error, fatal, panic)")

	resyncPeriod = flag.Int("resync", 300, "How often to check services with the API ")
	ttl          = flag.Uint("ttl", 5, "Record time to live in seconds")

	controller = flag.Bool("controller", false, "Run the service controller in addition to the DNS server")
)

func main() {
	flag.Parse()

    // Set the log level based on the flag value
    level, err := log.ParseLevel(*logLevel)
    if err != nil {
        level = log.InfoLevel
    }
    log.SetLevel(level)
	log.SetFormatter(&log.TextFormatter{
        DisableTimestamp: true, // Disable timestamp
    })

	if *kubeconfig == "" && !inCluster() {
		*kubeconfig = filepath.Join(homedir.HomeDir(), ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Fatalf("Error building kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating clientset: %v", err)
	}

	pkg.PrintNodeRoutes(clientset)

	ctx := context.Background()

	stopCh := make(chan struct{})
	defer close(stopCh)

	if (*controller) {
		informerFactory := informers.NewSharedInformerFactory(clientset, time.Duration(*resyncPeriod) * time.Second)
		serviceInformer := informerFactory.Core().V1().Services().Informer()

		serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				service := obj.(*v1.Service)
				if service.Spec.Type == v1.ServiceTypeLoadBalancer {

					lbDNS := service.Name + "." + service.Namespace + "." + *domain
					if err := updateServiceStatus(ctx, clientset, lbDNS, service); err != nil {
						log.Errorf("Error updating service status: %v\n", err)
					}
				}
			},
		})

		informerFactory.Start(stopCh)
	}

	// Create a DNS server
	dnsServer := &dns.Server{Addr: *listen, Net: "udp"}

	// Setup DNS handler
	dns.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true

		if r.Question[0].Qtype == dns.TypeA {

			name := strings.TrimSuffix(r.Question[0].Name, "."+*domain+".")
			parts := strings.SplitN(name, ".", 2)
			if len(parts) != 2 {
				log.Warnf("Invalid domain format: %s", name)
				w.WriteMsg(m)
				return
			}
			serviceName, namespace := parts[0], parts[1]

			endpoints, err := getEndpoints(clientset, serviceName, namespace)
			if err != nil {
				log.Errorf("Error getting Endpoints for %s: %v", serviceName, err)
				w.WriteMsg(m)
				return
			}

			for _, subset := range endpoints.Subsets {
				for _, address := range subset.Addresses {
					rr := dns.TypeA
					ip := net.ParseIP(address.IP)
					m.Answer = append(m.Answer, &dns.A{
						Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: rr, Class: dns.ClassINET, Ttl: uint32(*ttl)},
						A:   ip,
					})
				}


			}
			// Shuffle the responses so we get some load balancing
			for i := range m.Answer {
				j := rand.Intn(i + 1)
				m.Answer[i], m.Answer[j] = m.Answer[j], m.Answer[i]
			}

			log.WithFields(log.Fields{
				"svc": serviceName,
				"ns": namespace,
			}).Debug(m.Answer)
		}

		w.WriteMsg(m)
		log.Tracef("%v", m)
	})

	// Start DNS server
	go func() {
		err := dnsServer.ListenAndServe()
		if err != nil {
			log.Fatalf("Error starting DNS server: %v", err)
		}
	}()
	log.Infof("DNS server started on %s", dnsServer.Addr)

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

// inCluster checks whether the code is running inside a Kubernetes cluster
func inCluster() bool {
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return err == nil
}

func updateServiceStatus(ctx context.Context, clientset *kubernetes.Clientset, lbDNS string, svc *v1.Service) error {
	if len(svc.Status.LoadBalancer.Ingress) != 1 ||
		svc.Status.LoadBalancer.Ingress[0].IP != "" ||
		svc.Status.LoadBalancer.Ingress[0].Hostname != lbDNS {
		log.WithFields(log.Fields{
			"svc": svc.Name,
			"ns": svc.Namespace,
			"lb": lbDNS,
		}).Info("Set host for ", svc.Name)

		svc.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{
			{
				Hostname: lbDNS,
			},
		}
		if _, err := clientset.CoreV1().Services(svc.Namespace).UpdateStatus(ctx, svc, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
