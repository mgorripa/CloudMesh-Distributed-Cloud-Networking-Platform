# Suggested README insert

Copy the section below into the main README near the Linux networking feature. Update its verification status after running the live demo. Keep the existing AI-assistance note accurate.

## Declarative Linux Network Agent

The standalone Go network agent reconciles a two-site Linux VXLAN lab from a JSON configuration. It reuses CloudMesh's IPAM allocator, inspects actual kernel networking state through iproute2, and updates only missing or changed resources. The agent manages network namespaces, veth interfaces, IPv4/IPv6 addresses, stateful iptables/ip6tables rules and a fixed IPv4 DNAT mapping. A periodic reconciliation loop is designed to restore deleted overlay interfaces and apply changed TCP policies without restarting the process.

```bash
bash scripts/agent-demo.sh
```

Agent unit and race tests passed in the development environment (82.0% statement coverage). Live privileged networking validation is pending until the demo is run successfully. This lab is independent of the existing Kubernetes controller; BGP and real hybrid-cloud connectivity remain future work.

See [Linux network agent](docs/linux-agent.md) for the architecture, command explanations and live validation procedure.
