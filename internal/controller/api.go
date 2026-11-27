package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// API is a deliberately small REST client. It reuses CloudMesh's TLS and
// service-account setup; credentials are reread to support token rotation.
type API struct {
	BaseURL, TokenFile string
	HTTP               *http.Client
}
type APIError struct {
	Code int
	Body string
}

func (e *APIError) Error() string { return fmt.Sprintf("Kubernetes HTTP %d: %s", e.Code, e.Body) }
func (a *API) request(ctx context.Context, method, path, contentType string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if a.TokenFile != "" {
		b, err := os.ReadFile(a.TokenFile)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	}
	res, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return nil, &APIError{res.StatusCode, string(b)}
	}
	return res, nil
}
func (a *API) do(ctx context.Context, method, path, ct string, body, out any) error {
	res, err := a.request(ctx, method, path, ct, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
