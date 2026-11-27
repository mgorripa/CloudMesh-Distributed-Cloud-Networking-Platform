package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
)

type object = map[string]any

const jsonType = "application/json"
const mergeType = "application/merge-patch+json"

type Controller struct {
	API              *API
	Namespace, Image string
}

func (c *Controller) resources() string {
	return "/apis/" + APIVersion + "/namespaces/" + c.Namespace + "/" + Plural
}
func (c *Controller) deployments() string {
	return "/apis/apps/v1/namespaces/" + c.Namespace + "/deployments"
}
func (c *Controller) services() string   { return "/api/v1/namespaces/" + c.Namespace + "/services" }
func number(i int) float64               { return float64(i) }
func mapAt(m object, k string) object    { v, _ := m[k].(map[string]any); return v }
func stringAt(m object, k string) string { v, _ := m[k].(string); return v }
func intAt(m object, k string) int       { v, _ := m[k].(float64); return int(v) }
func hasCode(err error, code int) bool   { var e *APIError; return errors.As(err, &e) && e.Code == code }

func desired(n Network, image string) (object, object) {
	name := "cm-" + n.Metadata.Name
	labels := object{"app.kubernetes.io/name": "cloudmesh-proxy", "app.kubernetes.io/instance": name}
	owner := object{"apiVersion": APIVersion, "kind": Kind, "name": n.Metadata.Name, "uid": n.Metadata.UID, "controller": true}
	meta := func() object {
		return object{"name": name, "namespace": n.Metadata.Namespace, "labels": labels, "ownerReferences": []any{owner}}
	}
	args := []any{"-service=" + n.Spec.ServiceRef.Name, "-port-name=" + n.Spec.ServiceRef.PortName, "-algorithm=" + n.Spec.LoadBalancing.Algorithm, "-probe-interval=" + strconv.Itoa(n.Spec.ProbeIntervalSeconds) + "s", "-admin=:9090"}
	container := object{"name": "proxy", "image": image, "imagePullPolicy": "Never", "args": args,
		"ports":           []any{object{"name": "http", "containerPort": number(8080)}, object{"name": "tcp", "containerPort": number(9000)}, object{"name": "admin", "containerPort": number(9090)}},
		"readinessProbe":  object{"httpGet": object{"path": "/readyz", "port": "admin"}},
		"livenessProbe":   object{"httpGet": object{"path": "/healthz", "port": "admin"}},
		"securityContext": object{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": object{"drop": []any{"ALL"}}},
		"resources":       object{"requests": object{"cpu": "25m", "memory": "32Mi"}, "limits": object{"memory": "128Mi"}},
	}
	deployment := object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta(), "spec": object{"replicas": number(n.Spec.Replicas), "selector": object{"matchLabels": labels}, "template": object{"metadata": object{"labels": labels}, "spec": object{"serviceAccountName": "cloudmesh", "containers": []any{container}}}}}
	service := object{"apiVersion": "v1", "kind": "Service", "metadata": meta(), "spec": object{"type": "ClusterIP", "selector": labels, "ipFamilyPolicy": "PreferDualStack", "ports": []any{object{"name": "http", "port": number(8080), "targetPort": "http", "protocol": "TCP"}, object{"name": "tcp", "port": number(9000), "targetPort": "tcp", "protocol": "TCP"}}}}
	return deployment, service
}

// contains ignores server-defaulted map fields but requires desired list order
// and length. All desired values use the same JSON types as decoded objects.
func contains(actual, wanted any) bool {
	switch w := wanted.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			x, ok := a[k]
			if !ok || !contains(x, v) {
				return false
			}
		}
		return true
	case []any:
		a, ok := actual.([]any)
		if !ok || len(a) != len(w) {
			return false
		}
		for i, v := range w {
			if !contains(a[i], v) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(actual, wanted)
	}
}
func ownedBy(o object, n Network) bool {
	refs, _ := mapAt(o, "metadata")["ownerReferences"].([]any)
	for _, r := range refs {
		m, _ := r.(map[string]any)
		if m["controller"] == true && m["uid"] == n.Metadata.UID && m["kind"] == Kind && m["apiVersion"] == APIVersion && m["name"] == n.Metadata.Name {
			return true
		}
	}
	return false
}

