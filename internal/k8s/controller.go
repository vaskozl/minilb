package k8s

import (
	"context"
	"errors"
	"strings"
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
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha3 "sigs.k8s.io/gateway-api/apis/v1alpha3"
	gatewayclientset "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gatewayinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"
	gatewayv1listers "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1"
	gatewayv1alpha3listers "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1alpha3"

	"github.com/vaskozl/minilb/internal/config"
)

const HostnameAnnotation = "minilb/host"
const LBClass = "minilb"
const endpointSliceServiceLabel = "kubernetes.io/service-name"

var (
	clientset       *kubernetes.Clientset
	ingressLister   netv1.IngressLister
	httpRouteLister gatewayv1listers.HTTPRouteLister
	grpcRouteLister gatewayv1listers.GRPCRouteLister
	tlsRouteLister  gatewayv1alpha3listers.TLSRouteLister
	gatewayLister   gatewayv1listers.GatewayLister
	serviceMap      = make(map[string]string) // Map of hostname -> Service
	mutex           = sync.RWMutex{}
)

func Run(ctx context.Context) {
	restConfig := BuildConfig()

	var err error
	clientset, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		panic(err)
	}

	informerFactory := informers.NewSharedInformerFactory(clientset, time.Duration(*config.ResyncPeriod)*time.Second)
	ingressLister = informerFactory.Networking().V1().Ingresses().Lister()
	serviceInformer := informerFactory.Core().V1().Services().Informer()

	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(newObj interface{}) { onAddOrUpdate(ctx, newObj) },
		UpdateFunc: func(oldObj, newObj interface{}) { onAddOrUpdate(ctx, newObj) },
	})

	var gatewayInformerFactory gatewayinformers.SharedInformerFactory
	if gatewayClient, gErr := gatewayclientset.NewForConfig(restConfig); gErr != nil {
		klog.Warningf("Gateway API client initialization failed: %v", gErr)
	} else {
		gatewayInformerFactory = gatewayinformers.NewSharedInformerFactory(gatewayClient, time.Duration(*config.ResyncPeriod)*time.Second)
		v1Informers := gatewayInformerFactory.Gateway().V1()
		httpRouteLister = v1Informers.HTTPRoutes().Lister()
		grpcRouteLister = v1Informers.GRPCRoutes().Lister()
		gatewayLister = v1Informers.Gateways().Lister()

		v1alpha3Informers := gatewayInformerFactory.Gateway().V1alpha3()
		tlsRouteLister = v1alpha3Informers.TLSRoutes().Lister()
	}

	informerFactory.Start(ctx.Done())
	if gatewayInformerFactory != nil {
		gatewayInformerFactory.Start(ctx.Done())
	}
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
		hostnameKey := canonicalHostname(hostname)
		if hostnameKey == "" {
			return
		}
		mutex.Lock()
		serviceMap[hostnameKey] = lbDNS
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
	canonical := canonicalHostname(hostname)
	if canonical == "" {
		return "", errors.New("invalid hostname")
	}

	mutex.RLock()
	svcHost, ok := serviceMap[canonical]
	mutex.RUnlock()
	if ok {
		return svcHost, nil
	}

	if addr, err := resolveIngressHostname(canonical); err != nil {
		return "", err
	} else if addr != "" {
		return addr, nil
	}

	if addr, err := resolveHTTPRouteHostname(canonical); err != nil {
		return "", err
	} else if addr != "" {
		return addr, nil
	}

	if addr, err := resolveTLSRouteHostname(canonical); err != nil {
		return "", err
	} else if addr != "" {
		return addr, nil
	}

	if addr, err := resolveGRPCRouteHostname(canonical); err != nil {
		return "", err
	} else if addr != "" {
		return addr, nil
	}

	return "", errors.New("hostname not found")
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

