// Package agent reconciles a deliberately isolated, two-site Linux networking lab.
package agent

import (
	"cloudmesh/internal/ipam"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
)

const (
	SiteA       = "cma-site-a"
	SiteB       = "cma-site-b"
	Overlay     = "cma-vxlan"
	UnderA      = "cma-under-a"
	UnderB      = "cma-under-b"
	VIP         = "10.44.0.100"
	ServicePort = 8081
)

// Spec is intentionally small: a VNI and the TCP destination port allowed
// through the overlay. Addresses are derived deterministically from IPAM.
type Spec struct {
	VNI            int `json:"vni"`
	AllowedTCPPort int `json:"allowedTCPPort"`
}

func (s Spec) Validate() error {
	if s.VNI < 1 || s.VNI > 16777215 {
		return fmt.Errorf("vni must be between 1 and 16777215")
	}
	if s.AllowedTCPPort < 1 || s.AllowedTCPPort > 65535 {
		return fmt.Errorf("allowedTCPPort must be between 1 and 65535")
	}
	return nil
}

func LoadSpec(path string) (Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return Spec{}, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	var s Spec
	if err := d.Decode(&s); err != nil {
		return s, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return s, fmt.Errorf("configuration must contain exactly one JSON object")
	}
	return s, s.Validate()
}

// Kernel is the narrow boundary between reconciliation logic and Linux.
// The integer result counts mutations, including any completed before an error.
type Kernel interface {
	EnsureNamespace(context.Context, string) (int, error)
	EnsureVeth(context.Context, string, string, string, string) (int, error)
	EnsureVXLAN(context.Context, string, string, string, string, int) (int, error)
	EnsureUp(context.Context, string, string) (int, error)
	EnsureAddress(context.Context, string, string, string) (int, error)
	EnsureChain(context.Context, Chain) (int, error)
}

type Chain struct {
	Namespace, Program, Table, Parent, Name string
	Match                                   []string
	Rules                                   [][]string
}

// Reconcile converges desired state in dependency order. Operations are
// individually idempotent; a later pass retries after a partial failure.
// Only one caller should reconcile a given lab at a time.
func Reconcile(ctx context.Context, k Kernel, s Spec) (int, error) {
	if err := s.Validate(); err != nil {
		return 0, err
	}
	p4, err := ipam.New("10.44.0.0/24", 16)
	if err != nil {
		return 0, err
	}
	p6, err := ipam.New("fd44:44::/64", 16)
	if err != nil {
		return 0, err
	}
	type site struct{ ns, dev, local, remote, v4, v6 string }
	sites := []site{{ns: SiteA, dev: UnderA, local: "192.0.2.1", remote: "192.0.2.2"}, {ns: SiteB, dev: UnderB, local: "192.0.2.2", remote: "192.0.2.1"}}
	for i := range sites {
		a, err := p4.Allocate(sites[i].ns)
		if err != nil {
			return 0, err
		}
		b, err := p6.Allocate(sites[i].ns)
		if err != nil {
			return 0, err
		}
		sites[i].v4 = a.String()
		sites[i].v6 = b.String()
	}
	changes := 0
	step := func(label string, f func() (int, error)) error {
		n, err := f()
		changes += n
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		return nil
	}
	for _, x := range sites {
		if err := step("namespace "+x.ns, func() (int, error) { return k.EnsureNamespace(ctx, x.ns) }); err != nil {
			return changes, err
		}
	}
	if err := step("underlay veth", func() (int, error) { return k.EnsureVeth(ctx, SiteA, UnderA, SiteB, UnderB) }); err != nil {
		return changes, err
	}
	for _, x := range sites {
		for _, dev := range []string{"lo", x.dev} {
			if err := step(x.ns+" link "+dev, func() (int, error) { return k.EnsureUp(ctx, x.ns, dev) }); err != nil {
				return changes, err
			}
		}
		if err := step(x.ns+" underlay address", func() (int, error) { return k.EnsureAddress(ctx, x.ns, x.dev, x.local+"/30") }); err != nil {
			return changes, err
		}
		if err := step(x.ns+" overlay", func() (int, error) { return k.EnsureVXLAN(ctx, x.ns, x.dev, x.local, x.remote, s.VNI) }); err != nil {
			return changes, err
		}
		for _, prefix := range []string{x.v4 + "/24", x.v6 + "/64"} {
			if err := step(x.ns+" address "+prefix, func() (int, error) { return k.EnsureAddress(ctx, x.ns, Overlay, prefix) }); err != nil {
				return changes, err
			}
		}
		if err := step(x.ns+" overlay link up", func() (int, error) { return k.EnsureUp(ctx, x.ns, Overlay) }); err != nil {
			return changes, err
		}
		for _, program := range []string{"iptables", "ip6tables"} {
			c := Chain{Namespace: x.ns, Program: program, Table: "filter", Parent: "INPUT", Name: "CMA-FILTER", Match: []string{"-i", Overlay, "-p", "tcp"}, Rules: [][]string{
				{"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
				{"-p", "tcp", "--dport", strconv.Itoa(s.AllowedTCPPort), "-j", "ACCEPT"},
				{"-j", "DROP"},
			}}
			if err := step(x.ns+" "+program, func() (int, error) { return k.EnsureChain(ctx, c) }); err != nil {
				return changes, err
			}
		}
	}
	if err := step("virtual service address", func() (int, error) { return k.EnsureAddress(ctx, SiteA, "lo", VIP+"/32") }); err != nil {
		return changes, err
	}
	c := Chain{Namespace: SiteA, Program: "iptables", Table: "nat", Parent: "OUTPUT", Name: "CMA-DNAT", Match: []string{"-d", VIP + "/32", "-p", "tcp", "--dport", strconv.Itoa(ServicePort)}, Rules: [][]string{{"-p", "tcp", "-j", "DNAT", "--to-destination", sites[1].v4 + ":" + strconv.Itoa(ServicePort)}}}
	if err := step("virtual service DNAT", func() (int, error) { return k.EnsureChain(ctx, c) }); err != nil {
		return changes, err
	}
	return changes, nil
}
