# minilb - Lightweight DNS-based load balancer for Kubernetes

## Why create a new load balancer?

While MetalLB has long been the standard and many CNIs now support BGP advertisement, issues remain:

* MetalLB L2 does not offer any load balancing between service replicas, throughput is limited to a single node, and failover is slow.
* BGP solutions including MetalLB, Calico, Cilium and kube-router have other limitations:
    * Forward all non-peer traffic through a default gateway. This limits your bandwidth to the cluster and adds an extra hop
    * Can suffer from asymmetric routing issues on LANs and generally requires disabling ICMP redirects
    * Requires a BGP-capable router at all times which can limit flexibility
    * Nodes generally get a static subnet and BGP does close to nothing. Neither Cilium nor Flannel use it to distribute routes between nodes as they are readily available from the API server

Other load-balancing solutions tend to be much heavier, requiring daemonsets that use 15-100m CPU and 35-150Mi RAM per node. This wastes energy and leaves less room for actual workloads. `flannel` in `host-gw` mode is particularly well suited, performing native routing with no VXLAN overhead while using only 1m/10Mi per node.

Lastly, all other solutions rely on CRDs which make bootstrapping a cluster more difficult.

## How it works

`minilb` resolves service hostnames directly to pod IPs. Your router has static routes for each node's `podCIDR`, so traffic reaches pods without going through `kube-proxy` or a service VIP.

On startup `minilb` prints the routes you need to add to your default gateway (or advertise via DHCP):

```
Add the following routes to your default gateway (router):
ip route add 10.244.0.0/24 via 192.168.1.30
ip route add 10.244.1.0/24 via 192.168.1.31
ip route add 10.244.2.0/24 via 192.168.1.32
```

The `podCIDRs` are assigned by [kube-controller-manager](https://kubernetes.io/docs/reference/command-line-tools-reference/kube-controller-manager/) and are static once a node is provisioned.

For each `LoadBalancer` service with `loadBalancerClass: minilb`, the controller sets `status.loadBalancer.hostname` to `<service>.<namespace>.<domain>`, which resolves to the service's ready pod IPs:

```
$ kubectl get svc -n haproxy internal-kubernetes-ingress
NAME                          TYPE           CLUSTER-IP       EXTERNAL-IP                                  PORT(S)
internal-kubernetes-ingress   LoadBalancer   10.110.115.188   internal-kubernetes-ingress.haproxy.minilb   80:...

$ nslookup internal-kubernetes-ingress.haproxy.minilb
Name:    internal-kubernetes-ingress.haproxy.minilb
Address: 10.244.19.176
Name:    internal-kubernetes-ingress.haproxy.minilb
Address: 10.244.1.104
```

This means `external-dns` and `k8s-gateway` will CNAME your Ingress hosts to the `.minilb` record automatically.

`minilb` can also resolve Ingress and Gateway API (HTTPRoute, TLSRoute, GRPCRoute) hostnames directly, removing the need for `k8s-gateway`:

```
$ kubectl get ingress paperless-ngx
NAME            CLASS              HOSTS              ADDRESS                                      PORTS   AGE
paperless-ngx   haproxy-internal   paperless.sko.ai   internal-kubernetes-ingress.haproxy.minilb   80      22d

$ nslookup paperless.sko.ai
Name:    paperless.sko.ai
Address: 10.244.19.176
Name:    paperless.sko.ai
Address: 10.244.1.104
```

## Custom hostname annotations

You can assign additional hostnames to a service via the `minilb/host` annotation. Multiple hostnames are specified as a comma-separated list. This is useful for TLS with non-HTTP protocols or for giving a service multiple DNS names.

For example, `mosquitto.automation.minilb`, `mqtt.sko.ai`, and `mqtt.example.com` will all resolve to the service endpoints:

```yaml
apiVersion: v1
kind: Service
metadata:
  annotations:
    minilb/host: mqtt.sko.ai, mqtt.example.com
  name: mosquitto
  namespace: automation
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-kubeconfig` | `""` | Path to a kubeconfig file (auto-detected in-cluster) |
| `-domain` | `minilb` | Zone under which to resolve services |
| `-listen` | `:53` | Address and port for the DNS server |
| `-resync` | `300` | Informer resync period in seconds |
| `-ttl` | `5` | DNS record TTL in seconds |
| `-upstream` | `""` | Upstream DNS server for forwarding (e.g. `1.1.1.1:53`) |
| `-health` | `:8080` | Address for the health/readiness endpoint |

When `-upstream` is set, queries for domains that `minilb` does not handle are forwarded to the upstream resolver. This allows `minilb` to act as a full resolver for clients that point at it exclusively.

Both A and AAAA queries are supported. If your pods have IPv6 addresses (dual-stack or IPv6-only), AAAA queries return the IPv6 endpoint addresses.

The `/healthz` endpoint returns `200 OK` once informer caches are synced, suitable for Kubernetes liveness and readiness probes.

## Requirements

`minilb` expects your default gateway to have static routes for each node's `podCIDR`. It prints these on startup to help you set them up. This requires running `kube-controller-manager` with `--allocate-node-cidrs`.

* `flanneld` and `kube-router` require no additional configuration as they use `podCIDRs` by default.
* Cilium requires [Kubernetes Host Scope IPAM](https://docs.cilium.io/en/stable/network/concepts/ipam/kubernetes/). The default Cluster Scope will not work.
* Calico assigns /28 blocks dynamically instead of using `kube-controller-manager` CIDRs, making it unsuitable for use with `minilb`.

## Deployment

[Reference the example HA deployment](https://github.com/vaskozl/home-infra/tree/main/cluster/minilb). Your network should be configured to use `minilb` as a resolver for the `.minilb` domain and optionally for any domains used by your ingresses. The suggested approach is to expose `minilb` as a `NodePort` or via a `DaemonSet` with `hostPort`. After that you can use `type=LoadBalancer` for everything else.

## Limitations

Because `minilb` bypasses the service VIP and `kube-proxy`, the service `port` to `targetPort` mapping is not applied. Containers must listen on the same ports you want to reach them on. Since Kubernetes 1.22 this is straightforward even for privileged ports:

```yaml
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

Other considerations:

* Clients must respect the short TTLs in `minilb` responses.
* Some applications perform DNS lookups only once and cache the result indefinitely.

## Is `minilb` production ready?

No. It is still new and experimental, but it works well for small setups such as a homelab.
