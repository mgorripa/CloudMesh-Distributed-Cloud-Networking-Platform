package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The in-memory kernel records desired values, allowing lifecycle tests on macOS.
type fakeKernel struct {
	state map[string]string
	fail  string
	calls []string
}

func (f *fakeKernel) ensure(key, value string) (int, error) {
	f.calls = append(f.calls, key)
	if f.fail == key {
		return 0, fmt.Errorf("injected failure")
	}
	if f.state[key] == value {
		return 0, nil
	}
	f.state[key] = value
	return 1, nil
}
func (f *fakeKernel) EnsureNamespace(_ context.Context, n string) (int, error) {
	return f.ensure("ns/"+n, "exists")
}
func (f *fakeKernel) EnsureVeth(_ context.Context, a, ad, b, bd string) (int, error) {
	return f.ensure("veth", strings.Join([]string{a, ad, b, bd}, "/"))
}
func (f *fakeKernel) EnsureVXLAN(_ context.Context, n, d, l, r string, v int) (int, error) {
	return f.ensure("vxlan/"+n, fmt.Sprintf("%s/%s/%s/%d", d, l, r, v))
}
func (f *fakeKernel) EnsureUp(_ context.Context, n, d string) (int, error) {
	return f.ensure("up/"+n+"/"+d, "up")
}
func (f *fakeKernel) EnsureAddress(_ context.Context, n, d, p string) (int, error) {
	return f.ensure("addr/"+n+"/"+d+"/"+p, p)
}
func (f *fakeKernel) EnsureChain(_ context.Context, c Chain) (int, error) {
	return f.ensure("chain/"+c.Namespace+"/"+c.Program+"/"+c.Name, fmt.Sprint(c.Rules))
}
func TestReconcileLifecycle(t *testing.T) {
	ctx := context.Background()
	f := &fakeKernel{state: map[string]string{}}
	s := Spec{44, 8081}
	n, e := Reconcile(ctx, f, s)
	if e != nil || n == 0 {
		t.Fatalf("first pass: %d %v", n, e)
	}
	if f.state["addr/"+SiteA+"/"+Overlay+"/10.44.0.1/24"] == "" || f.state["addr/"+SiteB+"/"+Overlay+"/fd44:44::2/64"] == "" {
		t.Fatal("missing dual-stack address")
	}
	if !strings.Contains(
		f.state["chain/"+SiteA+"/iptables/CMA-DNAT"],
		"[-p tcp -j DNAT --to-destination 10.44.0.2:8081]",
	) {
		t.Fatal("missing TCP protocol or incorrect DNAT destination")
	}
	n, e = Reconcile(ctx, f, s)
	if e != nil || n != 0 {
		t.Fatalf("second pass: %d %v", n, e)
	}
	delete(f.state, "vxlan/"+SiteA)
	n, e = Reconcile(ctx, f, s)
	if e != nil || n != 1 {
		t.Fatalf("repair: %d %v", n, e)
	}
	s.AllowedTCPPort = 8082
	n, e = Reconcile(ctx, f, s)
	if e != nil || n != 4 {
		t.Fatalf("policy change: %d %v", n, e)
	}
	if !strings.Contains(f.state["chain/"+SiteB+"/ip6tables/CMA-FILTER"], "8082") {
		t.Fatal("IPv6 policy not updated")
	}
	s.VNI = 45
	n, e = Reconcile(ctx, f, s)
	if e != nil || n != 2 {
		t.Fatalf("VNI update: %d %v", n, e)
	}
}
func TestPartialFailureAndRetry(t *testing.T) {
	f := &fakeKernel{state: map[string]string{}, fail: "vxlan/" + SiteB}
	s := Spec{44, 8081}
	n, e := Reconcile(context.Background(), f, s)
	if e == nil || n == 0 || !strings.Contains(e.Error(), SiteB) {
		t.Fatalf("expected partial failure: %d %v", n, e)
	}
	f.fail = ""
	n, e = Reconcile(context.Background(), f, s)
	if e != nil || n == 0 {
		t.Fatalf("retry: %d %v", n, e)
	}
	n, e = Reconcile(context.Background(), f, s)
	if e != nil || n != 0 {
		t.Fatalf("convergence: %d %v", n, e)
	}
}
func TestInvalidSpec(t *testing.T) {
	for _, s := range []Spec{{0, 8081}, {16777216, 8081}, {44, 0}, {44, 65536}} {
		f := &fakeKernel{state: map[string]string{}}
		if n, e := Reconcile(context.Background(), f, s); e == nil || n != 0 || len(f.calls) != 0 {
			t.Fatalf("invalid input mutated state: %+v", s)
		}
	}
}
func TestLoadSpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.json")
	for _, x := range []struct {
		data  string
		valid bool
	}{{`{"vni":44,"allowedTCPPort":8081}`, true}, {`{"vni":44,"allowedTCPPort":8081,"oops":1}`, false}, {`{"vni":44,"allowedTCPPort":8081} {}`, false}, {`{"vni":44}`, false}} {
		if e := os.WriteFile(path, []byte(x.data), 0600); e != nil {
			t.Fatal(e)
		}
		_, e := LoadSpec(path)
		if (e == nil) != x.valid {
			t.Fatalf("%s: %v", x.data, e)
		}
	}
}

