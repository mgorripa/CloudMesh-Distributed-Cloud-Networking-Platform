// Package pool owns the backend snapshot shared by the controller and proxies.
package pool

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

type Backend struct {
	Address string
	active  atomic.Int64
	healthy atomic.Bool
}

func (b *Backend) Active() int64     { return b.active.Load() }
func (b *Backend) Healthy() bool     { return b.healthy.Load() }
func (b *Backend) SetHealthy(v bool) { b.healthy.Store(v) }

type Pool struct {
	mu       sync.RWMutex
	backends []*Backend
	next     atomic.Uint64
}

// Replace preserves live counters and health for endpoints that survive a reconciliation.
func (p *Pool) Replace(addresses []string) error {
	unique := map[string]bool{}
	for _, a := range addresses {
		h, port, err := net.SplitHostPort(a)
		n, portErr := strconv.Atoi(port)
		if err != nil || h == "" || portErr != nil || n < 1 || n > 65535 {
			return errors.New("invalid backend: " + a)
		}
		unique[a] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := map[string]*Backend{}
	for _, b := range p.backends {
		old[b.Address] = b
	}
	keys := make([]string, 0, len(unique))
	for a := range unique {
		keys = append(keys, a)
	}
	sort.Strings(keys)
	next := make([]*Backend, 0, len(keys))
	for _, a := range keys {
		b := old[a]
		if b == nil {
			b = &Backend{Address: a}
			b.healthy.Store(true)
		}
		next = append(next, b)
	}
	p.backends = next
	return nil
}
func (p *Pool) Snapshot() []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]*Backend(nil), p.backends...)
}
func (p *Pool) Ready() int {
	n := 0
	for _, b := range p.Snapshot() {
		if b.Healthy() {
			n++
		}
	}
	return n
}

// Acquire reserves a backend until release is called. The caller must release exactly once.
// Hash uses rendezvous hashing rather than a ring: removing a backend only remaps its keys.
func (p *Pool) Acquire(algorithm, key string) (*Backend, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	candidates := make([]*Backend, 0, len(p.backends))
	for _, b := range p.backends {
		if b.Healthy() {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) == 0 {
		return nil, nil, errors.New("no healthy backend")
	}
	var chosen *Backend
	switch algorithm {
	case "least-connections":
		start := int((p.next.Add(1) - 1) % uint64(len(candidates)))
		chosen = candidates[start]
		for i := 1; i < len(candidates); i++ {
			b := candidates[(start+i)%len(candidates)]
			if b.Active() < chosen.Active() {
				chosen = b
			}
		}
	case "hash":
		var best uint64
		for _, b := range candidates {
			h := sha256.Sum256([]byte(key + "\x00" + b.Address))
			score := binary.BigEndian.Uint64(h[:8])
			if chosen == nil || score > best {
				chosen = b
				best = score
			}
		}
	case "round-robin":
		chosen = candidates[(p.next.Add(1)-1)%uint64(len(candidates))]
	default:
		return nil, nil, errors.New("unknown scheduling algorithm")
	}
	chosen.active.Add(1)
	var once sync.Once
	return chosen, func() { once.Do(func() { chosen.active.Add(-1) }) }, nil
}
