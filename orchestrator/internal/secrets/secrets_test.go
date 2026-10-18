package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type fakeSM struct {
	value *string
	err   error
	gotID string
}

func (f *fakeSM) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.gotID = aws.ToString(in.SecretId)
	if f.err != nil {
		return nil, f.err
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: f.value}, nil
}

func TestFetchKey(t *testing.T) {
	cases := []struct {
		name    string
		value   *string
		want    string
		wantErr bool
	}{
		{"plain", aws.String("sk-ant-test"), "sk-ant-test", false},
		{"surrounding whitespace trimmed", aws.String(" \tsk-ant-test\r\n"), "sk-ant-test", false},
		{"no JSON unwrapping", aws.String(`{"api_key":"sk-ant-json"}`), `{"api_key":"sk-ant-json"}`, false},
		{"empty", aws.String("  "), "", true},
		{"binary only", nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSM{value: tc.value}
			s := &Manager{Client: f, SecretID: "plat/factory/anthropic-api-key"}
			got, err := s.FetchKey(context.Background())
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q, %v; want %q, err=%v", got, err, tc.want, tc.wantErr)
			}
			if f.gotID != "plat/factory/anthropic-api-key" {
				t.Errorf("secret id %q", f.gotID)
			}
			if err != nil && tc.value != nil && strings.Contains(err.Error(), "sk-ant") {
				t.Errorf("error leaks secret: %v", err)
			}
		})
	}
}

func TestFetchKeyAPIError(t *testing.T) {
	s := &Manager{Client: &fakeSM{err: errors.New("AccessDenied")}, SecretID: "x"}
	if _, err := s.FetchKey(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
