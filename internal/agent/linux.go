package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Runner enables deterministic tests without root or a Linux kernel.
type Runner interface {
	Run(context.Context, string, string, ...string) (string, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, input, program string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", program, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type Linux struct{ Runner Runner }

func (l Linux) ip(ctx context.Context, args ...string) (string, error) {
	return l.Runner.Run(ctx, "", "ip", args...)
}
func (l Linux) ns(ctx context.Context, n string, args ...string) (string, error) {
	return l.ip(ctx, append([]string{"-n", n}, args...)...)
}
func (l Linux) inside(ctx context.Context, n, input, program string, args ...string) (string, error) {
	return l.Runner.Run(ctx, input, "ip", append([]string{"netns", "exec", n, program}, args...)...)
}
func changed(err error) (int, error) {
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (l Linux) EnsureNamespace(ctx context.Context, n string) (int, error) {
	out, err := l.ip(ctx, "netns", "list")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == n {
			return 0, nil
		}
	}
	_, err = l.ip(ctx, "netns", "add", n)
	return changed(err)
}

type link struct {
	Name  string   `json:"ifname"`
	Index int      `json:"ifindex"`
	Peer  int      `json:"link_index"`
	Flags []string `json:"flags"`
	Info  struct {
		Kind string `json:"info_kind"`
		Data struct {
			ID     int    `json:"id"`
			Local  string `json:"local"`
			Remote string `json:"remote"`
			Port   int    `json:"port"`
			Device string `json:"link"`
		} `json:"info_data"`
	} `json:"linkinfo"`
}

func (l Linux) links(ctx context.Context, n string) (map[string]link, error) {
	out, err := l.ns(ctx, n, "-j", "-d", "link", "show")
	if err != nil {
		return nil, err
	}
	var list []link
	if err = json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("decode links: %w", err)
	}
	m := map[string]link{}
	for _, v := range list {
		m[v.Name] = v
	}
	return m, nil
}
func (l Linux) EnsureVeth(ctx context.Context, a, ad, b, bd string) (int, error) {
	al, err := l.links(ctx, a)
	if err != nil {
		return 0, err
	}
	bl, err := l.links(ctx, b)
	if err != nil {
		return 0, err
	}
	x, ax := al[ad]
	y, by := bl[bd]
	if ax && x.Info.Kind != "veth" || by && y.Info.Kind != "veth" {
		return 0, fmt.Errorf("refusing to replace non-veth underlay interface")
	}
	if ax && by {
		if x.Peer != 0 && x.Peer != y.Index || y.Peer != 0 && y.Peer != x.Index {
			return 0, fmt.Errorf("underlay interfaces are not the expected peer pair")
		}
		return 0, nil
	}
	n := 0
	if ax {
		_, err = l.ns(ctx, a, "link", "del", ad)
		if err != nil {
			return n, err
		}
		n++
	}
	if by {
		_, err = l.ns(ctx, b, "link", "del", bd)
		if err != nil {
			return n, err
		}
		n++
	}
	_, err = l.ip(ctx, "link", "add", ad, "netns", a, "type", "veth", "peer", "name", bd, "netns", b)
	c, err := changed(err)
	return n + c, err
}
func (l Linux) EnsureVXLAN(ctx context.Context, n, dev, local, remote string, vni int) (int, error) {
	links, err := l.links(ctx, n)
	if err != nil {
		return 0, err
	}
	count := 0
	if x, ok := links[Overlay]; ok {
		if x.Info.Kind != "vxlan" {
			return 0, fmt.Errorf("refusing to replace non-VXLAN interface %s", Overlay)
		}
		d := x.Info.Data
		if d.ID == vni && d.Local == local && d.Remote == remote && d.Port == 4789 && d.Device == dev {
			return 0, nil
		}
		_, err = l.ns(ctx, n, "link", "del", Overlay)
		if err != nil {
			return 0, err
		}
		count++
	}
	_, err = l.ns(ctx, n, "link", "add", Overlay, "type", "vxlan", "id", strconv.Itoa(vni), "local", local, "remote", remote, "dev", dev, "dstport", "4789")
	c, err := changed(err)
	return count + c, err
}
func (l Linux) EnsureUp(ctx context.Context, n, dev string) (int, error) {
	links, err := l.links(ctx, n)
	if err != nil {
		return 0, err
	}
	x, ok := links[dev]
	if !ok {
		return 0, fmt.Errorf("interface %s missing", dev)
	}
	if slices.Contains(x.Flags, "UP") {
		return 0, nil
	}
	_, err = l.ns(ctx, n, "link", "set", dev, "up")
	return changed(err)
}
func (l Linux) EnsureAddress(ctx context.Context, n, dev, prefix string) (int, error) {
	want, err := netip.ParsePrefix(prefix)
	if err != nil {
		return 0, err
	}
	out, err := l.ns(ctx, n, "-j", "addr", "show", "dev", dev)
	if err != nil {
		return 0, err
	}
	var v []struct {
		Info []struct {
			Local string `json:"local"`
			Bits  int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err = json.Unmarshal([]byte(out), &v); err != nil {
		return 0, err
	}
	for _, x := range v {
		for _, a := range x.Info {
			ip, e := netip.ParseAddr(a.Local)
			if e == nil && ip == want.Addr() && a.Bits == want.Bits() {
				return 0, nil
			}
		}
	}
	args := []string{"addr", "replace", prefix, "dev", dev}
	if want.Addr().Is6() {
		args = append([]string{"-6"}, args...)
		args = append(args, "nodad")
	}
	_, err = l.ns(ctx, n, args...)
	return changed(err)
}

// normalize handles formatting added by iptables -S (implicit TCP match,
// state ordering and host prefix notation). Chain names and arguments are
// generated internally; no arbitrary shell or restore text is accepted.
func normalize(in []string) string {
	out := []string{}
	for i := 0; i < len(in); i++ {
		s := in[i]
		if s == "-m" && i+1 < len(in) && in[i+1] == "tcp" {
			i++
			continue
		}
		if i > 0 && in[i-1] == "--ctstate" {
			v := strings.Split(s, ",")
			slices.Sort(v)
			s = strings.Join(v, ",")
		}
		if i > 0 && (in[i-1] == "-d" || in[i-1] == "-s") {
			if p, e := netip.ParsePrefix(s); e == nil {
				s = p.String()
			} else if a, e := netip.ParseAddr(s); e == nil {
				s = netip.PrefixFrom(a, a.BitLen()).String()
			}
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}
func rulesIn(out, chain string) [][]string {
	var rules [][]string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "-A" && f[1] == chain {
			rules = append(rules, f[2:])
		}
	}
	return rules
}
func equalRules(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if normalize(a[i]) != normalize(b[i]) {
			return false
		}
	}
	return true
}
func chainExists(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "-N" && f[1] == name {
			return true
		}
	}
	return false
}
func (l Linux) EnsureChain(ctx context.Context, c Chain) (int, error) {
	out, err := l.inside(ctx, c.Namespace, "", c.Program, "-w", "5", "-t", c.Table, "-S")
	if err != nil {
		return 0, err
	}
	n := 0
	// Replace only our chain in a single restore transaction, not the entire table.
	if !chainExists(out, c.Name) || !equalRules(rulesIn(out, c.Name), c.Rules) {
		var b strings.Builder
		fmt.Fprintf(&b, "*%s\n:%s - [0:0]\n-F %s\n", c.Table, c.Name, c.Name)
		for _, r := range c.Rules {
			fmt.Fprintf(&b, "-A %s %s\n", c.Name, strings.Join(r, " "))
		}
		b.WriteString("COMMIT\n")
		_, err = l.inside(ctx, c.Namespace, b.String(), c.Program+"-restore", "--noflush", "--wait", "5")
		if err != nil {
			return n, err
		}
		n++
	}
	hook := append(append([]string{}, c.Match...), "-j", c.Name)
	parent := rulesIn(out, c.Parent)
	matches := 0
	for _, r := range parent {
		if normalize(r) == normalize(hook) {
			matches++
		}
	}
	if matches == 1 && len(parent) > 0 && normalize(parent[0]) == normalize(hook) {
		return n, nil
	}
	// Keep exactly one owned hook at the top. Never flush the parent chain.
	// Remove duplicate hooks before insertion; lab traffic may be affected
	// briefly during this repair, unlike the atomic owned-chain replacement.
	for i := 0; i < matches; i++ {
		args := append([]string{"-w", "5", "-t", c.Table, "-D", c.Parent}, hook...)
		if _, err = l.inside(ctx, c.Namespace, "", c.Program, args...); err != nil {
			return n, err
		}
		n++
	}
	args := append([]string{"-w", "5", "-t", c.Table, "-I", c.Parent, "1"}, hook...)
	_, err = l.inside(ctx, c.Namespace, "", c.Program, args...)
	m, err := changed(err)
	return n + m, err
}
