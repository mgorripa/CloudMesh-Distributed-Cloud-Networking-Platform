package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestListWatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("labelSelector") != "kubernetes.io/service-name=echo" {
			t.Error("missing selector")
		}
		if q.Get("watch") == "true" {
			if q.Get("resourceVersion") != "42" {
				t.Error("wrong resourceVersion")
			}
			fmt.Fprintln(w, `{"type":"ADDED","object":{"metadata":{"name":"v6"},"addressType":"IPv6","ports":[{"name":"http","port":8081}],"endpoints":[{"addresses":["fd00::1"]}]}}`)
			fmt.Fprintln(w, `{"type":"DELETED","object":{"metadata":{"name":"v4"}}}`)
			return
		}
		fmt.Fprintln(w, `{"metadata":{"resourceVersion":"42"},"items":[{"metadata":{"name":"v4"},"addressType":"IPv4","ports":[{"name":"http","port":8081},{"name":"other","port":9999}],"endpoints":[{"addresses":["10.0.0.1"],"conditions":{"ready":true}},{"addresses":["10.0.0.2"],"conditions":{"ready":false}},{"addresses":["10.0.0.3"],"conditions":{"terminating":true}}]}]}`)
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL, Namespace: "default", Service: "echo", PortName: "http", HTTP: server.Client()}
	var states [][]string
	_ = c.Sync(context.Background(), func(a []string) error { states = append(states, a); return nil })
	if len(states) != 3 || !reflect.DeepEqual(states[0], []string{"10.0.0.1:8081"}) || len(states[1]) != 2 || !reflect.DeepEqual(states[2], []string{"[fd00::1]:8081"}) {
		t.Fatalf("bad states %v", states)
	}
}
