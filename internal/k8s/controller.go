package k8s

import (
	"context"
	"errors"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	netv1 "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/vaskozl/minilb/internal/config"
)

const HostnameAnnotation = "minilb/host"
const LBClass = "minilb"
const endpointSliceServiceLabel = "kubernetes.io/service-name"

var (
	clientset     *kubernetes.Clientset
	ingressLister netv1.IngressLister
	serviceMap    = make(map[string]string) // Map of hostname -> Service
	mutex         = sync.RWMutex{}
)

func Run(ctx context.Context) {
	clientset = NewClient()

	informerFactory := informers.NewSharedInformerFactory(clientset, time.Duration(*config.ResyncPeriod)*time.Second)
	ingressLister = informerFactory.Networking().V1().Ingresses().Lister()
	serviceInformer := informerFactory.Core().V1().Services().Informer()

	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(newObj interface{}) { onAddOrUpdate(ctx, newObj) },
		UpdateFunc: func(oldObj, newObj interface{}) { onAddOrUpdate(ctx, newObj) },
	})

	informerFactory.Start(ctx.Done())
}

// onAddOrUpdate updates sets the svc hostname and updates the hostname map
func onAddOrUpdate(ctx context.Context, obj interface{}) {
	svc, ok := obj.(*v1.Service)
	if !ok {
		return
	}

	if svc.Spec.Type != v1.ServiceTypeLoadBalancer {
		return
	}

	if svc.Spec.LoadBalancerClass == nil || *(svc.Spec.LoadBalancerClass) != LBClass {
		return
	}

	lbDNS := svc.Name + "." + svc.Namespace + "." + *config.Domain
	if err := updateServiceStatus(ctx, clientset, lbDNS, svc); err != nil {
		klog.Error(err, "Error updating service status")
	}

	if hostname, exists := svc.Annotations[HostnameAnnotation]; exists {
		mutex.Lock()
		serviceMap[hostname] = lbDNS
		mutex.Unlock()
		klog.Infof("Updated: Hostname %s -> Service %s/%s\n", hostname, svc.Namespace, svc.Name)
	}
}

func GetEndpoints(serviceName string, namespace string) (*v1.Endpoints, error) {
	labelSelector := labels.Set{
		endpointSliceServiceLabel: serviceName,
	}.AsSelector().String()

	endpointSlices, err := clientset.DiscoveryV1().EndpointSlices(namespace).List(
		context.Background(),
		metav1.ListOptions{LabelSelector: labelSelector},
	)
	if err != nil {
		return nil, err
	}

	subsets := make([]v1.EndpointSubset, 0, len(endpointSlices.Items))
	for _, slice := range endpointSlices.Items {
		if slice.AddressType != discoveryv1.AddressTypeIPv4 {
			continue
		}

		subset := v1.EndpointSubset{
			Ports: convertEndpointPorts(slice.Ports),
		}

		for _, endpoint := range slice.Endpoints {
			if !isReadyEndpoint(&endpoint) {
				continue
			}

			for _, address := range endpoint.Addresses {
				subset.Addresses = append(subset.Addresses, v1.EndpointAddress{
					IP: address,
				})
			}
		}

		if len(subset.Addresses) > 0 {
			subsets = append(subsets, subset)
		}
	}

	if len(subsets) == 0 {
		return nil, errors.New("no ready IPv4 endpoints found")
	}

	return &v1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Subsets: subsets,
	}, nil
}

func convertEndpointPorts(ports []discoveryv1.EndpointPort) []v1.EndpointPort {
	if len(ports) == 0 {
		return nil
	}

	result := make([]v1.EndpointPort, 0, len(ports))
	for _, port := range ports {
		var endpointPort v1.EndpointPort
		if port.Name != nil {
			endpointPort.Name = *port.Name
		}

		if port.Protocol != nil {
			endpointPort.Protocol = v1.Protocol(*port.Protocol)
		} else {
			endpointPort.Protocol = v1.ProtocolTCP
		}

		if port.Port != nil {
			endpointPort.Port = *port.Port
		}

		if port.AppProtocol != nil {
			endpointPort.AppProtocol = port.AppProtocol
		}

		result = append(result, endpointPort)
	}

	return result
}

func isReadyEndpoint(endpoint *discoveryv1.Endpoint) bool {
	if endpoint == nil || len(endpoint.Addresses) == 0 {
		return false
	}

	if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
		return false
	}

	if endpoint.Conditions.Serving != nil && !*endpoint.Conditions.Serving {
		return false
	}

	if endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating {
		return false
	}

	return true
}

func GetAddressForHostname(hostname string) (string, error) {
	// List all Ingresses across all namespaces
	mutex.RLock()
	svcHost, ok := serviceMap[hostname]
	mutex.RUnlock()
	if ok {
		return svcHost, nil
	}

	ingresses, err := ingressLister.List(labels.Everything())
	if err != nil {
		return "", err
	}

	// Iterate over ingresses to find a matching hostname
	for _, ingress := range ingresses {
		for _, rule := range ingress.Spec.Rules {
			if rule.Host == hostname {
				// Found a matching ingress rule, get the associated address
				if len(ingress.Status.LoadBalancer.Ingress) > 0 {
					return ingress.Status.LoadBalancer.Ingress[0].Hostname, nil
				}
			}
		}
	}

	return "", errors.New("hostname not found in any ingress")
}

func updateServiceStatus(ctx context.Context, clientset *kubernetes.Clientset, lbDNS string, svc *v1.Service) error {
	if len(svc.Status.LoadBalancer.Ingress) != 1 ||
		svc.Status.LoadBalancer.Ingress[0].IP != "" ||
		svc.Status.LoadBalancer.Ingress[0].Hostname != lbDNS {
		klog.InfoS("Set host",
			"svc", svc.Name,
			"ns", svc.Namespace,
			"lb", lbDNS,
		)

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
