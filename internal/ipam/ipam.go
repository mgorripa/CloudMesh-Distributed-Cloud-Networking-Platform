// Package ipam allocates addresses from bounded IPv4 and IPv6 pools.
package ipam

import (
	"fmt"
	"net/netip"
	"sync"
)

type Pool struct {
	mu     sync.Mutex
	prefix netip.Prefix
	owners map[string]netip.Addr
	used   map[netip.Addr]string
	limit  int
}

func New(prefix string, limit int) (*Pool, error) {
	p, e := netip.ParsePrefix(prefix)
	if e != nil {
		return nil, e
	}
	if limit < 1 || limit > 65536 {
		return nil, fmt.Errorf("limit must be 1..65536")
	}
	p = p.Masked()
	return &Pool{prefix: p, owners: map[string]netip.Addr{}, used: map[netip.Addr]string{}, limit: limit}, nil
}
func (p *Pool) Allocate(owner string) (netip.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if owner == "" {
		return netip.Addr{}, fmt.Errorf("empty owner")
	}
	if a, ok := p.owners[owner]; ok {
		return a, nil
	}
	if len(p.used) >= p.limit {
		return netip.Addr{}, fmt.Errorf("pool capacity reached")
	}
	// Skip the network/subnet-router address; also skip IPv4 broadcast. Scan at most limit+1 addresses.
	a := p.prefix.Addr().Next()
	for i := 0; i <= p.limit && p.prefix.Contains(a); i++ {
		if a.Is4() && !p.prefix.Contains(a.Next()) {
			break
		}
		if _, used := p.used[a]; !used {
			p.used[a] = owner
			p.owners[owner] = a
			return a, nil
		}
		a = a.Next()
	}
	return netip.Addr{}, fmt.Errorf("address pool exhausted")
}
func (p *Pool) Release(owner string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a, ok := p.owners[owner]; ok {
		delete(p.owners, owner)
		delete(p.used, a)
	}
}
