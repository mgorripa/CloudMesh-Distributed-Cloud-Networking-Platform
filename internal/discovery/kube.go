// Package discovery implements a small Kubernetes list/watch client using the REST API.
package discovery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

type Metadata struct {
	Name            string `json:"name"`
	ResourceVersion string `json:"resourceVersion"`
	Continue        string `json:"continue"`
}
type Port struct {
	Name     *string `json:"name"`
	Protocol *string `json:"protocol"`
	Port     *int    `json:"port"`
}
type Endpoint struct {
	Addresses  []string `json:"addresses"`
	Conditions struct {
		Ready       *bool `json:"ready"`
		Terminating *bool `json:"terminating"`
	} `json:"conditions"`
}
type Slice struct {
	Metadata    Metadata   `json:"metadata"`
	AddressType string     `json:"addressType"`
	Ports       []Port     `json:"ports"`
	Endpoints   []Endpoint `json:"endpoints"`
}
type Client struct {
	BaseURL, Namespace, Service, PortName, TokenFile string
	HTTP                                             *http.Client
}

func InCluster(namespace, service, portName string) (*Client, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS")
	if port == "" {
		port = "443"
	}
	if host == "" {
		return nil, fmt.Errorf("not running in a Kubernetes pod")
	}
	const dir = "/var/run/secrets/kubernetes.io/serviceaccount/"
	pem, err := os.ReadFile(dir + "ca.crt")
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("invalid cluster CA")
	}
	if namespace == "" {
		b, err := os.ReadFile(dir + "namespace")
		if err != nil {
			return nil, err
		}
		namespace = strings.TrimSpace(string(b))
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return &Client{"https://" + net.JoinHostPort(host, port), namespace, service, portName, dir + "token", &http.Client{Transport: t, Timeout: 40 * time.Second}}, nil
}
func (c *Client) get(ctx context.Context, q url.Values) (*http.Response, error) {
	q.Set("labelSelector", "kubernetes.io/service-name="+c.Service)
	u := strings.TrimRight(c.BaseURL, "/") + "/apis/discovery.k8s.io/v1/namespaces/" + url.PathEscape(c.Namespace) + "/endpointslices?" + q.Encode()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.TokenFile != "" {
		b, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	}
	r.Header.Set("Accept", "application/json")
	res, err := c.HTTP.Do(r)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, fmt.Errorf("Kubernetes API: HTTP %d", res.StatusCode)
	}
	return res, nil
}
func (c *Client) addresses(slices map[string]Slice) []string {
	var result []string
	for _, s := range slices {
		if s.AddressType != "IPv4" && s.AddressType != "IPv6" {
			continue
		}
		for _, port := range s.Ports {
			if port.Port == nil || *port.Port < 1 || *port.Port > 65535 || (port.Protocol != nil && *port.Protocol != "TCP") {
				continue
			}
			name := ""
			if port.Name != nil {
				name = *port.Name
			}
			if name != c.PortName {
				continue
			}
			for _, e := range s.Endpoints {
				if (e.Conditions.Ready != nil && !*e.Conditions.Ready) || (e.Conditions.Terminating != nil && *e.Conditions.Terminating) {
					continue
				}
				for _, a := range e.Addresses {
					ip, err := netip.ParseAddr(a)
					if err != nil || ip.Is4() != (s.AddressType == "IPv4") {
						continue
					}
					result = append(result, net.JoinHostPort(ip.String(), fmt.Sprint(*port.Port)))
				}
			}
		}
	}
	return result
}

// Sync lists all slices, then watches from the snapshot resourceVersion. On any watch
// interruption the outer loop relists, including after a resourceVersion expires.
func (c *Client) Sync(ctx context.Context, apply func([]string) error) error {
	slices := map[string]Slice{}
	rv := ""
	cont := ""
	for {
		q := url.Values{"limit": {"500"}}
		if cont != "" {
			q.Set("continue", cont)
		}
		res, err := c.get(ctx, q)
		if err != nil {
			return err
		}
		var list struct {
			Metadata Metadata `json:"metadata"`
			Items    []Slice  `json:"items"`
		}
		err = json.NewDecoder(res.Body).Decode(&list)
		res.Body.Close()
		if err != nil {
			return err
		}
		for _, s := range list.Items {
			slices[s.Metadata.Name] = s
		}
		rv = list.Metadata.ResourceVersion
		cont = list.Metadata.Continue
		if cont == "" {
			break
		}
	}
	if err := apply(c.addresses(slices)); err != nil {
		return err
	}
	res, err := c.get(ctx, url.Values{"watch": {"true"}, "resourceVersion": {rv}, "timeoutSeconds": {"30"}, "allowWatchBookmarks": {"true"}})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	for {
		var event struct {
			Type   string          `json:"type"`
			Object json.RawMessage `json:"object"`
		}
		if err := dec.Decode(&event); err != nil {
			return err
		}
		if event.Type == "ERROR" {
			return fmt.Errorf("watch error: %s", event.Object)
		}
		if event.Type == "BOOKMARK" {
			continue
		}
		var s Slice
		if err := json.Unmarshal(event.Object, &s); err != nil {
			return err
		}
		switch event.Type {
		case "ADDED", "MODIFIED":
			slices[s.Metadata.Name] = s
		case "DELETED":
			delete(slices, s.Metadata.Name)
		default:
			continue
		}
		if err := apply(c.addresses(slices)); err != nil {
			return err
		}
	}
}
