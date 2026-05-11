package credentials

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeStore struct {
	value string
	err   error
}

func (f fakeStore) Load(_ context.Context, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.value, nil
}

func TestEnvProviderLoadSuccess(t *testing.T) {
	provider := &EnvProvider{
		Lookup: func(key string) (string, bool) {
			if key == "CODEX_API_KEY" {
				return "demo-value", true
			}
			return "", false
		},
	}

	credential, err := provider.Load(context.Background(), "CODEX_API_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if credential.Source != SourceEnv {
		t.Fatalf("unexpected source %q", credential.Source)
	}
	if credential.Value != "demo-value" {
		t.Fatalf("unexpected value %q", credential.Value)
	}
}

func TestEnvProviderLoadMissing(t *testing.T) {
	provider := &EnvProvider{
		Lookup: func(string) (string, bool) { return "", false },
	}
	_, err := provider.Load(context.Background(), "CODEX_API_KEY")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound, got %v", err)
	}
}

func TestEnvProviderSanitizesCredentialKeyInErrors(t *testing.T) {
	provider := &EnvProvider{
		Lookup: func(string) (string, bool) { return "", false },
	}
	misusedKey := "sk-" + strings.Repeat("x", 24)
	_, err := provider.Load(context.Background(), misusedKey)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), misusedKey) {
		t.Fatalf("credential key leaked in error: %v", err)
	}
}

func TestSecureStoreUnsupported(t *testing.T) {
	provider := &SecureStoreProvider{}
	_, err := provider.Load(context.Background(), "CODEX_API_KEY")
	if !errors.Is(err, ErrSecureStoreUnsupported) {
		t.Fatalf("expected ErrSecureStoreUnsupported, got %v", err)
	}
}

func TestSecureStoreErrorIsRedacted(t *testing.T) {
	provider := &SecureStoreProvider{
		Store: fakeStore{err: errors.New("token=super-secret")},
	}
	_, err := provider.Load(context.Background(), "CODEX_API_KEY")
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("sensitive value leaked in error: %v", err)
	}
}

func TestChainLoaderFallsBackToSecureStore(t *testing.T) {
	env := &EnvProvider{
		Lookup: func(string) (string, bool) { return "", false },
	}
	store := &SecureStoreProvider{
		Store: fakeStore{value: "stored-token"},
	}
	loader := ChainLoader{Loaders: []Loader{env, store}}
	credential, err := loader.Load(context.Background(), "CODEX_API_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if credential.Source != SourceSecureStore {
		t.Fatalf("expected secure_store source, got %q", credential.Source)
	}
}

func TestTokenPersistenceDisabledByDefault(t *testing.T) {
	persist := TokenPersistence{}
	err := persist.Save(context.Background(), "CODEX_API_KEY", "demo")
	if !errors.Is(err, ErrTokenPersistenceDenied) {
		t.Fatalf("expected ErrTokenPersistenceDenied, got %v", err)
	}
}
