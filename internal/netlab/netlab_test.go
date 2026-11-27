package netlab

import (
	"strings"
	"testing"
)

func TestPlan(t *testing.T) {
	p, e := Plan()
	if e != nil {
		t.Fatal(e)
	}
	var s []string
	for _, c := range p {
		s = append(s, c.String())
		if strings.Contains(c.String(), "iptables") && !strings.Contains(c.String(), "netns exec cm-site-") {
			t.Fatal("host firewall touched")
		}
	}
	all := strings.Join(s, "\n")
	for _, x := range []string{"type vxlan", "fd44:44::1/64", "fd44:44::2/64", "DNAT", "--to-destination 10.44.0.2:8081"} {
		if !strings.Contains(all, x) {
			t.Fatal("missing " + x)
		}
	}
}
func TestFirewallRuleOrder(t *testing.T) {
	for _, firewall := range []string{
		"iptables", "ip6tables",
	} {
		t.Run(firewall, func(t *testing.T) {
			rules := firewallRules(A, firewall)

			if len(rules) != 3 {
				t.Fatalf("expected 3 rules, got %d", len(rules))
			}

			if !strings.Contains(
				rules[0].String(), "ESTABLISHED,RELATED",
			) {
				t.Fatal("established traffic must be allowed first")
			}

			if !strings.Contains(
				rules[1].String(), "--dport 8081",
			) {
				t.Fatal("application port must be allowed")
			}

			if !strings.HasSuffix(
				rules[2].String(), "-j DROP",
			) {
				t.Fatal("drop rule must come last")
			}
		})
	}
}
