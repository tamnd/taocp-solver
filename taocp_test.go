package taocp

import (
	"context"
	"testing"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/config"
)

type unusedCompleter struct{}

func (unusedCompleter) Complete(context.Context, api.Request) (api.Response, error) {
	return api.Response{}, nil
}

func TestNewAcceptsInjectedTransportWithoutBaseURL(t *testing.T) {
	t.Parallel()
	cfg := config.FromEnv()
	cfg.BaseURL = ""
	client, err := New(cfg, WithCompleter(unusedCompleter{}))
	if err != nil {
		t.Fatal(err)
	}
	if client.Engine.Client == nil {
		t.Fatal("injected transport missing")
	}
}

func TestNewRequiresTransport(t *testing.T) {
	t.Parallel()
	cfg := config.FromEnv()
	cfg.BaseURL = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("expected missing transport error")
	}
}
