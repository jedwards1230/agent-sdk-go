package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// modelsPath is the Models endpoint, relative to the credential-derived base
// URL (which already carries the /v1 prefix for the API-key route).
const modelsPath = "/models"

// codexClientVersionParam is the query parameter the Codex backend requires on
// GET /models. Without it that backend answers HTTP 400 with a "Field required"
// validation error naming ('query', 'client_version'), so the listing can never
// succeed for an OAuth credential unless it is sent.
const codexClientVersionParam = "client_version"

// defaultCodexClientVersion is the value sent for [codexClientVersionParam].
//
// Provenance and uncertainty: the parameter is undocumented. It names the
// version of the Codex client making the call, and the backend appears only to
// check that it is PRESENT — distinct values, including ones matching no real
// Codex release, were each accepted with HTTP 200. This literal is therefore a
// plausible-looking placeholder rather than a version this SDK actually is, and
// the accepted range is not understood. Should the backend begin validating it,
// override with [WithCodexClientVersion] instead of waiting for an SDK release.
const defaultCodexClientVersion = "0.144.3"

// listBodyLimit caps how much of a listing response is read, so a misrouted or
// hostile endpoint cannot stream unbounded data into memory.
const listBodyLimit = 8 << 20

// ListModels reports the models this credential can reach, via GET /models on
// the credential-derived base URL. It implements [provider.ModelLister].
//
// Every returned record carries the model id, "openai" as the provider, and
// Unregistered set with all metadata fields at their zero value meaning
// UNKNOWN, per [provider.ModelInfo]. Neither route reports pricing — the
// subscription backend has no per-token price at all — so pricing is never
// synthesized. Nothing is backfilled from the embedded registry: enriching a
// live listing with registry metadata is the caller's decision, not the
// adapter's. Fields ModelInfo has no home for are dropped: created and owned_by
// on the API-key route; display_name, description, context windows, visibility,
// and plan availability on the Codex route. Neither response is paginated.
//
// Routing follows the streaming path: an API key lists against the public API,
// while an OAuth (ChatGPT subscription) credential targets the Codex backend.
// BOTH routes serve a listing, but they are not the same endpoint:
//
//   - The API-key route returns {"data":[{"id":...}]} and takes no parameters.
//   - The Codex route REQUIRES a client_version query parameter (see
//     [defaultCodexClientVersion]) and answers with a differently shaped
//     {"models":[{"slug":...}]} body, keyed on slug rather than id.
//
// The two shapes do not overlap, so the body is decoded per route. Decoding a
// Codex body with the public API's decoder does not fail — it silently yields
// an empty catalogue, which is why an absent models key is reported as an error
// on that route rather than as "the vendor listed nothing".
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
	// The credential kind, not the base URL, selects the wire contract: a
	// WithBaseURL override redirects the host but preserves routing, so tests
	// exercise the real oauth-vs-api-key behavior.
	codex := cred.Kind == provider.CredOAuth

	endpoint := base + modelsPath
	if codex {
		// Sent on the Codex route only. The public API ignores unknown query
		// parameters, so adding it there would be harmless but meaningless: it
		// would misreport this SDK as a Codex client to a backend that never
		// asked, for no benefit.
		endpoint += "?" + url.Values{codexClientVersionParam: {p.codexClientVersion}}.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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

	ids, err := decodeModelIDs(data, codex)
	if err != nil {
		return nil, fmt.Errorf("openai: list models: decode response: %w", err)
	}

	out := make([]provider.ModelInfo, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			// An entry with no id names no model; there is nothing a caller
			// could do with it.
			continue
		}
		out = append(out, provider.ModelInfo{
			ID:           id,
			Provider:     providerID,
			Unregistered: true,
		})
	}
	return out, nil
}

// decodeModelIDs extracts model ids from a listing body in the shape the given
// route speaks. Only the id-bearing field is read: every other field either has
// no home on [provider.ModelInfo] or would amount to inventing metadata.
func decodeModelIDs(data []byte, codex bool) ([]string, error) {
	if codex {
		// The Codex backend names models with "slug" inside a "models" array.
		// That array is decoded through a pointer so an ABSENT key stays
		// distinguishable from an empty one: absent means the body was not the
		// shape this route promises (most plausibly the public API's), and
		// reporting that as a successful empty catalogue would hide the
		// failure from every caller.
		var res struct {
			Models *[]struct {
				Slug string `json:"slug"`
			} `json:"models"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return nil, err
		}
		if res.Models == nil {
			return nil, errNoModelsField
		}
		ids := make([]string, 0, len(*res.Models))
		for _, m := range *res.Models {
			ids = append(ids, m.Slug)
		}
		return ids, nil
	}

	var res struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(res.Data))
	for _, m := range res.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// errNoModelsField reports a Codex-route 200 whose body carries no models key —
// a wrong-shape response, not an empty catalogue.
var errNoModelsField = errors.New(`response has no "models" field`)

// compile-time guard: the adapter offers the optional listing capability.
var _ provider.ModelLister = (*Provider)(nil)