type command struct {
	args, input, out string
	err              error
}
type scriptedRunner struct {
	t        *testing.T
	commands []command
}

func (r *scriptedRunner) Run(_ context.Context, in, program string, args ...string) (string, error) {
	r.t.Helper()
	if len(r.commands) == 0 {
		r.t.Fatalf("unexpected command %s %v", program, args)
	}
	c := r.commands[0]
	r.commands = r.commands[1:]
	actual := program + " " + strings.Join(args, " ")
	if actual != c.args || in != c.input {
		r.t.Fatalf("command got %q input %q; want %q input %q", actual, in, c.args, c.input)
	}
	return c.out, c.err
}
func (r *scriptedRunner) done() {
	r.t.Helper()
	if len(r.commands) != 0 {
		r.t.Fatalf("%d expected commands not executed", len(r.commands))
	}
}
func TestVXLANInspection(t *testing.T) {
	const cmd = "ip -n cma-site-a -j -d link show"
	const current = `[{"ifname":"cma-vxlan","linkinfo":{"info_kind":"vxlan","info_data":{"id":44,"local":"192.0.2.1","remote":"192.0.2.2","port":4789,"link":"cma-under-a"}}}]`
	r := &scriptedRunner{t: t, commands: []command{{args: cmd, out: current}}}
	l := Linux{r}
	n, e := l.EnsureVXLAN(context.Background(), SiteA, UnderA, "192.0.2.1", "192.0.2.2", 44)
	if n != 0 || e != nil {
		t.Fatal(n, e)
	}
	r.done()
	r.commands = []command{{args: cmd, out: current}, {args: "ip -n cma-site-a link del cma-vxlan"}, {args: "ip -n cma-site-a link add cma-vxlan type vxlan id 45 local 192.0.2.1 remote 192.0.2.2 dev cma-under-a dstport 4789"}}
	n, e = l.EnsureVXLAN(context.Background(), SiteA, UnderA, "192.0.2.1", "192.0.2.2", 45)
	if n != 2 || e != nil {
		t.Fatal(n, e)
	}
	r.done()
	r.commands = []command{{args: cmd, out: `[{"ifname":"cma-vxlan","linkinfo":{"info_kind":"dummy"}}}]`}}
	if n, e = l.EnsureVXLAN(context.Background(), SiteA, UnderA, "192.0.2.1", "192.0.2.2", 44); e == nil || n != 0 {
		t.Fatal("wrong interface type accepted")
	}
	r.done()
}
func TestAddressInspection(t *testing.T) {
	r := &scriptedRunner{t: t, commands: []command{{args: "ip -n cma-site-a -j addr show dev cma-vxlan", out: `[{"addr_info":[{"local":"fd44:44:0:0:0:0:0:1","prefixlen":64}]}]`}}}
	n, e := (Linux{r}).EnsureAddress(context.Background(), SiteA, Overlay, "fd44:44::1/64")
	if n != 0 || e != nil {
		t.Fatal(n, e)
	}
	r.done()
}
func TestFirewallNoOpAndRepair(t *testing.T) {
	c := Chain{Namespace: SiteA, Program: "iptables", Table: "filter", Parent: "INPUT", Name: "CMA-FILTER", Match: []string{"-i", Overlay, "-p", "tcp"}, Rules: [][]string{{"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}, {"-p", "tcp", "--dport", "8081", "-j", "ACCEPT"}, {"-j", "DROP"}}}
	const inspect = "ip netns exec cma-site-a iptables -w 5 -t filter -S"
	const valid = "-P INPUT ACCEPT\n-N CMA-FILTER\n-A INPUT -i cma-vxlan -p tcp -j CMA-FILTER\n-A CMA-FILTER -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT\n-A CMA-FILTER -p tcp -m tcp --dport 8081 -j ACCEPT\n-A CMA-FILTER -j DROP\n"
	r := &scriptedRunner{t: t, commands: []command{{args: inspect, out: valid}}}
	l := Linux{r}
	n, e := l.EnsureChain(context.Background(), c)
	if n != 0 || e != nil {
		t.Fatal(n, e)
	}
	r.done()
	r.commands = []command{{args: inspect, out: "-P INPUT ACCEPT\n"}, {args: "ip netns exec cma-site-a iptables-restore --noflush --wait 5", input: "*filter\n:CMA-FILTER - [0:0]\n-F CMA-FILTER\n-A CMA-FILTER -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n-A CMA-FILTER -p tcp --dport 8081 -j ACCEPT\n-A CMA-FILTER -j DROP\nCOMMIT\n"}, {args: "ip netns exec cma-site-a iptables -w 5 -t filter -I INPUT 1 -i cma-vxlan -p tcp -j CMA-FILTER"}}
	n, e = l.EnsureChain(context.Background(), c)
	if n != 2 || e != nil {
		t.Fatal(n, e)
	}
	r.done()
	r.commands = []command{{args: inspect, out: valid + "-A INPUT -i cma-vxlan -p tcp -j CMA-FILTER\n"}, {args: "ip netns exec cma-site-a iptables -w 5 -t filter -D INPUT -i cma-vxlan -p tcp -j CMA-FILTER"}, {args: "ip netns exec cma-site-a iptables -w 5 -t filter -D INPUT -i cma-vxlan -p tcp -j CMA-FILTER"}, {args: "ip netns exec cma-site-a iptables -w 5 -t filter -I INPUT 1 -i cma-vxlan -p tcp -j CMA-FILTER"}}
	n, e = l.EnsureChain(context.Background(), c)
	if n != 3 || e != nil {
		t.Fatal(n, e)
	}
	r.done()
}
func TestInspectionErrorDoesNotMutate(t *testing.T) {
	r := &scriptedRunner{t: t, commands: []command{{args: "ip netns list", err: fmt.Errorf("permission denied")}}}
	n, e := (Linux{r}).EnsureNamespace(context.Background(), SiteA)
	if n != 0 || e == nil {
		t.Fatal(n, e)
	}
	r.done()
}

func TestNamespaceAndLinkState(t *testing.T) {
	r := &scriptedRunner{t: t, commands: []command{
		{args: "ip netns list", out: "cma-site-a (id: 0)\n"},
		{args: "ip netns list", out: "cma-site-a (id: 0)\n"},
		{args: "ip netns add cma-site-b"},
		{args: "ip -n cma-site-a -j -d link show", out: `[{"ifname":"lo","flags":["UP","LOOPBACK"]}]`},
		{args: "ip -n cma-site-a -j -d link show", out: `[{"ifname":"cma-vxlan","flags":["BROADCAST"]}]`},
		{args: "ip -n cma-site-a link set cma-vxlan up"},
		{args: "ip -n cma-site-a -j addr show dev cma-vxlan", out: `[{"addr_info":[]}]`},
		{args: "ip -n cma-site-a -6 addr replace fd44:44::1/64 dev cma-vxlan nodad"},
	}}
	l := Linux{r}
	ctx := context.Background()
	for i, f := range []func() (int, error){
		func() (int, error) { return l.EnsureNamespace(ctx, SiteA) },
		func() (int, error) { return l.EnsureNamespace(ctx, SiteB) },
		func() (int, error) { return l.EnsureUp(ctx, SiteA, "lo") },
		func() (int, error) { return l.EnsureUp(ctx, SiteA, Overlay) },
		func() (int, error) { return l.EnsureAddress(ctx, SiteA, Overlay, "fd44:44::1/64") },
	} {
		n, e := f()
		want := []int{0, 1, 0, 1, 1}[i]
		if n != want || e != nil {
			t.Fatalf("step %d: got %d %v", i, n, e)
		}
	}
	r.done()
}
func TestVethCreateAndPeerValidation(t *testing.T) {
	const a = "ip -n cma-site-a -j -d link show"
	const b = "ip -n cma-site-b -j -d link show"
	const create = "ip link add cma-under-a netns cma-site-a type veth peer name cma-under-b netns cma-site-b"
	const la = `[{"ifname":"cma-under-a","ifindex":9,"link_index":10,"linkinfo":{"info_kind":"veth"}}]`
	const lb = `[{"ifname":"cma-under-b","ifindex":10,"link_index":9,"linkinfo":{"info_kind":"veth"}}]`
	cases := []struct {
		name     string
		commands []command
		want     int
		bad      bool
	}{
		{"create", []command{{args: a, out: "[]"}, {args: b, out: "[]"}, {args: create}}, 1, false},
		{"no-op", []command{{args: a, out: la}, {args: b, out: lb}}, 0, false},
		{"repair", []command{{args: a, out: la}, {args: b, out: "[]"}, {args: "ip -n cma-site-a link del cma-under-a"}, {args: create}}, 2, false},
		{"wrong peer", []command{{args: a, out: la}, {args: b, out: strings.Replace(lb, `"ifindex":10`, `"ifindex":11`, 1)}}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &scriptedRunner{t: t, commands: c.commands}
			n, e := (Linux{r}).EnsureVeth(context.Background(), SiteA, UnderA, SiteB, UnderB)
			if n != c.want || (e != nil) != c.bad {
				t.Fatal(n, e)
			}
			r.done()
		})
	}
}
func TestNATCanonicalRules(t *testing.T) {
	c := Chain{Namespace: SiteA, Program: "iptables", Table: "nat", Parent: "OUTPUT", Name: "CMA-DNAT", Match: []string{"-d", "10.44.0.100/32", "-p", "tcp", "--dport", "8081"}, Rules: [][]string{{"-p", "tcp", "-j", "DNAT", "--to-destination", "10.44.0.2:8081"}}}
	r := &scriptedRunner{t: t, commands: []command{{args: "ip netns exec cma-site-a iptables -w 5 -t nat -S", out: "-P OUTPUT ACCEPT\n-N CMA-DNAT\n-A OUTPUT -d 10.44.0.100/32 -p tcp -m tcp --dport 8081 -j CMA-DNAT\n-A CMA-DNAT -p tcp -j DNAT --to-destination 10.44.0.2:8081\n"}}}
	n, e := (Linux{r}).EnsureChain(context.Background(), c)
	if n != 0 || e != nil {
		t.Fatal(n, e)
	}
	r.done()
}
