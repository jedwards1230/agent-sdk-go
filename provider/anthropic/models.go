package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// Model-listing endpoint constants.
const (
	modelsPath = "/v1/models"

	// listPageSize is the per-page item count requested from the Models API,
	// whose documented maximum is 1000. One page covers the catalogue today;
	// pagination is still followed so a larger catalogue is not silently
	// truncated.
	listPageSize = 1000

	// maxListPages bounds pagination so a backend that always reports has_more
	// cannot spin forever. It is a safety stop, not an expected limit.
	maxListPages = 20

	// listBodyLimit caps how much of a listing response is read, so a
	// misrouted or hostile endpoint cannot stream unbounded data into memory.
	listBodyLimit = 8 << 20
)

// listResponse is the Models API page envelope.
type listResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// ListModels reports the models this credential can reach, via GET /v1/models,
// following pagination. It implements [provider.ModelLister].
//
// The endpoint returns identity only — id, type, display_name, created_at — and
// no pricing, context window, max output, or reasoning capability. Every
// returned record therefore carries the id, "anthropic" as the provider, the
// vendor's display_name, and Unregistered set. Per the per-field rule on
// [provider.ModelInfo.Unregistered], the metadata fields this endpoint says
// nothing about stay at their zero value meaning UNKNOWN — they are never
// backfilled from the embedded registry, since enriching a live listing with
// registry metadata is the caller's decision, not the adapter's. created_at is
// dropped because ModelInfo has no field for it.
//
// A vendor listing of zero models returns an empty slice and a nil error.
func (p *Provider) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	cred, err := p.creds.Credential(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("anthropic: resolve credential: %w", err)
	}

	out := make([]provider.ModelInfo, 0, 64)
	var afterID string
	for page := 0; page < maxListPages; page++ {
		res, err := p.listPage(ctx, cred, afterID)
		if err != nil {
			return nil, err
		}
		for _, m := range res.Data {
			if m.ID == "" {
				// An entry with no id names no model; there is nothing a
				// caller could do with it.
				continue
			}
			out = append(out, provider.ModelInfo{
				ID:           m.ID,
				Provider:     providerID,
				DisplayName:  m.DisplayName,
				Unregistered: true,
			})
		}
		if !res.HasMore || res.LastID == "" {
			return out, nil
		}
		afterID = res.LastID
	}
	return nil, fmt.Errorf("anthropic: list models: pagination exceeded %d pages", maxListPages)
}

// listPage fetches one page of the models listing, starting after afterID when
// it is non-empty.
func (p *Provider) listPage(ctx context.Context, cred provider.Credential, afterID string) (listResponse, error) {
	q := url.Values{"limit": {strconv.Itoa(listPageSize)}}
	if afterID != "" {
		q.Set("after_id", afterID)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+modelsPath+"?"+q.Encode(), nil)
	if err != nil {
		return listResponse{}, fmt.Errorf("anthropic: new request: %w", err)
	}
	p.applyHeaders(httpReq, cred)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return listResponse{}, fmt.Errorf("anthropic: list models: %w", ctxErr)
		}
		return listResponse{}, fmt.Errorf("anthropic: list models: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// apiError closes the body.
		return listResponse{}, apiError(resp)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, listBodyLimit))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return listResponse{}, fmt.Errorf("anthropic: list models: %w", ctxErr)
		}
		return listResponse{}, fmt.Errorf("anthropic: list models: read body: %w", err)
	}

	var res listResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return listResponse{}, fmt.Errorf("anthropic: list models: decode response: %w", err)
	}
	return res, nil
}

// compile-time guard: the adapter offers the optional listing capability.
var _ provider.ModelLister = (*Provider)(nil)
