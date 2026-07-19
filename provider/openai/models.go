package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// modelsPath is the Models endpoint, relative to the credential-derived base
// URL (which already carries the /v1 prefix for the API-key route).
const modelsPath = "/models"

// listBodyLimit caps how much of a listing response is read, so a misrouted or
// hostile endpoint cannot stream unbounded data into memory.
const listBodyLimit = 8 << 20

// ListModels reports the models this credential can reach, via GET /models on
// the credential-derived base URL. It implements [provider.ModelLister].
//
// The endpoint returns identity only — id, object, created, owned_by — and no
// pricing, context window, max output, or reasoning capability. Every returned
// record therefore carries the id, "openai" as the provider, and Unregistered
// set with all metadata fields at their zero value meaning UNKNOWN, per
// [provider.ModelInfo]. Nothing is backfilled from the embedded registry:
// enriching a live listing with registry metadata is the caller's decision, not
// the adapter's. created and owned_by are dropped because ModelInfo has no
// field for them. The response is not paginated.
//
// Routing follows the streaming path: an API key lists against the public API,
// while an OAuth (ChatGPT subscription) credential targets the Codex backend,
// which serves the streaming endpoint but is not documented to expose a models
// listing — expect an *APIError there rather than a catalogue.
//
// A vendor listing of zero models returns an empty slice and a nil error.
func (p *Provider) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	cred, err := p.creds.Credential(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("openai: resolve credential: %w", err)
	}

	base, headers, err := p.route(cred)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+modelsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("openai: new request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.httpc.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("openai: list models: %w", ctxErr)
		}
		return nil, fmt.Errorf("openai: list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, listBodyLimit))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("openai: list models: %w", ctxErr)
		}
		return nil, fmt.Errorf("openai: list models: read body: %w", err)
	}

	var res struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("openai: list models: decode response: %w", err)
	}

	out := make([]provider.ModelInfo, 0, len(res.Data))
	for _, m := range res.Data {
		if m.ID == "" {
			// An entry with no id names no model; there is nothing a caller
			// could do with it.
			continue
		}
		out = append(out, provider.ModelInfo{
			ID:           m.ID,
			Provider:     providerID,
			Unregistered: true,
		})
	}
	return out, nil
}

// compile-time guard: the adapter offers the optional listing capability.
var _ provider.ModelLister = (*Provider)(nil)
