// Package secrets fetches the Anthropic API key. The key is read once at
// startup and held only in the gateway's memory (ADR 0001, Decision E1).
package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// KeySource returns the upstream API key.
type KeySource interface {
	FetchKey(ctx context.Context) (string, error)
}

// GetSecretValueAPI is the one Secrets Manager call used, so tests can fake it.
type GetSecretValueAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// Manager reads the key from one Secrets Manager secret.
type Manager struct {
	Client   GetSecretValueAPI
	SecretID string
}

// NewManager uses the AWS SDK default credential chain, which picks up
// EKS Pod Identity in the cluster.
func NewManager(ctx context.Context, secretID string) (*Manager, error) {
	if secretID == "" {
		return nil, errors.New("secret id is empty")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &Manager{Client: secretsmanager.NewFromConfig(cfg), SecretID: secretID}, nil
}

// FetchKey returns the key. `make secrets` stores it as a plain SecretString;
// it is used as-is apart from trimming surrounding whitespace. Errors never
// include the secret value.
func (s *Manager) FetchKey(ctx context.Context) (string, error) {
	out, err := s.Client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(s.SecretID)})
	if err != nil {
		return "", fmt.Errorf("get secret value %s: %w", s.SecretID, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %s has no string value", s.SecretID)
	}
	return parseKey(*out.SecretString)
}

func parseKey(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("secret holds an empty key")
	}
	return v, nil
}

// Static is a fixed key, for tests.
type Static string

// FetchKey returns the fixed key.
func (s Static) FetchKey(context.Context) (string, error) { return string(s), nil }
