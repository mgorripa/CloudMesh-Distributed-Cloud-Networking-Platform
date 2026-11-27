package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sample() Network {
	n := Network{Metadata: Metadata{Name: "application-network", Namespace: "cloudmesh", UID: "uid-1", ResourceVersion: "10", Generation: 1}}
	n.Spec.ServiceRef.Name = "echo"
	n.Spec.ServiceRef.PortName = "http"
	n.Spec.LoadBalancing.Algorithm = "round-robin"
	n.Spec.Replicas = 1
	n.Spec.ProbeIntervalSeconds = 2
	return n
}
func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Network)
	}{
		{"valid", func(*Network) {}},
		{"algorithm", func(n *Network) { n.Spec.LoadBalancing.Algorithm = "random" }},
		{"replicas", func(n *Network) { n.Spec.Replicas = 0 }},
		{"interval", func(n *Network) { n.Spec.ProbeIntervalSeconds = 0 }},
		{"self reference", func(n *Network) { n.Spec.ServiceRef.Name = "cm-application-network" }},
		{"numeric port", func(n *Network) { n.Spec.ServiceRef.PortName = "123" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := sample()
			tc.change(&n)
			if got := n.Validate(); (got == nil) != (tc.name == "valid") {
				t.Fatalf("validation: %v", got)
			}
		})
	}
}
func TestDesired(t *testing.T) {
	n := sample()
	d, s := desired(n, "cloudmesh:local")
	if !ownedBy(d, n) || !ownedBy(s, n) {
		t.Fatal("missing owner references")
	}
	container := mapAt(mapAt(mapAt(d, "spec"), "template"), "spec")["containers"].([]any)[0].(object)
	if !contains(container["args"], []any{"-service=echo", "-port-name=http", "-algorithm=round-robin", "-probe-interval=2s", "-admin=:9090"}) {
		t.Fatal(container)
	}
	if mapAt(s, "spec")["type"] != "ClusterIP" {
		t.Fatal("must be internal service")
	}
	if !contains(mapAt(s, "spec")["selector"], mapAt(mapAt(mapAt(d, "spec"), "template"), "metadata")["labels"]) {
		t.Fatal("selector mismatch")
	}
}

// fakeCluster emulates the subset of the API used by the controller. It does
// not emulate admission, SSA field ownership, scheduling, or garbage collection.
type fakeCluster struct {
	objects      map[string]object
	writes       int
	status       Status
	statusWrites int
	listCalls    int
	watchCalls   int
	watchError   bool
	events       []object
}

