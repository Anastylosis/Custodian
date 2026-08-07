// Package stash talks to a Stash instance's GraphQL API.
//
// The transport — auth, size limits, typed errors, API-key redaction — lives
// in github.com/Anastylosis/stash-go, shared with the other projects here.
// What stays is the query catalogue in queries.go, which is genuinely
// Custodian's: findDuplicateScenes, sceneMerge, moveFiles and the rest exist
// for deduplication and have no counterpart in the other consumers.
package stash

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	upstream "github.com/Anastylosis/stash-go"
)

// Errors from the shared client, re-exported so callers keep asserting on
// stash.HTTPError and stash.GraphQLError. Type aliases, so a value returned by
// the upstream client satisfies these exactly — not a lookalike.
type (
	// HTTPError is a non-2xx response from Stash. Body carries what the
	// server actually said.
	HTTPError = upstream.HTTPError
	// GraphQLError aggregates the `errors` array from a GraphQL response.
	GraphQLError = upstream.APIError
	// GraphQLErrorItem is one entry, keeping its `path` and `extensions`.
	GraphQLErrorItem = upstream.GraphQLError
)

// requestTimeout bounds a hung Stash instance. findDuplicateScenes over a 60k
// scene library is genuinely slow, so this is generous rather than tight — the
// shared client's 30s default would abort it.
const requestTimeout = 5 * time.Minute

// Client is a Stash GraphQL client. Safe for concurrent use.
type Client struct {
	// Endpoint is the base URL as given to NewClient, kept for logging.
	Endpoint string

	up *upstream.Client
}

// NewClient builds a Client pointed at the given Stash base URL. The base URL
// should be the root of the Stash web UI (e.g. "http://host:9999"); the
// "/graphql" suffix is appended automatically.
//
// apiKey is the literal API key, NOT the env var name. Resolve the env var at
// the call site (typically via config.Config.StashAPIKey).
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		Endpoint: baseURL,
		up: upstream.NewClient(baseURL,
			upstream.WithAPIKey(apiKey),
			upstream.WithHTTPClient(&http.Client{Timeout: requestTimeout}),
		),
	}
}

// Execute sends a GraphQL query and decodes the `data` field into out (which
// must be a non-nil pointer to a struct or map matching the query's response
// shape). variables may be nil; out may be nil when the caller does not need
// the data, as for a mutation whose return value is ignored.
//
// Returns:
//   - *HTTPError    — non-2xx response from Stash
//   - *GraphQLError — Stash returned an `errors` array
//   - error         — network failure, JSON decode failure, etc.
func (c *Client) Execute(ctx context.Context, query string, variables map[string]any, out any) error {
	slog.Debug("stash graphql request", "endpoint", c.Endpoint)

	data, err := c.up.Execute(ctx, query, variables)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding stash response: %w", err)
	}
	return nil
}
