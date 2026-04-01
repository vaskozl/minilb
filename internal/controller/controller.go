package controller

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	netv1 "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha3 "sigs.k8s.io/gateway-api/apis/v1alpha3"
	gatewayclientset "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gatewayinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"
	gatewayv1listers "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1"
	gatewayv1alpha3listers "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1alpha3"
)

const (
	HostnameAnnotation        = "minilb/host"
	ExcludeAnnotation         = "minilb/exclude"
	LBClass                   = "minilb"
	endpointSliceServiceLabel = "kubernetes.io/service-name"
)

func isExcluded(annotations map[string]string) bool {
	return annotations[ExcludeAnnotation] == "true"
}

type Controller struct {
	clientset           *kubernetes.Clientset
	domain              string
	endpointSliceLister discoverylisters.EndpointSliceLister
	ingressLister       netv1.IngressLister
	httpRouteLister     gatewayv1listers.HTTPRouteLister
	grpcRouteLister     gatewayv1listers.GRPCRouteLister
	tlsRouteLister      gatewayv1alpha3listers.TLSRouteLister
	gatewayLister       gatewayv1listers.GatewayLister

	mu         sync.RWMutex
	serviceMap map[string]string // hostname -> service.namespace.domain

	ready bool
}

func New(ctx context.Context, kubeconfig, domain string, resyncSeconds int) (*Controller, error) {
	cfg, err := buildConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	c := &Controller{
		clientset:  cs,
		domain:     domain,
		serviceMap: make(map[string]string),
	}

	resync := time.Duration(resyncSeconds) * time.Second
	factory := informers.NewSharedInformerFactory(cs, resync)

	c.ingressLister = factory.Networking().V1().Ingresses().Lister()
	c.endpointSliceLister = factory.Discovery().V1().EndpointSlices().Lister()

	factory.Core().V1().Services().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.onAddOrUpdate(ctx, obj) },
		UpdateFunc: func(_, obj interface{}) { c.onAddOrUpdate(ctx, obj) },
		DeleteFunc: c.onDelete,
	})

	var gwSyncs []cache.InformerSynced
	if gwClient, gErr := gatewayclientset.NewForConfig(cfg); gErr != nil {
		slog.Warn("Gateway API client init failed", "err", gErr)
	} else {
		gwFactory := gatewayinformers.NewSharedInformerFactory(gwClient, resync)
		v1i := gwFactory.Gateway().V1()
		c.httpRouteLister = v1i.HTTPRoutes().Lister()
		c.grpcRouteLister = v1i.GRPCRoutes().Lister()
		c.gatewayLister = v1i.Gateways().Lister()
		c.tlsRouteLister = gwFactory.Gateway().V1alpha3().TLSRoutes().Lister()
		gwFactory.Start(ctx.Done())
		gwSyncs = append(gwSyncs,
			v1i.HTTPRoutes().Informer().HasSynced,
			v1i.GRPCRoutes().Informer().HasSynced,
			v1i.Gateways().Informer().HasSynced,
			gwFactory.Gateway().V1alpha3().TLSRoutes().Informer().HasSynced,
		)
	}

	factory.Start(ctx.Done())

	syncs := []cache.InformerSynced{
		factory.Core().V1().Services().Informer().HasSynced,
		factory.Networking().V1().Ingresses().Informer().HasSynced,
		factory.Discovery().V1().EndpointSlices().Informer().HasSynced,
	}
	syncs = append(syncs, gwSyncs...)

	if !cache.WaitForCacheSync(ctx.Done(), syncs...) {
		return nil, errors.New("timed out waiting for informer caches to sync")
	}
	c.ready = true
	slog.Info("Informer caches synced")

	return c, nil
}

// Ready returns true once informer caches have synced.
func (c *Controller) Ready() bool {
	return c.ready
}

