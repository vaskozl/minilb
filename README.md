# Why ?

* MetalLB L2
    * Does not offer any loadbalancing between service replicas and throughput is limited to a single node
    * Slow failover that's hard to debug
* BGP solutions including MetalLB, Calico, Cilium and kube-rouer have other limitations::
    * Forward all non-peer traffic through a default gateway. This limits your bandwith to the cluster and adds an extra hop.
    * Can suffer from assymetric routing issues LANs and requires disabling ICMP redirects.
    * Requires a BGP capable router at all times which can limit flexibility
    * Nodes generally get a static subnet and BGP does close to nothing, neither Cilium nor Flannel actually use it to "distribute" routes between nodes since the routes are readily available from the APIServer.

Furthemore other load-balancing solutions tend to be much heavier than, `minilb` - requiring daemonsets that tend to use between 15-100m CPU and between 35-150Mi of RAM in my testing. This amounts to undue energy usage and less room for your actual applications. `minilb` works with any CNI. `flannel` is particularly suited, since when in `host-gw` mode performs native routing similar to the other CNIs with no VXLAN penalties while using only 1m/10Mi per node.

# How

At startup `minilb` looks up all routes to nodes and prints them out for you so you can set on default gateways
or even directly on devices. The manual step is similar to how you would add each node as a BGP peer, but instead you just add the static route to the node. The PodCIDRs are normally assigned by [kube-controller-manager](https://kubernetes.io/docs/reference/command-line-tools-reference/kube-controller-manager/) and are static once the node is provisioned.

On startup `minilb` prints:
```
Add the following routes to your default gateway (router):
route -n add -net 10.244.0.0/24 192.168.1.30
route -n add -net 10.244.1.0/24 192.168.1.31
route -n add -net 10.244.2.0/24 192.168.1.32

2024/05/11 13:11:06 DNS server started on :53
;; opcode: QUERY, status: NOERROR, id: 10290
;; flags: qr aa rd; QUERY: 1, ANSWER: 2, AUTHORITY: 0, ADDITIONAL: 0

;; QUESTION SECTION:
;mosquitto.automation.minilb.    IN    A

;; ANSWER SECTION:
mosquitto.automation.minilb.    5    IN    A    10.244.19.168
mosquitto.automation.minilb.    5    IN    A    10.244.1.103
```


 The idea is that the router has static routes for the podcidr for each node (based on the podCIDR of the node object), and we run a resolver which resolves service -> pod ips. When you add a node you have to add a new static route but even with BGP you generally have to add new nodes as neighbours so it's no different. One of the benefits is that you can advertise the static routes over DHCP to remove the hop through the router for traffic local to the LAN. This also means you don't need BGP and can use any router that supports static routes. To make ingresses work, the controller could set the externalIP of each service to the hostname that resolves to the pods, that way external-dns and k8s-gateway should just work. What do you guys think about the idea?


`minilb` updates the external IPs of LoadBalancer services to the configured domain.
```
$ k get svc -n haproxy internal-kubernetes-ingress
NAME                          TYPE           CLUSTER-IP       EXTERNAL-IP                                  PORT(S)                                                                               AGE
internal-kubernetes-ingress   LoadBalancer   10.110.115.188   internal-kubernetes-ingress.haproxy.minilb   80:...
```

They resolve directly to the pod which your network knows how to route:
```
$ nslookup internal-kubernetes-ingress.haproxy.minilb
Server:        192.168.1.1
Address:    192.168.1.1#53

Name:    internal-kubernetes-ingress.haproxy.minilb
Address: 10.244.19.176
Name:    internal-kubernetes-ingress.haproxy.minilb
Address: 10.244.1.104
```

When `k8s-gateway` or `external-dns` are present, they will CNAME any ingress hosts to our minilb service hostname.

```
$ k get ingress paperless-ngx
NAME            CLASS              HOSTS              ADDRESS                                      PORTS   AGE
paperless-ngx   haproxy-internal   paperless.sko.ai   internal-kubernetes-ingress.haproxy.minilb   80      22d

$ curl -I https://paperless.sko.ai:8443
HTTP/2 302
content-length: 0
location: https://gate.sko.ai/?rd=https://paperless.sko.ai:8443/
cache-control: no-cache
```

# Limitations

By far the biggest limitations is that because we completely bypass the service ip and  `kube-proxy`, the service `port` to `targetPort` mapping is bypassed. This means that you need to have the containers listening to the same ports you want to access them by. Traditionally this was a problem for ports less than `1024` which required root, but this can now easily be remedied since 1.22:

```
apiVersion: v1
kind: Pod
metadata:
  name: sysctl-example
spec:
  securityContext:
    sysctls:
    - name: net.ipv4.ip_unprivileged_port_start
      value: "80"
```


There are a few other limitations:
    * The upstream needs to respect the short TTLs of the `minilb` response
    * Some apps do DNS lookups only once and cache the results indefinitely.
