package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/babarot/gh-tidy/internal/logging"
)

// Client reads GitHub for gh-tidy: an owner's list of repositories through
// REST, with conditional requests, and repositories' heavier fields through
// GraphQL, by name. Every call returns the rate limit the response
// reported. It never writes to GitHub.
type Client struct {
	http     *http.Client
	host     string
	restBase string // ends with a slash
	gqlURL   string
	log      *slog.Logger
}

// Options set up a Client. Transport and AuthToken are for tests: without
// them, gh's own authentication for Host is used.
type Options struct {
	Host      string
	Transport http.RoundTripper
	AuthToken string
	Logger    *slog.Logger
}

// requestTimeout bounds one request: a constant, past GitHub's own limit
// of ten seconds on a query.
const requestTimeout = 30 * time.Second

// NewClient returns a client for opts.Host, github.com when empty.
func NewClient(opts Options) (*Client, error) {
	host := opts.Host
	if host == "" {
		host = "github.com"
	}
	// LogIgnoreEnv: GH_DEBUG would have go-gh write requests to stderr,
	// under the TUI. Timeout bounds a request whose caller set no deadline.
	hc, err := api.NewHTTPClient(api.ClientOptions{Host: host, Transport: opts.Transport, AuthToken: opts.AuthToken,
		Headers: map[string]string{"Accept": "application/vnd.github+json"}, LogIgnoreEnv: true, Timeout: requestTimeout})
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = logging.Discard()
	}
	c := &Client{http: hc, host: host, log: log}
	if host == "github.com" {
		c.restBase, c.gqlURL = "https://api.github.com/", "https://api.github.com/graphql"
	} else {
		c.restBase, c.gqlURL = "https://"+host+"/api/v3/", "https://"+host+"/api/graphql"
	}
	return c, nil
}

// Host is the host the client reads.
func (c *Client) Host() string { return c.host }

// Rate is the rate limit as a response reported it.
type Rate struct {
	Resource  string // core or graphql
	Limit     int
	Remaining int
	Used      int
	Reset     time.Time
	// Cost is what a GraphQL query cost; zero for REST, and for a
	// conditional request answered with 304.
	Cost      int
	RequestID string
	// Date is GitHub's clock when it answered, from the Date header; zero
	// when it is missing or does not parse.
	Date time.Time
}

// Known reports whether the response carried rate limit headers.
func (r Rate) Known() bool { return r.Limit > 0 }

func rateFrom(h http.Header) Rate {
	n := func(k string) int { v, _ := strconv.Atoi(h.Get(k)); return v }
	r := Rate{Resource: h.Get("X-Ratelimit-Resource"), Limit: n("X-Ratelimit-Limit"), Remaining: n("X-Ratelimit-Remaining"),
		Used: n("X-Ratelimit-Used"), RequestID: h.Get("X-Github-Request-Id")}
	if s := n("X-Ratelimit-Reset"); s > 0 {
		r.Reset = time.Unix(int64(s), 0)
	}
	if d, err := http.ParseTime(h.Get("Date")); err == nil {
		r.Date = d
	}
	return r
}

// RateLimitError is GitHub refusing a request for its rate limits.
// Primary means the hourly points ran out (wait until Reset); otherwise a
// secondary limit was hit (wait RetryAfter, or a minute without one, as
// GitHub's documentation says).
type RateLimitError struct {
	Primary    bool
	RetryAfter time.Duration
	Reset      time.Time
	Rate       Rate
}

func (e *RateLimitError) Error() string {
	if e.Primary {
		return "GitHub rate limit used up until " + e.Reset.Format("15:04")
	}
	return "GitHub secondary rate limit; retry after " + e.Wait().String()
}

// Wait is how long to wait before asking GitHub anything again.
func (e *RateLimitError) Wait() time.Duration {
	switch {
	case e.Primary && !e.Reset.IsZero():
		return max(time.Until(e.Reset), time.Second)
	case e.RetryAfter > 0:
		return e.RetryAfter
	}
	return time.Minute
}

