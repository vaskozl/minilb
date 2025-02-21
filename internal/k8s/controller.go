package k8s

import (
	"context"
	"errors"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
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
	endpoints, err := clientset.CoreV1().Endpoints(namespace).Get(context.Background(), serviceName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return endpoints, nil
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