// ensure creates missing objects and updates only owned objects. resourceVersion
// guards updates against concurrent changes between the GET and PATCH.
func (c *Controller) ensure(ctx context.Context, collection string, wanted object, n Network) (object, error) {
	name := stringAt(mapAt(wanted, "metadata"), "name")
	path := collection + "/" + name
	var actual object
	err := c.API.do(ctx, http.MethodGet, path, "", nil, &actual)
	if hasCode(err, 404) {
		err = c.API.do(ctx, http.MethodPost, collection+"?fieldManager=cloudmesh-controller", jsonType, wanted, &actual)
		return actual, err
	}
	if err != nil {
		return nil, err
	}
	if !ownedBy(actual, n) {
		return nil, fmt.Errorf("refusing to modify unowned object %s", name)
	}
	if contains(actual, wanted) {
		return actual, nil
	}
	// SSA manages declared fields without replacing server-assigned ClusterIPs.
	// Force is restricted to resources already owned by this exact CR UID.
	mapAt(wanted, "metadata")["resourceVersion"] = stringAt(mapAt(actual, "metadata"), "resourceVersion")
	err = c.API.do(ctx, http.MethodPatch, path+"?fieldManager=cloudmesh-controller&force=true", "application/apply-patch+yaml", wanted, &actual)
	return actual, err
}
func (c *Controller) setStatus(ctx context.Context, n Network, s Status) error {
	if n.Status == s {
		return nil
	}
	body := object{"metadata": object{"resourceVersion": n.Metadata.ResourceVersion}, "status": s}
	return c.API.do(ctx, http.MethodPatch, c.resources()+"/"+n.Metadata.Name+"/status", mergeType, body, nil)
}
func (c *Controller) targetExists(ctx context.Context, n Network) error {
	var service object
	err := c.API.do(ctx, http.MethodGet, c.services()+"/"+n.Spec.ServiceRef.Name, "", nil, &service)
	if err != nil {
		return err
	}
	ports, _ := mapAt(service, "spec")["ports"].([]any)
	for _, p := range ports {
		m, _ := p.(map[string]any)
		if m["name"] == n.Spec.ServiceRef.PortName && (m["protocol"] == "TCP" || m["protocol"] == nil) {
			return nil
		}
	}
	return fmt.Errorf("target Service has no matching TCP port")
}

func (c *Controller) Reconcile(ctx context.Context, n Network) error {
	if n.Metadata.DeletionTimestamp != nil {
		return nil
	} // Kubernetes garbage collector removes children.
	if n.Metadata.Namespace != c.Namespace {
		return fmt.Errorf("unexpected namespace")
	}
	s := Status{ObservedGeneration: n.Metadata.Generation, Phase: "Pending", Message: "Waiting for proxy rollout"}
	fail := func(err error) error {
		s.Phase = "Degraded"
		s.Message = err.Error()
		if len(s.Message) > 512 {
			s.Message = s.Message[:512]
		}
		return errors.Join(err, c.setStatus(ctx, n, s))
	}
	if err := n.Validate(); err != nil {
		return fail(err)
	}
	if err := c.targetExists(ctx, n); err != nil {
		return fail(err)
	}
	d, svc := desired(n, c.Image)
	deployment, err := c.ensure(ctx, c.deployments(), d, n)
	if err != nil {
		return fail(err)
	}
	if _, err = c.ensure(ctx, c.services(), svc, n); err != nil {
		return fail(err)
	}
	s.ProxyService = "cm-" + n.Metadata.Name
	st := mapAt(deployment, "status")
	s.AvailableReplicas = intAt(st, "availableReplicas")
	if intAt(st, "observedGeneration") >= intAt(mapAt(deployment, "metadata"), "generation") && s.AvailableReplicas >= n.Spec.Replicas {
		s.Phase = "Ready"
		s.Message = "Desired proxy replicas available"
	}
	return c.setStatus(ctx, n, s)
}
