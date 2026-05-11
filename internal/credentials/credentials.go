package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"codex-gate/internal/redaction"
)

var (
	ErrCredentialNotFound     = errors.New("credential not found")
	ErrSecureStoreUnsupported = errors.New("secure store provider is not configured")
	ErrTokenPersistenceDenied = errors.New("token persistence is disabled and requires explicit human approval")
)

type Source string

const (
	SourceEnv         Source = "env"
	SourceSecureStore Source = "secure_store"
)

type Credential struct {
	Name   string
	Value  string
	Source Source
}

type Loader interface {
	Load(ctx context.Context, key string) (Credential, error)
}

type SecureStore interface {
	Load(ctx context.Context, key string) (string, error)
}

type EnvProvider struct {
	Lookup func(string) (string, bool)
}

func NewEnvProvider() *EnvProvider {
	return &EnvProvider{Lookup: os.LookupEnv}
}

func (p *EnvProvider) Load(_ context.Context, key string) (Credential, error) {
	lookup := p.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return Credential{}, fmt.Errorf("%w: %s", ErrCredentialNotFound, sanitizeCredentialKey(key))
	}
	return Credential{Name: key, Value: value, Source: SourceEnv}, nil
}

type SecureStoreProvider struct {
	Store SecureStore
}

func (p *SecureStoreProvider) Load(ctx context.Context, key string) (Credential, error) {
	if p.Store == nil {
		return Credential{}, fmt.Errorf("%w: %s", ErrSecureStoreUnsupported, sanitizeCredentialKey(key))
	}
	value, err := p.Store.Load(ctx, key)
	if err != nil {
		return Credential{}, sanitizeCredentialError(key, err)
	}
	if strings.TrimSpace(value) == "" {
		return Credential{}, fmt.Errorf("%w: %s", ErrCredentialNotFound, sanitizeCredentialKey(key))
	}
	return Credential{Name: key, Value: value, Source: SourceSecureStore}, nil
}

type ChainLoader struct {
	Loaders []Loader
}

func (c ChainLoader) Load(ctx context.Context, key string) (Credential, error) {
	var reasons []string
	for _, loader := range c.Loaders {
		credential, err := loader.Load(ctx, key)
		if err == nil {
			return credential, nil
		}
		if errors.Is(err, ErrCredentialNotFound) || errors.Is(err, ErrSecureStoreUnsupported) {
			reasons = append(reasons, redaction.RedactError(err))
			continue
		}
		return Credential{}, sanitizeCredentialError(key, err)
	}
	if len(reasons) == 0 {
		return Credential{}, fmt.Errorf("%w: %s", ErrCredentialNotFound, sanitizeCredentialKey(key))
	}
	return Credential{}, fmt.Errorf(
		"%w: %s (%s)",
		ErrCredentialNotFound,
		sanitizeCredentialKey(key),
		strings.Join(reasons, "; "),
	)
}

type TokenPersistence struct {
	Enabled bool
}

func (p TokenPersistence) Save(_ context.Context, _ string, _ string) error {
	if !p.Enabled {
		return ErrTokenPersistenceDenied
	}
	return errors.New("token persistence backend is not implemented")
}

func sanitizeCredentialError(key string, err error) error {
	return fmt.Errorf("credential load failed for %s: %s", sanitizeCredentialKey(key), redaction.RedactError(err))
}

func sanitizeCredentialKey(key string) string {
	return redaction.RedactText(key)
}
