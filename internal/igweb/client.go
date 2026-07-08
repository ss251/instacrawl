// Package igweb is the Go-native web Fetcher — the DEFAULT transport for the
// web lane. It uses net/http + a cookiejar seeded from the real browser session
// bundle, so nothing about a device is fabricated (that is the web lane's whole
// safety advantage over the mobile/instagrapi lane). It implements ig.Fetcher.
package igweb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/session"
)

// maxBodyBytes bounds how much of a response we read, so a hostile/huge body
// can't exhaust memory. A profile JSON is a few KB; 8 MiB is generous.
const maxBodyBytes = 8 << 20

// Client is the Go-native web fetcher.
type Client struct {
	httpClient *http.Client
	urlFor     func(dsUserID string) string
}

// Option configures a Client.
type Option func(*Client)

// WithTransport injects an http.RoundTripper, so tests can replay fixtures and
// assert the exact outbound request without hitting the network.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.httpClient.Transport = rt }
}

// WithURLBuilder overrides how the probe URL is built (tests only).
func WithURLBuilder(f func(string) string) Option {
	return func(c *Client) { c.urlFor = f }
}

// New builds a web client. The cookie jar uses the public-suffix list so
// instagram.com cookies are scoped correctly across subdomains.
func New(opts ...Option) (*Client, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	c := &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
		urlFor: ig.SelfProfileURL,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Name identifies this transport.
func (c *Client) Name() string { return "web" }

// FetchSelfProfile issues the read-only self-profile GET. An HTTP error status
// is returned inside RawResponse (not as a Go error) so the classifier judges
// it; only a transport failure (no response) returns a non-nil error.
func (c *Client) FetchSelfProfile(ctx context.Context, b *session.WebBundle) (ig.RawResponse, error) {
	if b == nil {
		return ig.RawResponse{}, fmt.Errorf("nil session bundle")
	}
	reqURL := c.urlFor(b.DSUserID)
	u, err := url.Parse(reqURL)
	if err != nil {
		return ig.RawResponse{}, fmt.Errorf("bad probe URL: %w", err)
	}
	c.seedCookies(u, b)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return ig.RawResponse{}, fmt.Errorf("build request: %w", err)
	}
	setHeaders(req, b)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ig.RawResponse{}, err // genuine transport failure
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return ig.RawResponse{}, fmt.Errorf("read response body: %w", err)
	}

	return ig.RawResponse{
		Status:      resp.StatusCode,
		ContentType: strings.ToLower(resp.Header.Get("Content-Type")),
		Location:    resp.Header.Get("Location"),
		WWWClaim:    resp.Header.Get("X-Ig-Set-Www-Claim"),
		RetryAfter:  resp.Header.Get("Retry-After"),
		Body:        body,
	}, nil
}

// seedCookies loads the bundle's cookies into the jar, scoped to instagram.com
// (so they cover www./i. subdomains alike).
func (c *Client) seedCookies(u *url.URL, b *session.WebBundle) {
	var cookies []*http.Cookie
	add := func(name, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		cookies = append(cookies, &http.Cookie{
			Name:   name,
			Value:  value,
			Domain: "instagram.com",
			Path:   "/",
		})
	}
	add("sessionid", b.SessionID)
	add("ds_user_id", b.DSUserID)
	add("csrftoken", b.CSRFToken)
	add("mid", b.Mid)
	add("rur", b.Rur)
	add("ig_did", b.IGDID)
	c.httpClient.Jar.SetCookies(u, cookies)
}

// setHeaders applies the required web-API headers. A mismatch between the
// User-Agent and X-IG-App-ID is a checkpoint trigger, so both come from the
// bundle and travel together.
func setHeaders(req *http.Request, b *session.WebBundle) {
	req.Header.Set("User-Agent", b.UserAgent)
	req.Header.Set("X-IG-App-ID", b.AppID)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", "https://www.instagram.com/")
	req.Header.Set("Accept", "*/*")
	if strings.TrimSpace(b.CSRFToken) != "" {
		req.Header.Set("X-CSRFToken", b.CSRFToken)
	}
}