func resolveIngressHostname(hostname string) (string, error) {
	if ingressLister == nil {
		return "", nil
	}

	ingresses, err := ingressLister.List(labels.Everything())
	if err != nil {
		return "", err
	}

	for _, ingress := range ingresses {
		for _, rule := range ingress.Spec.Rules {
			if !hostnameMatches(rule.Host, hostname) {
				continue
			}

			if len(ingress.Status.LoadBalancer.Ingress) == 0 {
				continue
			}

			lb := ingress.Status.LoadBalancer.Ingress[0]
			if lb.Hostname != "" {
				return lb.Hostname, nil
			}
			if lb.IP != "" {
				return lb.IP, nil
			}
		}
	}

	return "", nil
}

func resolveHTTPRouteHostname(hostname string) (string, error) {
	if httpRouteLister == nil {
		return "", nil
	}

	routes, err := httpRouteLister.List(labels.Everything())
	if err != nil {
		return "", err
	}

	for _, route := range routes {
		if !containsMatchingHostname(hostnamesToStrings[gwapiv1alpha3.Hostname](route.Spec.Hostnames), hostname) {
			continue
		}

		if addr := gatewayAddressForParentRefs(route.Namespace, route.Spec.ParentRefs); addr != "" {
			return addr, nil
		}
	}

	return "", nil
}

func resolveTLSRouteHostname(hostname string) (string, error) {
	if tlsRouteLister == nil {
		return "", nil
	}

	routes, err := tlsRouteLister.List(labels.Everything())
	if err != nil {
		return "", err
	}

	for _, route := range routes {
		if !containsMatchingHostname(hostnamesToStrings(route.Spec.Hostnames), hostname) {
			continue
		}

		if addr := gatewayAddressForParentRefs(route.Namespace, route.Spec.ParentRefs); addr != "" {
			return addr, nil
		}
	}

	return "", nil
}

func resolveGRPCRouteHostname(hostname string) (string, error) {
	if grpcRouteLister == nil {
		return "", nil
	}

	routes, err := grpcRouteLister.List(labels.Everything())
	if err != nil {
		return "", err
	}

	for _, route := range routes {
		if len(route.Spec.Hostnames) == 0 {
			continue
		}

		if !containsMatchingHostname(hostnamesToStrings(route.Spec.Hostnames), hostname) {
			continue
		}

		if addr := gatewayAddressForParentRefs(route.Namespace, route.Spec.ParentRefs); addr != "" {
			return addr, nil
		}
	}

	return "", nil
}

func gatewayAddressForParentRefs(routeNamespace string, parentRefs []gwapiv1.ParentReference) string {
	if gatewayLister == nil {
		return ""
	}

	for _, parentRef := range parentRefs {
		if parentRef.Kind != nil && string(*parentRef.Kind) != "Gateway" {
			continue
		}

		gwNamespace := routeNamespace
		if parentRef.Namespace != nil && *parentRef.Namespace != "" {
			gwNamespace = string(*parentRef.Namespace)
		}

		gateway, err := gatewayLister.Gateways(gwNamespace).Get(string(parentRef.Name))
		if err != nil || gateway == nil {
			continue
		}

		for _, addr := range gateway.Status.Addresses {
			if addr.Value != "" {
				return addr.Value
			}
		}
	}

	return ""
}

func hostnameMatches(candidate string, query string) bool {
	candidate = canonicalHostname(candidate)
	query = canonicalHostname(query)
	if candidate == "" || query == "" {
		return false
	}

	if candidate == query {
		return true
	}

	if strings.HasPrefix(candidate, "*.") {
		suffix := strings.TrimPrefix(candidate, "*.")
		return suffix != "" && len(query) > len(suffix) && strings.HasSuffix(query, suffix)
	}

	return false
}

func canonicalHostname(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

func containsMatchingHostname(hosts []string, query string) bool {
	if len(hosts) == 0 {
		return false
	}

	for _, host := range hosts {
		if hostnameMatches(host, query) {
			return true
		}
	}

	return false
}

func hostnamesToStrings[T ~string](hosts []T) []string {
	if len(hosts) == 0 {
		return nil
	}

	result := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if host == "" {
			continue
		}
		result = append(result, string(host))
	}
	return result
}
