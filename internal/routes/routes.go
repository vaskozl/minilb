package routes

import (
	"context"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vaskozl/minilb/internal/k8s"
)

func Print() {
	clientset := k8s.NewClient()
	nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return
	}

	log.Info("Add the following routes to your default gateway (router):\n")
	for _, node := range nodes.Items {
		nodeIP := getNodeAddress(node.Status.Addresses)
		for _, cidr := range node.Spec.PodCIDRs {
			log.Infof("ip route add %s via %s\n", cidr, nodeIP)
		}
	}
}

func getNodeAddress(addresses []corev1.NodeAddress) string {
	for _, addr := range addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}
