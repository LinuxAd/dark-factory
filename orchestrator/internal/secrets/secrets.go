// Package secrets fetches the Anthropic API key. The key is read once at
// startup and held only in the gateway's memory (ADR 0001, Decision E1).
package secrets

import (
	"context"
	"encoding/json"
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

// SecretsManager reads the key from one Secrets Manager secret.
type SecretsManager struct {
	Client   GetSecretValueAPI
	SecretID string
}

// NewSecretsManager uses the AWS SDK default credential chain, which picks up
// EKS Pod Identity in the cluster.
func NewSecretsManager(ctx context.Context, secretID string) (*SecretsManager, error) {
	if secretID == "" {
		return nil, errors.New("secret id is empty")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &SecretsManager{Client: secretsmanager.NewFromConfig(cfg), SecretID: secretID}, nil
}

// FetchKey returns the key. The secret may be the bare key, or a JSON object
// with an "api_key" field. Errors never include the secret value.
func (s *SecretsManager) FetchKey(ctx context.Context) (string, error) {
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
	if strings.HasPrefix(v, "{") {
		var obj struct {
			APIKey string `json:"api_key"`
		}
		if json.Unmarshal([]byte(v), &obj) != nil {
			return "", errors.New("secret is a malformed JSON object")
		}
		v = strings.TrimSpace(obj.APIKey)
	}
	if v == "" {
		return "", errors.New("secret holds an empty key")
	}
	return v, nil
}

// Static is a fixed key, for tests.
type Static string

// FetchKey returns the fixed key.
func (s Static) FetchKey(context.Context) (string, error) { return string(s), nil }
