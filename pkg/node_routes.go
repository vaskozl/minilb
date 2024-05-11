// node_routes.go
package pkg

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func PrintNodeRoutes(clientset *kubernetes.Clientset) {
	nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return
	}

	fmt.Printf("Add the following routes to your default gateway (router):\n")
	for _, node := range nodes.Items {
		nodeIP := getNodeAddress(node.Status.Addresses)
		for _, cidr := range node.Spec.PodCIDRs {
			fmt.Printf("route -n add -net %s %s\n", cidr, nodeIP)
		}
	}
	fmt.Printf("\n")
}

func getNodeAddress(addresses []corev1.NodeAddress) string {
	for _, addr := range addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}
