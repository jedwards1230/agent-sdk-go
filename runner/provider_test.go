package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/auth"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// TestNewProvider_UninferableModel asserts a legible, hermetic (no network)
// error for a model id belonging to no known provider family — the only
// non-empty id newProvider still refuses.
func TestNewProvider_UninferableModel(t *testing.T) {
	_, err := newProvider(context.Background(), "not-a-real-model", t.TempDir())
	if !errors.Is(err, provider.ErrUnknownProvider) {
		t.Fatalf("newProvider error = %v, want ErrUnknownProvider", err)
	}
	if !strings.Contains(err.Error(), "not-a-real-model") {
		t.Errorf("newProvider error = %q, want it to name the offending model", err.Error())
	}
}

// TestNewProvider_EmptyModel asserts the empty id is reported as "no model was
// resolved" — a caller-side bug — rather than as an unrecognized model name,
// which would send the user hunting for a typo in a name they never typed.
func TestNewProvider_EmptyModel(t *testing.T) {
	_, err := newProvider(context.Background(), "", t.TempDir())
	if !errors.Is(err, provider.ErrNoModel) {
		t.Fatalf("newProvider(\"\") error = %v, want ErrNoModel", err)
	}
	if errors.Is(err, provider.ErrUnknownProvider) {
		t.Error("empty model reported as an unknown provider; the two cases must stay distinct")
	}
}

// TestNewProvider_UnregisteredModelAccepted is the regression test for the
// allowlist bug: a model the registry does not carry must get PAST model
// resolution. It is expected to fail later, on the credential pre-flight
// against an empty store — reaching that point proves the model itself was
// admitted. If the registry is restored as a gate, this fails with a
// model error instead.
func TestNewProvider_UnregisteredModelAccepted(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "") // hermetic: the pre-flight must find nothing
	_, err := newProvider(context.Background(), "gpt-5.5-turbo-2027", t.TempDir())
	if errors.Is(err, provider.ErrUnknownProvider) || errors.Is(err, provider.ErrNoModel) {
		t.Fatalf("newProvider rejected an unregistered model: %v", err)
	}
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("newProvider error = %v, want the credential pre-flight to be what stops it", err)
	}
}

// TestCompositeCredSource_EnvFallback asserts the composite falls back to the
// environment when the auth store has no entry for the provider, and that
// the combined error is legible when neither source has one.
func TestCompositeCredSource_EnvFallback(t *testing.T) {
	store, err := auth.New(auth.WithRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	t.Run("falls back to env", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
		src := compositeCredSource{store: store, envVars: envVars}

		cred, err := src.Credential(context.Background(), "anthropic")
		if err != nil {
			t.Fatalf("Credential: %v", err)
		}
		if cred.Token != "sk-test-key" {
			t.Errorf("cred.Token = %q, want %q", cred.Token, "sk-test-key")
		}
	})

	t.Run("legible error when neither source has a credential", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		src := compositeCredSource{store: store, envVars: envVars}

		_, err := src.Credential(context.Background(), "anthropic")
		if err == nil {
			t.Fatal("Credential: got nil error, want one")
		}
		// One short, actionable sentence — not the redundant wrapped chain.
		want := "no credential configured for anthropic (set ANTHROPIC_API_KEY)"
		if err.Error() != want {
			t.Errorf("Credential error = %q, want %q", err.Error(), want)
		}
		// errors.Is matches the sentinel, and the underlying causes are retained
		// via Unwrap for structured consumers.
		if !errors.Is(err, ErrNoCredential) {
			t.Errorf("error is not ErrNoCredential: %v", err)
		}
		if !errors.Is(err, auth.ErrNoCredential) {
			t.Errorf("error lost the underlying auth.ErrNoCredential cause: %v", err)
		}
		var nce *NoCredentialError
		if !errors.As(err, &nce) {
			t.Fatalf("error does not unwrap to *NoCredentialError: %v", err)
		}
		if nce.Provider != "anthropic" || nce.EnvVar != "ANTHROPIC_API_KEY" {
			t.Errorf("NoCredentialError = %+v, want Provider=anthropic EnvVar=ANTHROPIC_API_KEY", nce)
		}
	})
}
