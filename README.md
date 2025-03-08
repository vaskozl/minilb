# minilb - Lightweight DNS based load balancer for Kubernetes

## Why create a new loadbalancer?

While MetalLB has long been the standard and many CNIs now supports BGP advertisement, issues still remain:

* MetalLB L2:
    * Does not offer any loadbalancing between service replicas and throughput is limited to a single node
    * Slow failover
* BGP solutions including MetalLB, Calico, Cilium and kube-rouer have other limitations:
    * Forward all non-peer traffic through a default gateway. This limits your bandwith to the cluster and adds an extra hop
    * Can suffer from assymetric routing issues on LANs and generally requires disabling ICMP redirects
    * Requires a BGP capable router at all times which can limit flexibility
    * Nodes generally get a static subnet and BGP does close to nothing, neither Cilium nor Flannel actually use it to "distribute" routes between nodes since the routes are readily available from the APIServer.

Furthemore other load-balancing solutions tend to be much heavier - requiring daemonsets that tend to use between 15-100m CPU and between 35-150Mi of RAM in my tests. This amounts to undue energy usage and less room for your actual applications. `flannel` is particularly suited, since when in `host-gw` mode performs native routing similar to the other CNIs with no VXLAN penalties while using only 1m/10Mi per node.

Lastly all other solutions rely on CRDs which make boostraping a cluster that much more difficult.

## How `minilb` works

At startup `minilb` looks up all routes to nodes and prints them out for you so you can set on default gateways
or even directly on devices. The manual step is similar to how you would add each node as a BGP peer, but instead you just add the static route to the node. The podCIDRss are normally assigned by [kube-controller-manager](https://kubernetes.io/docs/reference/command-line-tools-reference/kube-controller-manager/) and are static once the node is provisioned.

On startup `minilb` prints:
```
Add the following routes to your default gateway (router):
ip route add 10.244.0.0/24 via 192.168.1.30
ip route add 10.244.1.0/24 via 192.168.1.31
ip route add 10.244.2.0/24 via 192.168.1.32
```

Example queries that `minilb` handles:
```
2024/05/11 13:11:06 DNS server started on :53
;; opcode: QUERY, status: NOERROR, id: 10290
;; flags: qr aa rd; QUERY: 1, ANSWER: 2, AUTHORITY: 0, ADDITIONAL: 0

;; QUESTION SECTION:
;mosquitto.automation.minilb.    IN    A

;; ANSWER SECTION:
mosquitto.automation.minilb.    5    IN    A    10.244.19.168
mosquitto.automation.minilb.    5    IN    A    10.244.1.103
```


 The idea is that the router has static routes for the podCIDRs for each node (based on the node spec), and we run a resolver which resolves the service "hostname" to pod IPs. One of the benefits is that you can advertise the static routes over DHCP to remove the hop through the router for traffic local to the LAN. This also means you don't need BGP and can use any router that supports static routes. To make ingresses work, the controller sets the `status.loadBalancer.Hostname` of each service to the hostname that resolves to the pods, that way `external-dns` and `k8s-gateway` will CNAME your defined Ingress `hosts` to the associated `.minilb` record.


`minilb` updates the external IPs of LoadBalancer services with the `minilb` LoadBalancerClass the configured domain:
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

Since version `0.0.4` minilb also supports resolving ingresses directly, which removes the need to use `k8s-gateway`.
```
$ k get ingress paperless-ngx
NAME            CLASS              HOSTS              ADDRESS                                      PORTS   AGE
paperless-ngx   haproxy-internal   paperless.sko.ai   internal-kubernetes-ingress.haproxy.minilb   80      22d


% nslookup paperless.sko.ai
Server:		192.168.1.1
Address:	192.168.1.1#53

Name:	paperless.sko.ai
Address: 10.244.19.176
Name:	paperless.sko.ai
Address: 10.244.1.104
```

You may use also assign additional custom hostnames to a service, aside from the `.minilb` hostname, via the `minilb/host` annotation. This can be useful if you want to use TLS with protocols other than HTTP.


For example both `mosquitto.automation.minilb` and `mqtt.sko.ai` will resolve to the service endpoints given the following service metadata:

```yaml
apiVersion: v1
kind: Service
metadata:
  annotations:
    minilb/host: mqtt.sko.ai
  namespace: mqtt
  namespace: automation
```

## Requirements

`minilb` expects your default gateway to have static routes for the nodes `podCIDRs`. In order to help set that up it prints podCIDRs assigned by kube-controller-manager on startup. Typically this is achieved by running `kube-controller-manager` with the `--allocate-node-cidrs` flag.

Both `flanneld` and `kube-router` should require no additional configuration as they use `podCIDRs` by default.

For Cilium the [Kubernetes Host Scope IPAM](https://docs.cilium.io/en/stable/network/concepts/ipam/kubernetes/) should be used. The default is Cluster Scope.

Calico does not use the CIDR's assigned by `kube-controller-manager` but instead assigns blocks of /28 dynamically. This makes it unsuitable for use with `minilb`.

## Deployment

[Reference the example HA deployment deployment](https://github.com/vaskozl/home-infra/tree/main/cluster/minilb). Your network should then be configured to use minilb as a resolver for the `.minilb` (or any other chosen) domain and optionally for any domains used by your ingresses. The suggested way to do this is to expose `minilb` itself as a `NodePort` or a `Daemonset` with `hostPort`. After this you can use `type=LoadBalancer` for everything else!

## Limitations

By far the biggest limitations is that because we completely bypass the service ip and  `kube-proxy`, the service `port` to `targetPort` mapping is bypassed. This means that you need to have the containers listening to the same ports you want to access them by. Traditionally this was a problem for ports less than `1024` which required root, but this is now easily achieved directly since 1.22:

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

There are a few other things which you should consider:

* Users needs to respect the short TTLs of the `minilb` response
* Some apps do DNS lookups only once and cache the results indefinitely.

## Is `minilb` production ready?

No, it's still very new and experimental, but you may use it for small setups such as in your homelab.
