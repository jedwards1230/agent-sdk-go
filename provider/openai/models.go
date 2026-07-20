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
// Unregistered set — nothing here comes from the embedded registry, since
// enriching a live listing with registry metadata is the caller's decision, not
// the adapter's. The per-field rule on [provider.ModelInfo.Unregistered] then
// applies: a zero metadata field means UNKNOWN, a non-zero one is what the
// vendor said.
//
// What the vendor says differs by route. The API-key route reports identity
// only, so those records carry no metadata at all. The Codex route additionally
// supplies a display name, a context window, a visibility marker, and a list of
// supported reasoning levels, which are carried into DisplayName,
// ContextWindow, Hidden, and Reasoning respectively. Neither route reports
// pricing — the subscription backend has no per-token price at all — so pricing
// is never synthesized, nor is MaxOutput.
//
// Fields ModelInfo has no home for are dropped: created and owned_by on the
// API-key route; description, available_in_plans, and the reasoning level
// VALUES themselves on the Codex route (only their presence is carried, as the
// Reasoning bit — see [countReasoningLevels]). Neither response is paginated.
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

	models, err := decodeModels(data, codex)
	if err != nil {
		return nil, fmt.Errorf("openai: list models: decode response: %w", err)
	}

	out := make([]provider.ModelInfo, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			// An entry with no id names no model; there is nothing a caller
			// could do with it.
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// visibilityHidden is the one Codex visibility value that means "do not offer
// this model in a picker". The field is matched EXACTLY against it, and every
// other value — "list", a marker this SDK has never seen, or an absent field —
// leaves [provider.ModelInfo.Hidden] false. That direction is the safe one: an
// unrecognized marker at worst shows a model the vendor would have tucked away,
// where the inverse would silently hide models over a vocabulary change.
const visibilityHidden = "hide"

// decodeModels turns a listing body in the shape the given route speaks into
// records. It reads the fields ModelInfo has a home for and can honestly carry;
// everything else is either homeless or would amount to inventing metadata.
//
// Entries are returned as-is, including ones with an empty id — filtering those
// is the caller's step, so this function stays a pure decode.
func decodeModels(data []byte, codex bool) ([]provider.ModelInfo, error) {
	if codex {
		// The Codex backend names models with "slug" inside a "models" array.
		// That array is decoded through a pointer so an ABSENT key stays
		// distinguishable from an empty one: absent means the body was not the
		// shape this route promises (most plausibly the public API's), and
		// reporting that as a successful empty catalogue would hide the
		// failure from every caller.
		var res struct {
			Models *[]struct {
				Slug        string `json:"slug"`
				DisplayName string `json:"display_name"`
				// Two spellings of the same idea appear in this catalogue.
				// context_window is the authoritative one when present;
				// max_context_window is the older/wider field kept as a
				// fallback so a body carrying only it is not read as
				// "unknown".
				ContextWindow    int    `json:"context_window"`
				MaxContextWindow int    `json:"max_context_window"`
				Visibility       string `json:"visibility"`
				// Held raw and decoded separately so a vendor shape change
				// degrades this one advisory field instead of failing the whole
				// catalogue. See [countReasoningLevels].
				SupportedReasoningLevels json.RawMessage `json:"supported_reasoning_levels"`
			} `json:"models"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return nil, err
		}
		if res.Models == nil {
			return nil, errNoModelsField
		}
		out := make([]provider.ModelInfo, 0, len(*res.Models))
		for _, m := range *res.Models {
			// Absent and zero are treated alike here: a reported window of 0
			// is not a real limit, so falling back costs nothing. When NEITHER
			// field is present the window stays 0 meaning UNKNOWN — no value
			// is ever invented to fill the gap.
			window := m.ContextWindow
			if window == 0 {
				window = m.MaxContextWindow
			}
			out = append(out, provider.ModelInfo{
				ID:            m.Slug,
				Provider:      providerID,
				DisplayName:   m.DisplayName,
				ContextWindow: window,
				Hidden:        m.Visibility == visibilityHidden,
				// Every level value the catalogue has been observed to use
				// ("low", "medium", "high", "xhigh", "max", "ultra") names a
				// reasoning EFFORT, and the vocabulary has no "none"/"off"
				// member — so a non-empty list is positive vendor evidence that
				// the model reasons. Zero levels leaves this false, which under
				// the per-field [provider.ModelInfo.Unregistered] rule reads as
				// UNKNOWN: a bool cannot distinguish "the vendor says no" from
				// "the vendor said nothing", and the conservative reading is the
				// safe one either way.
				//
				// The levels THEMSELVES are deliberately dropped: nothing
				// consumes them today, and carrying a slice would make ModelInfo
				// non-comparable.
				Reasoning:    countReasoningLevels(m.SupportedReasoningLevels) > 0,
				Unregistered: true,
			})
		}
		return out, nil
	}

	// The public API's listing reports identity only — created and owned_by are
	// the rest of it, and neither has a home on ModelInfo — so these records
	// carry no metadata at all.
	var res struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	out := make([]provider.ModelInfo, 0, len(res.Data))
	for _, m := range res.Data {
		out = append(out, provider.ModelInfo{
			ID:           m.ID,
			Provider:     providerID,
			Unregistered: true,
		})
	}
	return out, nil
}

// countReasoningLevels reports how many reasoning levels a Codex catalogue entry
// declares, reading supported_reasoning_levels TOLERANTLY.
//
// The field is advisory, so no shape of it may ever fail the listing — a vendor
// change here must cost one capability bit, not the whole catalogue (the same
// silently-empty-catalogue failure class [TestListModelsCodexShape] guards).
// Two shapes are understood and everything else counts zero:
//
//   - [{"effort":"low"},…], the shape the catalogue actually serves. Entries
//     with an empty effort name no level and are not counted.
//   - ["low",…], a flat string list. Not observed live, but a plausible enough
//     spelling to accept rather than discard.
//
// An absent field, an empty array, a bare string, an object, or malformed JSON
// all yield 0 with no error.
func countReasoningLevels(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var objs []struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(raw, &objs); err == nil {
		n := 0
		for _, o := range objs {
			if o.Effort != "" {
				n++
			}
		}
		return n
	}
	var strs []string
	if err := json.Unmarshal(raw, &strs); err == nil {
		n := 0
		for _, s := range strs {
			if s != "" {
				n++
			}
		}
		return n
	}
	return 0
}

// errNoModelsField reports a Codex-route 200 whose body carries no models key —
// a wrong-shape response, not an empty catalogue.
var errNoModelsField = errors.New(`response has no "models" field`)

// compile-time guard: the adapter offers the optional listing capability.
var _ provider.ModelLister = (*Provider)(nil)
