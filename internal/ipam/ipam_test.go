package ipam

import (
	"fmt"
	"sync"
	"testing"
)

func TestAllocateRelease(t *testing.T) {
	p, _ := New("10.0.0.0/30", 10)
	a, e := p.Allocate("a")
	if e != nil || a.String() != "10.0.0.1" {
		t.Fatal(a, e)
	}
	again, _ := p.Allocate("a")
	if a != again {
		t.Fatal("not idempotent")
	}
	b, _ := p.Allocate("b")
	if b.String() != "10.0.0.2" {
		t.Fatal(b)
	}
	if _, e := p.Allocate("c"); e == nil {
		t.Fatal("broadcast allocated")
	}
	p.Release("a")
	c, _ := p.Allocate("c")
	if c != a {
		t.Fatal("address not reused")
	}
}
func TestIPv6Concurrent(t *testing.T) {
	p, _ := New("fd00::/64", 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, e := p.Allocate(fmt.Sprint(i)); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	if len(p.used) != 32 {
		t.Fatal("duplicate allocation")
	}
	if _, e := p.Allocate("extra"); e == nil {
		t.Fatal("limit ignored")
	}
}
