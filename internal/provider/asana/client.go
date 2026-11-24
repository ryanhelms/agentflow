package asana

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/nscaledev/agentflow/internal/provider"
)

const apiURL = "https://app.asana.com/api/1.0"

type envelope[T any] struct {
	Data     *T `json:"data"`
	NextPage *struct {
		Offset string `json:"offset"`
	} `json:"next_page"`
}

func request[T any](ctx context.Context, a *Adapter, method, path string, query url.Values, data any) (envelope[T], error) {
	var result envelope[T]
	endpoint := apiURL + path
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	var body *bytes.Reader
	if data != nil {
		payload, err := json.Marshal(struct {
			Data any `json:"data"`
		}{Data: data})
		if err != nil {
			return result, fmt.Errorf("encode asana request: %w", err)
		}
		body = bytes.NewReader(payload)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return result, fmt.Errorf("build asana request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
		// A write with an uncertain outcome must not be replayed by the transport.
		req.GetBody = nil
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return result, fmt.Errorf("asana %s request: %w", method, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return result, fmt.Errorf("asana %s request: %w", method, provider.ErrNotFound)
	case http.StatusConflict, http.StatusPreconditionFailed:
		return result, fmt.Errorf("asana %s request: %w", method, provider.ErrConflict)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("asana %s request returned http %d", method, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return result, fmt.Errorf("decode asana %s response: %w", method, err)
	}
	if result.Data == nil {
		return result, errors.New("asana response is missing data")
	}
	return result, nil
}
