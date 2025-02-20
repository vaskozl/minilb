package k8s

import (
	"context"
	"errors"
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

var (
	clientset     *kubernetes.Clientset
	ingressLister netv1.IngressLister
)

func Run(ctx context.Context) {
	clientset = NewClient()

	informerFactory := informers.NewSharedInformerFactory(clientset, time.Duration(*config.ResyncPeriod)*time.Second)
	ingressLister = informerFactory.Networking().V1().Ingresses().Lister()
	serviceInformer := informerFactory.Core().V1().Services().Informer()

	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			service := obj.(*v1.Service)
			if service.Spec.Type == v1.ServiceTypeLoadBalancer {

				lbDNS := service.Name + "." + service.Namespace + "." + *config.Domain
				if err := updateServiceStatus(ctx, clientset, lbDNS, service); err != nil {
					klog.Error(err, "Error updating service status")
				}
			}
		},
		UpdateFunc: func(old, obj interface{}) {
			service := obj.(*v1.Service)
			if service.Spec.Type == v1.ServiceTypeLoadBalancer {

				lbDNS := service.Name + "." + service.Namespace + "." + *config.Domain
				if err := updateServiceStatus(ctx, clientset, lbDNS, service); err != nil {
					klog.Error(err, "Error updating service status")
				}
			}
		},
	})

	informerFactory.Start(ctx.Done())
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
