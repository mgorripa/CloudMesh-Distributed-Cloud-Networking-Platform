# Linux data plane experiment

This independent lab uses the same Go IPAM allocator as the tested library. It generates an explicit command plan and creates two Linux network namespaces joined by a veth underlay and a point-to-point VXLAN overlay (VNI 44). IPv4 and IPv6 overlay addresses are allocated programmatically. TCP ingress filtering permits port 8081 and drops other overlay TCP traffic; an IPv4 virtual service address is translated with OUTPUT DNAT. It is not a general-purpose CNI or a multi-tenant security boundary.

Run only inside a disposable Ubuntu Linux VM. Do not run `up` on a production host. The code requires root and the kernel's veth, VXLAN and netfilter support. Namespace names `cm-site-a` and `cm-site-b` are reserved for this lab. It refuses to create a lab if they exist. `down` removes these namespaces; verify that they are yours first.

```sh
sudo apt-get update
sudo apt-get install -y iproute2 iptables iputils-ping curl
make build
bin/netlab -action plan
sudo bin/netlab -action up
sudo ip netns exec cm-site-a ping -c 3 10.44.0.2
sudo ip netns exec cm-site-a ping -6 -c 3 fd44:44::2
```

In a second terminal inside the VM:

```sh
sudo ip netns exec cm-site-b ./bin/echo -listen :8081 -id remote-site
```

In the first terminal:

```sh
sudo ip netns exec cm-site-a curl -fsS http://10.44.0.2:8081/
sudo ip netns exec cm-site-a curl -gfsS 'http://[fd44:44::2]:8081/'
sudo ip netns exec cm-site-a curl -fsS http://10.44.0.100:8081/
sudo ip netns exec cm-site-a iptables -t nat -L -n -v
sudo ip netns exec cm-site-b iptables -L -n -v
sudo ip -n cm-site-a -d link show cm-vxlan
```

To exercise the Go proxy on top of the overlay, run in another terminal:

```sh
sudo ip netns exec cm-site-a ./bin/cloudmesh -http 127.0.0.1:8080 -tcp 127.0.0.1:9000 -admin 127.0.0.1:9090 -backends '10.44.0.2:8081,[fd44:44::2]:8081' -algorithm round-robin
```

Then `sudo ip netns exec cm-site-a curl -fsS http://127.0.0.1:8080/` should reach the remote server via alternating address families. Both addresses are the same backend process; do not treat this as redundant physical infrastructure.

Stop the proxy and echo process with Ctrl-C, then `sudo bin/netlab -action down`. If setup fails, the Go command attempts cleanup of namespaces created in that run. Inspect `ip netns list` after failures.

**Verification boundary:** the generated commands and IPAM were unit-tested. Actual privileged execution has not been verified in the supplied environment. These commands are a lab validation procedure, not recorded results.
