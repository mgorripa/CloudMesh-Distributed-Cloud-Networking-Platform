# BGP and hybrid connectivity extension (not implemented)

The Linux VXLAN experiment connects two simulated sites over a point-to-point underlay. It does not implement BGP. A follow-on implementation would run FRRouting peers at each site, advertise site-specific prefixes, verify route installation and withdrawal, and connect the Kubernetes test network to the simulated external network. Measure route convergence, packet loss and recovery after a link failure. A second actual VM can test pod-to-VM connectivity. Actual GCP connectivity requires a real GCP VPC/VM and a secure tunnel or managed VPN, and may incur charges.

Do not describe these features as completed until they are implemented and tested. The existing Go controller discovers EndpointSlices; it does not control BGP or program kernel routes on Kubernetes nodes.
