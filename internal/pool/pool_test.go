package pool

import (
	"sync"
	"testing"
)

func TestSchedulingAndReconciliation(t *testing.T) {
	p := &Pool{}
	if err := p.Replace([]string{"127.0.0.1:2", "127.0.0.1:1", "127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	a, release, e := p.Acquire("round-robin", "")
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	b, releaseB, e := p.Acquire("least-connections", "")
	if e != nil || a == b {
		t.Fatalf("expected idle backend: %v", e)
	}
	releaseB()
	if err := p.Replace([]string{a.Address, "[::1]:3"}); err != nil {
		t.Fatal(err)
	}
	if a.Active() != 1 {
		t.Fatal("active reservation lost")
	}
	for _, b := range p.Snapshot() {
		if b.Address != a.Address {
			b.SetHealthy(false)
		}
	}
	b, rel, e := p.Acquire("round-robin", "")
	if e != nil || b != a {
		t.Fatal("unhealthy backend selected")
	}
	rel()
	release()
	release()
	if a.Active() != 0 {
		t.Fatal("release not idempotent")
	}
}
func TestHashStableAndConcurrent(t *testing.T) {
	p := &Pool{}
	_ = p.Replace([]string{"localhost:1", "localhost:2", "localhost:3"})
	a, r, _ := p.Acquire("hash", "client")
	r()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, r, e := p.Acquire("hash", "client")
			if e != nil || a != b {
				t.Error("hash not stable")
				return
			}
			r()
		}()
	}
	wg.Wait()
	if a.Active() != 0 {
		t.Fatal("leaked reservation")
	}
	_ = p.Replace(nil)
	if _, _, e := p.Acquire("hash", "client"); e == nil {
		t.Fatal("expected empty error")
	}
}
func TestHashRemoval(t *testing.T) {
	p := &Pool{}
	_ = p.Replace([]string{"localhost:1", "localhost:2", "localhost:3"})
	before := map[string]string{}
	for i := 0; i < 200; i++ {
		k := string(rune(i + 32))
		b, r, _ := p.Acquire("hash", k)
		before[k] = b.Address
		r()
	}
	_ = p.Replace([]string{"localhost:1", "localhost:2"})
	for k, old := range before {
		b, r, _ := p.Acquire("hash", k)
		if old != "localhost:3" && b.Address != old {
			t.Fatal("unnecessary remap")
		}
		r()
	}
}