func (c *Controller) onAddOrUpdate(ctx context.Context, obj interface{}) {
	svc, ok := obj.(*v1.Service)
	if !ok || svc.Spec.Type != v1.ServiceTypeLoadBalancer {
		return
	}
	if svc.Spec.LoadBalancerClass == nil || *svc.Spec.LoadBalancerClass != LBClass {
		return
	}

	lbDNS := svc.Name + "." + svc.Namespace + "." + c.domain
	if err := c.updateServiceStatus(ctx, lbDNS, svc); err != nil {
		slog.Error("Failed to update service status", "err", err)
	}

	if raw, ok := svc.Annotations[HostnameAnnotation]; ok {
		c.mu.Lock()
		for _, hostname := range parseHostnames(raw) {
			c.serviceMap[hostname] = lbDNS
		}
		c.mu.Unlock()
		slog.Info("Mapped hostname(s)", "hosts", raw, "svc", svc.Namespace+"/"+svc.Name)
	}
}

func (c *Controller) onDelete(obj interface{}) {
	svc, ok := obj.(*v1.Service)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		svc, ok = tombstone.Obj.(*v1.Service)
		if !ok {
			return
		}
	}

	raw, ok := svc.Annotations[HostnameAnnotation]
	if !ok {
		return
	}
	c.mu.Lock()
	for _, hostname := range parseHostnames(raw) {
		delete(c.serviceMap, hostname)
	}
	c.mu.Unlock()
	slog.Info("Unmapped hostname(s)", "hosts", raw, "svc", svc.Namespace+"/"+svc.Name)
}

// parseHostnames splits a comma-separated annotation value into canonical hostnames.
func parseHostnames(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		key := CanonicalHostname(p)
		if key != "" {
			out = append(out, key)
		}
	}
	return out
}

func (c *Controller) updateServiceStatus(ctx context.Context, lbDNS string, svc *v1.Service) error {
	ing := svc.Status.LoadBalancer.Ingress
	if len(ing) == 1 && ing[0].IP == "" && ing[0].Hostname == lbDNS {
		return nil
	}
	slog.Info("Setting LB host", "svc", svc.Name, "ns", svc.Namespace, "lb", lbDNS)
	patch := svc.DeepCopy()
	patch.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{{Hostname: lbDNS}}
	_, err := c.clientset.CoreV1().Services(svc.Namespace).UpdateStatus(ctx, patch, metav1.UpdateOptions{})
	return err
}

// GetEndpointIPs returns the ready addresses for a service of the given type, using the informer cache.
func (c *Controller) GetEndpointIPs(serviceName, namespace string, addrType discoveryv1.AddressType) ([]string, error) {
	sel := labels.Set{endpointSliceServiceLabel: serviceName}.AsSelector()
	slices, err := c.endpointSliceLister.EndpointSlices(namespace).List(sel)
	if err != nil {
		return nil, err
	}

	var ips []string
	for _, slice := range slices {
		if slice.AddressType != addrType {
			continue
		}
		for _, ep := range slice.Endpoints {
			if !IsReadyEndpoint(&ep) {
				continue
			}
			ips = append(ips, ep.Addresses...)
		}
	}

	if len(ips) == 0 {
		return nil, errors.New("no ready endpoints found")
	}
	return ips, nil
}

func (c *Controller) GetAddressForHostname(hostname string) (string, error) {
	canonical := CanonicalHostname(hostname)
	if canonical == "" {
		return "", errors.New("invalid hostname")
	}

	c.mu.RLock()
	svcHost, ok := c.serviceMap[canonical]
	c.mu.RUnlock()
	if ok {
		return svcHost, nil
	}

	resolvers := []func(string) (string, error){
		c.resolveIngressHostname,
		c.resolveHTTPRouteHostname,
		c.resolveTLSRouteHostname,
		c.resolveGRPCRouteHostname,
	}
	for _, resolve := range resolvers {
		addr, err := resolve(canonical)
		if err != nil {
			return "", err
		}
		if addr != "" {
			return addr, nil
		}
	}
	return "", errors.New("hostname not found")
}

