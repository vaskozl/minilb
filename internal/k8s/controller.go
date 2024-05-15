package k8s

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/vaskozl/minilb/internal/config"
)

var clientset *kubernetes.Clientset

func Run() {
	ctx := context.Background()

	clientset = NewClient()

	informerFactory := informers.NewSharedInformerFactory(clientset, time.Duration(*config.ResyncPeriod)*time.Second)
	serviceInformer := informerFactory.Core().V1().Services().Informer()

	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			service := obj.(*v1.Service)
			if service.Spec.Type == v1.ServiceTypeLoadBalancer {

				lbDNS := service.Name + "." + service.Namespace + "." + *config.Domain
				if err := updateServiceStatus(ctx, clientset, lbDNS, service); err != nil {
					log.Error(err, "Error updating service status")
				}
			}
		},
	})

	stopCh := make(chan struct{})
	defer close(stopCh)
	informerFactory.Start(stopCh)
}

func GetEndpoints(serviceName string, namespace string) (*v1.Endpoints, error) {
	endpoints, err := clientset.CoreV1().Endpoints(namespace).Get(context.Background(), serviceName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return endpoints, nil
}

func updateServiceStatus(ctx context.Context, clientset *kubernetes.Clientset, lbDNS string, svc *v1.Service) error {
	if len(svc.Status.LoadBalancer.Ingress) != 1 ||
		svc.Status.LoadBalancer.Ingress[0].IP != "" ||
		svc.Status.LoadBalancer.Ingress[0].Hostname != lbDNS {
		log.WithFields(log.Fields{
			"svc": svc.Name,
			"ns":  svc.Namespace,
			"lb":  lbDNS,
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
