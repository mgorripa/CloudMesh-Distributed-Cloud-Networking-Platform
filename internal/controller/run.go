package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

// cycle reconciles the full snapshot, then reacts to new spec generations.
// A finite watch also repairs child-resource drift and refreshes rollout status.
// A watch error (including 410 Gone) causes the next cycle to relist.
func (c *Controller) cycle(ctx context.Context) error {
	seen := map[string]int64{}
	rv, cont := "", ""
	apply := func(n Network) {
		if err := c.Reconcile(ctx, n); err != nil && ctx.Err() == nil {
			log.Printf("reconcile %s: %v", n.Metadata.Name, err)
		}
	}
	for {
		var list struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
				Continue        string `json:"continue"`
			} `json:"metadata"`
			Items []Network `json:"items"`
		}
		q := url.Values{"limit": {"100"}}
		if cont != "" {
			q.Set("continue", cont)
		}
		if err := c.API.do(ctx, http.MethodGet, c.resources()+"?"+q.Encode(), "", nil, &list); err != nil {
			return err
		}
		for _, n := range list.Items {
			apply(n)
			seen[n.Metadata.UID] = n.Metadata.Generation
		}
		rv, cont = list.Metadata.ResourceVersion, list.Metadata.Continue
		if cont == "" {
			break
		}
	}
	q := url.Values{"watch": {"true"}, "resourceVersion": {rv}, "timeoutSeconds": {"15"}}
	res, err := c.API.request(ctx, http.MethodGet, c.resources()+"?"+q.Encode(), "", nil)
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
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch event.Type {
		case "ERROR":
			return fmt.Errorf("watch error: %s", event.Object)
		case "ADDED", "MODIFIED", "DELETED":
			var n Network
			if err := json.Unmarshal(event.Object, &n); err != nil {
				return err
			}
			if event.Type == "DELETED" {
				delete(seen, n.Metadata.UID)
				continue
			}
			gen, ok := seen[n.Metadata.UID]
			if ok && gen == n.Metadata.Generation {
				continue
			} // ignore status-only changes
			apply(n)
			seen[n.Metadata.UID] = n.Metadata.Generation
		}
	}
}

// Run uses a single worker, avoiding overlapping mutations of a resource.
func (c *Controller) Run(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		err := c.cycle(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			delay = time.Second
			continue
		}
		log.Printf("controller list/watch: %v; retry in %s", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
}