// PrintRoutes logs the static routes needed on the default gateway.
func (c *Controller) PrintRoutes() {
	nodes, err := c.clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		slog.Warn("Failed to list nodes", "err", err)
		return
	}
	slog.Info("Add the following routes to your default gateway (router):")
	for _, node := range nodes.Items {
		ip := NodeInternalIP(node.Status.Addresses)
		for _, cidr := range node.Spec.PodCIDRs {
			slog.Info("route", "cidr", cidr, "via", ip)
		}
	}
}

// NodeInternalIP returns the first InternalIP from a node's addresses.
func NodeInternalIP(addrs []v1.NodeAddress) string {
	for _, a := range addrs {
		if a.Type == v1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

func (c *Controller) resolveIngressHostname(hostname string) (string, error) {
	if c.ingressLister == nil {
		return "", nil
	}
	ingresses, err := c.ingressLister.List(labels.Everything())
	if err != nil {
		return "", err
	}
	for _, ing := range ingresses {
		if isExcluded(ing.Annotations) {
			continue
		}
		for _, rule := range ing.Spec.Rules {
			if !HostnameMatches(rule.Host, hostname) {
				continue
			}
			if len(ing.Status.LoadBalancer.Ingress) == 0 {
				continue
			}
			lb := ing.Status.LoadBalancer.Ingress[0]
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

func (c *Controller) resolveHTTPRouteHostname(hostname string) (string, error) {
	if c.httpRouteLister == nil {
		return "", nil
	}
	routes, err := c.httpRouteLister.List(labels.Everything())
	if err != nil {
		return "", err
	}
	for _, route := range routes {
		if isExcluded(route.Annotations) {
			continue
		}
		if !ContainsMatchingHostname(hostnamesToStrings[gwapiv1alpha3.Hostname](route.Spec.Hostnames), hostname) {
			continue
		}
		if addr := c.gatewayAddressForParentRefs(route.Namespace, route.Spec.ParentRefs); addr != "" {
			return addr, nil
		}
	}
	return "", nil
}

func (c *Controller) resolveTLSRouteHostname(hostname string) (string, error) {
	if c.tlsRouteLister == nil {
		return "", nil
	}
	routes, err := c.tlsRouteLister.List(labels.Everything())
	if err != nil {
		return "", err
	}
	for _, route := range routes {
		if isExcluded(route.Annotations) {
			continue
		}
		if !ContainsMatchingHostname(hostnamesToStrings(route.Spec.Hostnames), hostname) {
			continue
		}
		if addr := c.gatewayAddressForParentRefs(route.Namespace, route.Spec.ParentRefs); addr != "" {
			return addr, nil
		}
	}
	return "", nil
}

func (c *Controller) resolveGRPCRouteHostname(hostname string) (string, error) {
	if c.grpcRouteLister == nil {
		return "", nil
	}
	routes, err := c.grpcRouteLister.List(labels.Everything())
	if err != nil {
		return "", err
	}
	for _, route := range routes {
		if isExcluded(route.Annotations) {
			continue
		}
		if !ContainsMatchingHostname(hostnamesToStrings(route.Spec.Hostnames), hostname) {
			continue
		}
		if addr := c.gatewayAddressForParentRefs(route.Namespace, route.Spec.ParentRefs); addr != "" {
			return addr, nil
		}
	}
	return "", nil
}

func (c *Controller) gatewayAddressForParentRefs(routeNS string, refs []gwapiv1.ParentReference) string {
	if c.gatewayLister == nil {
		return ""
	}
	for _, ref := range refs {
		if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
			continue
		}
		ns := routeNS
		if ref.Namespace != nil && *ref.Namespace != "" {
			ns = string(*ref.Namespace)
		}
		gw, err := c.gatewayLister.Gateways(ns).Get(string(ref.Name))
		if err != nil || gw == nil {
			continue
		}
		for _, addr := range gw.Status.Addresses {
			if addr.Value != "" {
				return addr.Value
			}
		}
	}
	return ""
}