// ErrTimeout is GitHub giving up on a query it took too long over (a 502
// or 504). GitHub takes extra points for it from the next hour, so the
// caller asks for less at a time.
var ErrTimeout = errors.New("GitHub took too long to answer (502/504); asking for less at a time")

// StatusError is any other response GitHub refused.
type StatusError struct {
	Status  int
	Message string
	SSO     string // the X-GitHub-SSO header, when the token is not authorized for an organization
}

func (e *StatusError) Error() string { return fmt.Sprintf("GitHub %d: %s", e.Status, e.Message) }

// do sends req and sorts out the refusals every endpoint shares: the
// rate limits and timeouts. The body of a response it returns is open.
func (c *Client) do(req *http.Request, op string) (*http.Response, Rate, error) {
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, Rate{}, err
	}
	rate := rateFrom(resp.Header)
	c.log.Debug("github", logging.KeyOp, op, "status", resp.StatusCode, logging.KeyDurationMS, time.Since(start).Milliseconds(),
		logging.KeyGHRequestID, rate.RequestID, logging.KeyGQLRemaining, rate.Remaining)
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		_ = resp.Body.Close()
		c.log.Info("github timeout", logging.KeyOp, op, "status", resp.StatusCode)
		return nil, rate, ErrTimeout
	case http.StatusForbidden, http.StatusTooManyRequests:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		msg := message(body)
		if rl := rateLimit(resp, rate, msg); rl != nil {
			c.log.Info("github rate limit", logging.KeyOp, op, "primary", rl.Primary, logging.KeyWaitedMS, rl.Wait().Milliseconds())
			return nil, rate, rl
		}
		return nil, rate, &StatusError{Status: resp.StatusCode, Message: msg, SSO: resp.Header.Get("X-Github-Sso")}
	}
	return resp, rate, nil
}

// rateLimit reads a 403 or 429 as a rate limit, or returns nil for any
// other refusal.
func rateLimit(resp *http.Response, rate Rate, msg string) *RateLimitError {
	retry := time.Duration(0)
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
		retry = time.Duration(s) * time.Second
	}
	switch {
	case strings.Contains(strings.ToLower(msg), "secondary rate limit") || retry > 0:
		return &RateLimitError{RetryAfter: retry, Rate: rate}
	case rate.Known() && rate.Remaining == 0:
		return &RateLimitError{Primary: true, Reset: rate.Reset, Rate: rate}
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitError{Rate: rate}
	}
	return nil
}

func message(body []byte) string {
	var m struct{ Message string }
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		return m.Message
	}
	return strings.TrimSpace(string(body))
}

// graphql posts query and decodes its data into out, returning the errors
// GitHub put beside the data: a query answers with what it could read. An
// error not tied to a part of the query means the query as a whole
// failed, and is returned as the error.
func (c *Client) graphql(ctx context.Context, op, query string, out any) ([]gqlError, Rate, error) {
	body, _ := json.Marshal(map[string]any{"query": query})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.gqlURL, bytes.NewReader(body))
	if err != nil {
		return nil, Rate{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, rate, err := c.do(req, op)
	if err != nil {
		return nil, rate, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, rate, &StatusError{Status: resp.StatusCode, Message: message(b)}
	}
	var r struct {
		Data   json.RawMessage
		Errors []gqlError
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, rate, err
	}
	for _, e := range r.Errors {
		switch {
		case e.Type == "RATE_LIMITED":
			return nil, rate, &RateLimitError{Primary: true, Reset: rate.Reset, Rate: rate}
		case strings.Contains(strings.ToLower(e.Message), "secondary rate limit"):
			return nil, rate, &RateLimitError{Rate: rate}
		case len(e.Path) == 0:
			return nil, rate, &StatusError{Status: http.StatusOK, Message: e.Message}
		}
	}
	if len(r.Data) > 0 && string(r.Data) != "null" {
		if err := json.Unmarshal(r.Data, out); err != nil {
			return r.Errors, rate, err
		}
	}
	return r.Errors, rate, nil
}

type gqlError struct {
	Type    string
	Message string
	Path    []any
}