func (f *fakeCluster) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	path := r.URL.Path
	if strings.HasSuffix(path, "/status") && r.Method == http.MethodPatch {
		var p struct {
			Status Status `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.status = p.Status
		f.statusWrites++
		send(object{})
		return
	}
	if strings.HasSuffix(path, "/"+Plural) && r.Method == http.MethodGet {
		if r.URL.Query().Get("watch") == "true" {
			f.watchCalls++
			if f.watchError {
				http.Error(w, "expired", 410)
				return
			}
			for _, ev := range f.events {
				send(ev)
			}
			return
		}
		f.listCalls++
		send(object{"metadata": object{"resourceVersion": "10"}, "items": []Network{sample()}})
		return
	}
	if r.Method == http.MethodGet {
		if o, ok := f.objects[path]; ok {
			send(o)
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		var o object
		if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if r.Method == http.MethodPost {
			path += "/" + stringAt(mapAt(o, "metadata"), "name")
		} else if r.Header.Get("Content-Type") != "application/apply-patch+yaml" || r.URL.Query().Get("fieldManager") != "cloudmesh-controller" {
			http.Error(w, "bad apply", 400)
			return
		}
		mapAt(o, "metadata")["resourceVersion"] = "11"
		mapAt(o, "metadata")["generation"] = float64(1)
		f.objects[path] = o
		f.writes++
		send(o)
		return
	}
	http.Error(w, "unexpected method", 405)
}
func newFake(t *testing.T) (*Controller, *fakeCluster) {
	t.Helper()
	f := &fakeCluster{objects: map[string]object{}}
	s := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	c := &Controller{API: &API{BaseURL: s.URL, HTTP: s.Client()}, Namespace: "cloudmesh", Image: "cloudmesh:local"}
	f.objects[c.services()+"/echo"] = object{"spec": object{"ports": []any{object{"name": "http", "protocol": "TCP"}}}}
	return c, f
}
func TestReconcileLifecycle(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	n := sample()
	if err := c.Reconcile(ctx, n); err != nil {
		t.Fatal(err)
	}
	if f.writes != 2 || f.status.Phase != "Pending" || f.status.ProxyService != "cm-application-network" {
		t.Fatalf("unexpected create: %d %+v", f.writes, f.status)
	}
	n.Status = f.status
	if err := c.Reconcile(ctx, n); err != nil {
		t.Fatal(err)
	}
	if f.writes != 2 || f.statusWrites != 1 {
		t.Fatal("unchanged reconcile wrote to API")
	}
	d := f.objects[c.deployments()+"/cm-application-network"]
	d["status"] = object{"observedGeneration": float64(1), "availableReplicas": float64(1)}
	if err := c.Reconcile(ctx, n); err != nil {
		t.Fatal(err)
	}
	if f.status.Phase != "Ready" {
		t.Fatalf("status %+v", f.status)
	}
	n.Status = f.status
	n.Metadata.Generation = 2
	n.Spec.LoadBalancing.Algorithm = "least-connections"
	n.Spec.Replicas = 2
	if err := c.Reconcile(ctx, n); err != nil {
		t.Fatal(err)
	}
	if f.writes != 3 || f.status.ObservedGeneration != 2 || f.status.Phase != "Pending" {
		t.Fatalf("unexpected update: %d %+v", f.writes, f.status)
	}
	d = f.objects[c.deployments()+"/cm-application-network"]
	if intAt(mapAt(d, "spec"), "replicas") != 2 {
		t.Fatal("replica drift")
	}
	// Delete the child; next reconciliation repairs it.
	delete(f.objects, c.deployments()+"/cm-application-network")
	if err := c.Reconcile(ctx, n); err != nil {
		t.Fatal(err)
	}
	if f.writes != 4 {
		t.Fatal("missing child was not recreated")
	}
}
func TestReconcileRejectsUnowned(t *testing.T) {
	c, f := newFake(t)
	f.objects[c.deployments()+"/cm-application-network"] = object{"metadata": object{"name": "cm-application-network"}}
	err := c.Reconcile(context.Background(), sample())
	if err == nil || !strings.Contains(err.Error(), "unowned") || f.writes != 0 || f.status.Phase != "Degraded" {
		t.Fatalf("err=%v writes=%d status=%+v", err, f.writes, f.status)
	}
}
func TestMissingTarget(t *testing.T) {
	c, f := newFake(t)
	delete(f.objects, c.services()+"/echo")
	if err := c.Reconcile(context.Background(), sample()); !hasCode(err, 404) || f.writes != 0 || f.status.Phase != "Degraded" {
		t.Fatalf("unexpected: %v %+v", err, f.status)
	}
}
func TestDeletionSkipsWrites(t *testing.T) {
	c, f := newFake(t)
	n := sample()
	ts := "2026-11-26T00:00:00Z"
	n.Metadata.DeletionTimestamp = &ts
	if err := c.Reconcile(context.Background(), n); err != nil || f.writes != 0 || f.statusWrites != 0 {
		t.Fatal("deleting object was reconciled")
	}
}
func TestWatchGenerationAndExpiry(t *testing.T) {
	c, f := newFake(t)
	n := sample()
	same := n
	same.Status.Phase = "Pending"
	n.Metadata.Generation = 2
	n.Spec.Replicas = 2
	f.events = []object{{"type": "MODIFIED", "object": same}, {"type": "MODIFIED", "object": n}}
	if err := c.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.writes != 3 || f.status.ObservedGeneration != 2 || f.statusWrites != 2 {
		t.Fatalf("watch writes=%d status=%+v count=%d", f.writes, f.status, f.statusWrites)
	}
	f.watchError = true
	if err := c.cycle(context.Background()); !hasCode(err, 410) {
		t.Fatalf("expected expiration, got %v", err)
	}
	f.watchError = false
	f.events = nil
	if err := c.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.listCalls != 3 || f.watchCalls != 3 {
		t.Fatal(fmt.Sprintf("list/watch calls: %d/%d", f.listCalls, f.watchCalls))
	}
}
