Currently toying with the idea of replacing BGP with a more lightweight loadbalancer for clusters where PodCIDR is fixed per node. This is what I have:
minilb % go run main.go node_routes.go -kubeconfig ~/.kube/config
Add the following routes to your default gateway (router):
route -n add -net 10.244.5.0/24 192.168.1.30
route -n add -net 10.244.0.0/24 192.168.1.31
route -n add -net 10.244.15.0/24 192.168.1.32
route -n add -net 10.244.18.0/24 192.168.1.33
route -n add -net 10.244.19.0/24 192.168.1.34
route -n add -net 10.244.1.0/24 192.168.1.35
route -n add -net 10.244.3.0/24 192.168.1.41

2024/05/11 13:11:06 DNS server started on :53
;; opcode: QUERY, status: NOERROR, id: 10290
;; flags: qr aa rd; QUERY: 1, ANSWER: 2, AUTHORITY: 0, ADDITIONAL: 0

;; QUESTION SECTION:
;mosquitto.automation.k8s.    IN    A

;; ANSWER SECTION:
mosquitto.automation.k8s.    5    IN    A    10.244.19.168
mosquitto.automation.k8s.    5    IN    A    10.244.1.103
 The idea is that the router has static routes for the podcidr for each node (based on the podCIDR of the node object), and we run a resolver which resolves service -> pod ips. When you add a node you have to add a new static route but even with BGP you generally have to add new nodes as neighbours so it's no different. The benefit being you can advertise the routes over DHCP to remove the hop through the router for local traffic. Also means you don't need BGP and can use any router that supports static routes. To make ingresses work, the controller could set the externalIP of each service to the hostname that resolves to the pods, that way external-dns and k8s-gateway should just work. What do you guys think about the idea?

TODO:
* update: loadBalancer.ingress.hostname to the custom domain so k8s gateway picks up the CNAME
