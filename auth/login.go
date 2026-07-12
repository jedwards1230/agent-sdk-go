package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// loginFlow is one provider's OAuth login + refresh implementation. Concrete
// flows live in anthropic.go and openai.go; the store wires them by provider id.
type loginFlow interface {
	// provider is the provider id (e.g. "anthropic", "openai").
	provider() string

	// usesCallback reports whether the flow completes via a local HTTP
	// redirect (true) or a user-pasted code (false).
	usesCallback() bool

	// callbackListenAddr is the host:port to bind for a callback flow (e.g.
	// "localhost:1455"). A ":0" port asks for an ephemeral port (tests).
	callbackListenAddr() string
	// callbackPath is the redirect path a callback flow listens on.
	callbackPath() string
	// callbackRedirectURI is the exact redirect_uri a callback flow registers
	// (e.g. "http://localhost:1455/auth/callback").
	callbackRedirectURI() string

	// manualRedirectURI is the fixed redirect_uri a code-paste flow uses.
	manualRedirectURI() string

	// authorize builds the authorize URL and PKCE material for a redirect URI.
	authorize(redirectURI string) (string, pkce, error)
	// exchange trades an authorization code (as delivered — a callback "code"
	// query value or a user-pasted string) for a persisted [Entry].
	exchange(ctx context.Context, hc httpDoer, code string, p pkce) (Entry, error)
	// refresh renews an expired [Entry].
	refresh(ctx context.Context, hc httpDoer, e Entry) (Entry, error)
}

// LoginMode is how a login completes.
type LoginMode int

const (
	// LoginModeCallback completes when the browser redirects to a local
	// listener; the caller invokes [Login.Wait].
	LoginModeCallback LoginMode = iota
	// LoginModeManualCode completes when the user pastes a code back; the
	// caller invokes [Login.Redeem].
	LoginModeManualCode
)

// Login is an in-progress OAuth login. The SDK never opens a browser: the
// caller presents AuthorizeURL, then either waits (callback mode) or redeems a
// pasted code (manual mode). Both paths persist the credential on success.
type Login struct {
	// AuthorizeURL is the URL the user must open to authorize.
	AuthorizeURL string
	// Mode selects which completion method applies.
	Mode LoginMode
	// Wait blocks until a callback-mode login completes (credential persisted)
	// or ctx is done. Nil in manual-code mode.
	Wait func() error
	// Redeem completes a manual-code login with the user-pasted code and
	// persists the credential. Nil in callback mode.
	Redeem func(code string) error
}

// Login begins an OAuth login for a provider id. For a callback provider it
// binds the local listener before returning so the redirect can never race the
// URL being opened.
func (s *Store) Login(ctx context.Context, providerID string) (*Login, error) {
	flow, ok := s.flows[providerID]
	if !ok {
		return nil, fmt.Errorf("auth: no oauth flow registered for %s", providerID)
	}
	if flow.usesCallback() {
		return s.loginCallback(ctx, flow)
	}
	return s.loginManual(ctx, flow)
}

// loginManual builds a code-paste login.
func (s *Store) loginManual(ctx context.Context, flow loginFlow) (*Login, error) {
	authURL, p, err := flow.authorize(flow.manualRedirectURI())
	if err != nil {
		return nil, err
	}
	return &Login{
		AuthorizeURL: authURL,
		Mode:         LoginModeManualCode,
		Redeem: func(code string) error {
			e, err := flow.exchange(ctx, s.httpClient, strings.TrimSpace(code), p)
			if err != nil {
				return err
			}
			return s.Set(flow.provider(), e)
		},
	}, nil
}

// loginCallback binds the local listener, then builds a redirect-based login.
func (s *Store) loginCallback(ctx context.Context, flow loginFlow) (*Login, error) {
	listenAddr := flow.callbackListenAddr()
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("auth: bind callback listener %s: %w", listenAddr, err)
	}

	redirectURI := flow.callbackRedirectURI()
	if strings.HasSuffix(listenAddr, ":0") {
		// Ephemeral port (tests): rewrite the redirect to the bound address.
		redirectURI = "http://" + ln.Addr().String() + flow.callbackPath()
	}

	authURL, p, err := flow.authorize(redirectURI)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	// Buffered so the handler never blocks if Wait already returned via ctx.
	done := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(flow.callbackPath(), func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			writeCallbackPage(w, false)
			trySend(done, fmt.Errorf("auth: authorization denied: %s", e))
			return
		}
		if st := q.Get("state"); st != p.state {
			writeCallbackPage(w, false)
			trySend(done, errors.New("auth: callback state mismatch"))
			return
		}
		code := q.Get("code")
		if code == "" {
			writeCallbackPage(w, false)
			trySend(done, errors.New("auth: callback missing code"))
			return
		}
		e, err := flow.exchange(ctx, s.httpClient, code, p)
		if err != nil {
			writeCallbackPage(w, false)
			trySend(done, err)
			return
		}
		if err := s.Set(flow.provider(), e); err != nil {
			writeCallbackPage(w, false)
			trySend(done, err)
			return
		}
		writeCallbackPage(w, true)
		trySend(done, nil)
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	wait := func() error {
		// Shutdown (not Close) so the in-flight callback response flushes to
		// the browser before the listener tears down; Close would abort it.
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(sctx); err != nil {
				_ = srv.Close()
			}
		}()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return &Login{AuthorizeURL: authURL, Mode: LoginModeCallback, Wait: wait}, nil
}

// trySend delivers on a buffered result channel without blocking a second
// callback (e.g. a browser retry).
func trySend(ch chan error, err error) {
	select {
	case ch <- err:
	default:
	}
}

// writeCallbackPage renders the minimal page the browser shows after the
// redirect. It carries no secrets.
func writeCallbackPage(w http.ResponseWriter, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		_, _ = w.Write([]byte("<!doctype html><title>Signed in</title><body>Signed in. You can close this tab and return to the terminal.</body>"))
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte("<!doctype html><title>Sign-in failed</title><body>Sign-in failed. Return to the terminal for details.</body>"))
}
