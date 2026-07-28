// Package taocp provides a reusable TAOCP solution and review workflow.
// Applications can use Client directly, or assemble the public subpackages for
// custom storage, prompting, transport, and exercise sources.
package taocp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/config"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/prompt"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/solver"
)

type Option func(*Client)

type Client struct {
	Config config.Config
	Engine *solver.Engine
}

func New(cfg config.Config, options ...Option) (*Client, error) {
	cfg.Normalize()
	if err := cfg.Validate(false); err != nil {
		return nil, err
	}
	client := &Client{Config: cfg}
	client.Engine = &solver.Engine{
		Repository: exercise.NewRepository(cfg.TAOCPRoot),
		Prompts:    prompt.Builder{},
		Store:      result.Store{Root: cfg.OutputRoot},
	}
	if cfg.BaseURL != "" {
		client.Engine.Client = &api.ChatClient{
			URL:        cfg.ChatCompletionsURL(),
			APIKey:     cfg.APIKey,
			MaxRetries: cfg.MaxRetries,
			HTTPClient: &http.Client{Timeout: cfg.Timeout},
			UserAgent:  "taocp-solver-library",
		}
	}
	for _, option := range options {
		option(client)
	}
	if client.Engine == nil || client.Engine.Repository == nil {
		return nil, fmt.Errorf("invalid solver engine configuration")
	}
	if client.Engine.Client == nil {
		return nil, fmt.Errorf("API base URL is required unless WithCompleter supplies a transport")
	}
	return client, nil
}

func FromEnv(options ...Option) (*Client, error) {
	return New(config.FromEnv(), options...)
}

func WithCompleter(completer api.Completer) Option {
	return func(client *Client) {
		client.Engine.Client = completer
	}
}

func WithProgress(writer io.Writer) Option {
	return func(client *Client) {
		if writer == nil {
			client.Engine.Progress = nil
			return
		}
		client.Engine.Progress = func(step solver.Progress) {
			_, _ = fmt.Fprintln(writer, step)
		}
	}
}

func WithRepository(repository *exercise.Repository) Option {
	return func(client *Client) {
		client.Engine.Repository = repository
	}
}

func WithStore(store result.Store) Option {
	return func(client *Client) {
		client.Engine.Store = store
	}
}

func (c *Client) Solve(ctx context.Context, section string, number int, options solver.Options) (result.Result, error) {
	if options.Model == "" {
		options.Model = c.Config.Model
	}
	if options.MaxCorrections < 0 {
		options.MaxCorrections = 0
	}
	if options.Candidates < 1 {
		options.Candidates = c.Config.Candidates
	}
	if options.Mode == solver.ModeSlow {
		options.Verify = true
	}
	return c.Engine.Solve(ctx, section, number, options)
}

func (c *Client) SolveReference(ctx context.Context, reference string, options solver.Options) (result.Result, error) {
	section, number, err := exercise.ParseReference(reference)
	if err != nil {
		return result.Result{}, err
	}
	return c.Solve(ctx, section, number, options)
}

func (c *Client) Review(ctx context.Context, section string, number int, solution string) (string, string, error) {
	return c.Engine.Review(ctx, section, number, solution, c.Config.Model)
}

func DefaultConfig(baseURL, apiKey string) config.Config {
	cfg := config.FromEnv()
	cfg.BaseURL = baseURL
	cfg.APIKey = apiKey
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Minute
	}
	return cfg
}
