// Package controller turns a CloudMeshNetwork declaration into proxy workloads.
package controller

import (
	"fmt"
	"regexp"
	"strings"
)

const APIVersion = "networking.cloudmesh.io/v1alpha1"
const Kind = "CloudMeshNetwork"
const Plural = "cloudmeshnetworks"

type Metadata struct {
	Name              string  `json:"name"`
	Namespace         string  `json:"namespace"`
	UID               string  `json:"uid"`
	ResourceVersion   string  `json:"resourceVersion"`
	Generation        int64   `json:"generation"`
	DeletionTimestamp *string `json:"deletionTimestamp,omitempty"`
}
type Spec struct {
	ServiceRef struct {
		Name     string `json:"name"`
		PortName string `json:"portName"`
	} `json:"serviceRef"`
	LoadBalancing struct {
		Algorithm string `json:"algorithm"`
	} `json:"loadBalancing"`
	Replicas             int `json:"replicas"`
	ProbeIntervalSeconds int `json:"probeIntervalSeconds"`
}
type Status struct {
	ObservedGeneration int64  `json:"observedGeneration"`
	Phase              string `json:"phase"`
	Message            string `json:"message"`
	ProxyService       string `json:"proxyService"`
	AvailableReplicas  int    `json:"availableReplicas"`
}
type Network struct {
	Metadata Metadata `json:"metadata"`
	Spec     Spec     `json:"spec"`
	Status   Status   `json:"status"`
}

var label = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
var portLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var hasLetter = regexp.MustCompile(`[a-z]`)

func (n Network) Validate() error {
	s := n.Spec
	if n.Metadata.UID == "" || !label.MatchString(n.Metadata.Name) || len(n.Metadata.Name) > 60 {
		return fmt.Errorf("resource name must be a DNS label of at most 60 characters")
	}
	if !label.MatchString(s.ServiceRef.Name) || len(s.ServiceRef.Name) > 63 || strings.HasPrefix(s.ServiceRef.Name, "cm-") || s.ServiceRef.Name == "cloudmesh" {
		return fmt.Errorf("invalid target service or self-reference")
	}
	if !portLabel.MatchString(s.ServiceRef.PortName) || !hasLetter.MatchString(s.ServiceRef.PortName) || len(s.ServiceRef.PortName) > 15 {
		return fmt.Errorf("invalid named service port")
	}
	switch s.LoadBalancing.Algorithm {
	case "round-robin", "least-connections", "hash":
	default:
		return fmt.Errorf("unsupported scheduling algorithm")
	}
	if s.Replicas < 1 || s.Replicas > 5 {
		return fmt.Errorf("replicas must be 1..5")
	}
	if s.ProbeIntervalSeconds < 1 || s.ProbeIntervalSeconds > 60 {
		return fmt.Errorf("probe interval must be 1..60 seconds")
	}
	return nil
}
