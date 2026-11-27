// Package netlab builds an isolated dual-stack Linux VXLAN laboratory.
// It never modifies host firewall rules, routes, or default namespaces.
package netlab

import (
	"cloudmesh/internal/ipam"
	"fmt"
	"os/exec"
	"strings"
)

const A = "cm-site-a"
const B = "cm-site-b"

type Command struct {
	Program string
	Args    []string
}

func (c Command) String() string { return c.Program + " " + strings.Join(c.Args, " ") }
func (c Command) Run() error {
	out, err := exec.Command(c.Program, c.Args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", c.String(), err, out)
	}
	return nil
}
func ip(a ...string) Command           { return Command{"ip", a} }
func ns(n string, a ...string) Command { return ip(append([]string{"-n", n}, a...)...) }
func inside(n, program string, a ...string) Command {
	return ip(append([]string{"netns", "exec", n, program}, a...)...)
}
func Cleanup() []Command { return []Command{ip("netns", "del", A), ip("netns", "del", B)} }

// firewallRules permits established connections and new
// connections to the application port. All other inbound
// TCP traffic through the overlay is dropped.
func firewallRules(namespace, firewall string) []Command {
    return []Command{
        inside(namespace, firewall,
            "-A", "INPUT",
            "-i", "cm-vxlan",
            "-m", "conntrack",
            "--ctstate", "ESTABLISHED,RELATED",
            "-j", "ACCEPT"),

        inside(namespace, firewall,
            "-A", "INPUT",
            "-i", "cm-vxlan",
            "-p", "tcp",
            "--dport", "8081",
            "-j", "ACCEPT"),

        inside(namespace, firewall,
            "-A", "INPUT",
            "-i", "cm-vxlan",
            "-p", "tcp",
            "-j", "DROP"),
    }
}

func Plan() ([]Command, error) {
	v4, _ := ipam.New("10.44.0.0/24", 16)
	v6, _ := ipam.New("fd44:44::/64", 16)

	a4, e := v4.Allocate(A)
	if e != nil {
		return nil, e
	}

	b4, e := v4.Allocate(B)
	if e != nil {
		return nil, e
	}

	a6, e := v6.Allocate(A)
	if e != nil {
		return nil, e
	}

	b6, e := v6.Allocate(B)
	if e != nil {
		return nil, e
	}

	cmds := []Command{
		ip("netns", "add", A),
		ip("netns", "add", B),
		ip("link", "add", "cm-under-a", "netns", A,
			"type", "veth", "peer", "name", "cm-under-b", "netns", B),
	}

	for _, site := range []struct {
		name, dev, local, remote, v4, v6 string
	}{
		{A, "cm-under-a", "192.0.2.1", "192.0.2.2", a4.String(), a6.String()},
		{B, "cm-under-b", "192.0.2.2", "192.0.2.1", b4.String(), b6.String()},
	} {
		n := site.name

		cmds = append(cmds,
			ns(n, "link", "set", "lo", "up"),
			ns(n, "addr", "add", site.local+"/30", "dev", site.dev),
			ns(n, "link", "set", site.dev, "up"),

			ns(n, "link", "add", "cm-vxlan",
				"type", "vxlan",
				"id", "44",
				"local", site.local,
				"remote", site.remote,
				"dev", site.dev,
				"dstport", "4789"),

			ns(n, "addr", "add", site.v4+"/24", "dev", "cm-vxlan"),
			ns(n, "-6", "addr", "add", site.v6+"/64", "dev", "cm-vxlan", "nodad"),
			ns(n, "link", "set", "cm-vxlan", "up"),
		)

		// Configure stateful IPv4 and IPv6 firewall rules.
		cmds = append(cmds, firewallRules(n, "iptables")...)
		cmds = append(cmds, firewallRules(n, "ip6tables")...)
	}

	// A virtual service address in site A is translated
	// to the remote backend by OUTPUT DNAT.
	cmds = append(cmds,
		ns(A, "addr", "add", "10.44.0.100/32", "dev", "lo"),
		inside(A, "iptables",
			"-t", "nat",
			"-A", "OUTPUT",
			"-d", "10.44.0.100",
			"-p", "tcp",
			"--dport", "8081",
			"-j", "DNAT",
			"--to-destination", b4.String()+":8081"),
	)

	return cmds, nil
}
