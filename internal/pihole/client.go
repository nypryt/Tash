// Package pihole talks to Pi-hole v6 (preferred) and legacy v5 APIs.
package pihole

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/version"
)

// Summary is the Pi-hole overview block.
type Summary struct {
	TotalQueries   int64     `json:"total_queries"`
	Blocked        int64     `json:"blocked"`
	PercentBlocked float64   `json:"percent_blocked"`
	UniqueDomains  int64     `json:"unique_domains"`
	Forwarded      int64     `json:"forwarded"`
	Cached         int64     `json:"cached"`
	ActiveClients  int       `json:"active_clients"`
	TotalClients   int       `json:"total_clients"`
	GravityDomains int64     `json:"gravity_domains"`
	GravityUpdated time.Time `json:"gravity_updated,omitempty"`
	Frequency      float64   `json:"frequency"`
}

// Query is one DNS query.
type Query struct {
	At        time.Time `json:"at"`
	Type      string    `json:"type"`
	Domain    string    `json:"domain"`
	Client    string    `json:"client"`
	Status    string    `json:"status"`
	Upstream  string    `json:"upstream,omitempty"`
	Blocked   bool      `json:"blocked"`
	ReplySecs float64   `json:"reply_seconds,omitempty"`
}

// TopItem is a top-domain row.
type TopItem struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// Snapshot is everything the dashboard needs from Pi-hole.
type Snapshot struct {
	Configured bool      `json:"configured"`
	Reachable  bool      `json:"reachable"`
	URL        string    `json:"url,omitempty"`
	Version    int       `json:"version,omitempty"`
	Summary    Summary   `json:"summary"`
	Queries    []Query   `json:"queries"`
	TopBlocked []TopItem `json:"top_blocked"`
	Blocking   string    `json:"blocking"` // enabled, disabled, unknown
	Error      string    `json:"error,omitempty"`
	QueryError string    `json:"query_error,omitempty"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// Client is a concurrency-safe Pi-hole API client.
type Client struct {
	cfg  config.PiHoleConfig
	base string
	http *http.Client

	mu        sync.Mutex
	version   int
	sid       string
	csrf      string
	sidExpiry time.Time
}

// New validates cfg and creates a client.
func New(cfg config.PiHoleConfig) (*Client, error) {
	base, err := normalizeBase(cfg.URL)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.SkipTLSVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- explicit user opt-in for self-signed Pi-hole certs
	}
	return &Client{
		cfg: cfg, base: base,
		http: &http.Client{Transport: tr, Timeout: 20 * time.Second},
	}, nil
}

// BaseURL returns the normalised Pi-hole root URL.
func (c *Client) BaseURL() string { return c.base }

func normalizeBase(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("Pi-hole URL is empty")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid Pi-hole URL %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/admin")
	u.Path = strings.TrimSuffix(u.Path, "/api")
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// Test forces version detection and fetches a summary.
func (c *Client) Test(ctx context.Context) (Snapshot, error) {
	s := c.Fetch(ctx)
	if s.Error != "" {
		return s, fmt.Errorf("%s", s.Error)
	}
	return s, nil
}

// Fetch retrieves the overview, recent queries, and top blocked domains.
func (c *Client) Fetch(ctx context.Context) Snapshot {
	s := Snapshot{Configured: c.cfg.Enabled && c.cfg.URL != "", URL: c.base, FetchedAt: time.Now()}
	if !s.Configured {
		return s
	}
	if err := c.detect(ctx); err != nil {
		s.Error = err.Error()
		return s
	}
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	s.Version = v
	s.Reachable = true
	if v == 6 {
		c.fetchV6(ctx, &s)
	} else {
		c.fetchV5(ctx, &s)
	}
	s.FetchedAt = time.Now()
	return s
}

func (c *Client) detect(ctx context.Context) error {
	c.mu.Lock()
	if c.version != 0 {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	if c.cfg.APIVersion == 5 {
		c.setVersion(5)
		return nil
	}
	if c.cfg.APIVersion == 6 {
		c.setVersion(6)
		return nil
	}
	// Auto: try v6 first. A missing /api/auth or /api/stats/summary means v5.
	c.setVersion(6)
	if _, err := c.v6Get(ctx, "/api/stats/summary"); err == nil {
		return nil
	} else if isNotFound(err) {
		c.setVersion(5)
		return nil
	} else {
		// A 401 is a real v6 auth failure, not v5. Network errors are real too.
		c.setVersion(0)
		return err
	}
}

func (c *Client) setVersion(v int) {
	c.mu.Lock()
	c.version = v
	c.sid = ""
	c.sidExpiry = time.Time{}
	c.mu.Unlock()
}

func (c *Client) fetchV6(ctx context.Context, s *Snapshot) {
	var raw struct {
		Queries struct {
			Total          int64   `json:"total"`
			Blocked        int64   `json:"blocked"`
			PercentBlocked float64 `json:"percent_blocked"`
			UniqueDomains  int64   `json:"unique_domains"`
			Forwarded      int64   `json:"forwarded"`
			Cached         int64   `json:"cached"`
			Frequency      float64 `json:"frequency"`
		} `json:"queries"`
		Clients struct {
			Active int `json:"active"`
			Total  int `json:"total"`
		} `json:"clients"`
		Gravity struct {
			DomainsBeingBlocked int64 `json:"domains_being_blocked"`
			LastUpdate          int64 `json:"last_update"`
		} `json:"gravity"`
	}
	if err := c.v6JSON(ctx, http.MethodGet, "/api/stats/summary", nil, &raw); err != nil {
		s.Error = err.Error()
		return
	}
	s.Summary = Summary{
		TotalQueries: raw.Queries.Total, Blocked: raw.Queries.Blocked,
		PercentBlocked: raw.Queries.PercentBlocked, UniqueDomains: raw.Queries.UniqueDomains,
		Forwarded: raw.Queries.Forwarded, Cached: raw.Queries.Cached, Frequency: raw.Queries.Frequency,
		ActiveClients: raw.Clients.Active, TotalClients: raw.Clients.Total,
		GravityDomains: raw.Gravity.DomainsBeingBlocked,
	}
	if raw.Gravity.LastUpdate > 0 {
		s.Summary.GravityUpdated = time.Unix(raw.Gravity.LastUpdate, 0)
	}
	var blocking struct {
		Blocking string `json:"blocking"`
	}
	if err := c.v6JSON(ctx, http.MethodGet, "/api/dns/blocking", nil, &blocking); err == nil && blocking.Blocking != "" {
		s.Blocking = blocking.Blocking
	} else if blocking.Blocking == "" {
		s.Blocking = "unknown"
	}

	var qresp struct {
		Queries []struct {
			Time   float64 `json:"time"`
			Type   string  `json:"type"`
			Domain string  `json:"domain"`
			Status string  `json:"status"`
			Client struct {
				IP   string `json:"ip"`
				Name string `json:"name"`
			} `json:"client"`
			Upstream string `json:"upstream"`
			Reply    struct {
				Time float64 `json:"time"`
			} `json:"reply"`
		} `json:"queries"`
	}
	if err := c.v6JSON(ctx, http.MethodGet, "/api/queries?length=150", nil, &qresp); err != nil {
		s.QueryError = err.Error()
	} else {
		for _, q := range qresp.Queries {
			client := q.Client.Name
			if client == "" {
				client = q.Client.IP
			}
			at := time.Now()
			if q.Time > 0 {
				sec := int64(q.Time)
				nsec := int64((q.Time - float64(sec)) * 1e9)
				at = time.Unix(sec, nsec)
			}
			s.Queries = append(s.Queries, Query{
				At: at, Type: q.Type, Domain: q.Domain, Client: client, Status: q.Status,
				Upstream: q.Upstream, Blocked: blockedStatusV6(q.Status), ReplySecs: q.Reply.Time,
			})
		}
		sort.SliceStable(s.Queries, func(i, j int) bool { return s.Queries[i].At.After(s.Queries[j].At) })
	}
	var top struct {
		Domains []struct {
			Domain string `json:"domain"`
			Count  int64  `json:"count"`
		} `json:"domains"`
	}
	if err := c.v6JSON(ctx, http.MethodGet, "/api/stats/top_domains?blocked=true&count=10", nil, &top); err == nil {
		for _, d := range top.Domains {
			s.TopBlocked = append(s.TopBlocked, TopItem{Name: d.Domain, Count: d.Count})
		}
	}
	if len(s.TopBlocked) == 0 {
		s.TopBlocked = aggregateBlocked(s.Queries, 10)
	}
}

func (c *Client) fetchV5(ctx context.Context, s *Snapshot) {
	var raw map[string]json.RawMessage
	if err := c.v5JSON(ctx, "summaryRaw&topItems=10", &raw); err != nil {
		s.Error = err.Error()
		return
	}
	s.Summary = Summary{
		TotalQueries:   jsonInt(raw, "dns_queries_today"),
		Blocked:        jsonInt(raw, "ads_blocked_today"),
		PercentBlocked: jsonFloat(raw, "ads_percentage_today"),
		UniqueDomains:  jsonInt(raw, "unique_domains"),
		Forwarded:      jsonInt(raw, "queries_forwarded"),
		Cached:         jsonInt(raw, "queries_cached"),
		ActiveClients:  int(jsonInt(raw, "unique_clients")),
		TotalClients:   int(jsonInt(raw, "clients_ever_seen")),
		GravityDomains: jsonInt(raw, "domains_being_blocked"),
	}
	if st := jsonString(raw, "status"); st != "" {
		s.Blocking = st
	} else {
		s.Blocking = "unknown"
	}
	if top, ok := raw["top_ads"]; ok {
		var m map[string]int64
		if json.Unmarshal(top, &m) == nil {
			for k, v := range m {
				s.TopBlocked = append(s.TopBlocked, TopItem{Name: k, Count: v})
			}
			sort.Slice(s.TopBlocked, func(i, j int) bool { return s.TopBlocked[i].Count > s.TopBlocked[j].Count })
			if len(s.TopBlocked) > 10 {
				s.TopBlocked = s.TopBlocked[:10]
			}
		}
	}
	var qresp struct {
		Data [][]json.RawMessage `json:"data"`
	}
	if err := c.v5JSON(ctx, "getAllQueries", &qresp); err != nil {
		s.QueryError = err.Error()
	} else {
		for _, row := range qresp.Data {
			if len(row) < 5 {
				continue
			}
			var ts int64
			var qtype, domain, client, status string
			var upstream string
			_ = json.Unmarshal(row[0], &ts)
			_ = json.Unmarshal(row[1], &qtype)
			_ = json.Unmarshal(row[2], &domain)
			_ = json.Unmarshal(row[3], &client)
			_ = json.Unmarshal(row[4], &status)
			if len(row) > 5 {
				_ = json.Unmarshal(row[5], &upstream)
			}
			code, _ := strconv.Atoi(status)
			s.Queries = append(s.Queries, Query{
				At: time.Unix(ts, 0), Type: qtype, Domain: domain, Client: client,
				Status: statusLabelV5(code), Upstream: upstream, Blocked: blockedCodeV5(code),
			})
		}
		sort.SliceStable(s.Queries, func(i, j int) bool { return s.Queries[i].At.After(s.Queries[j].At) })
		if len(s.Queries) > 200 {
			s.Queries = s.Queries[:200]
		}
	}
	if len(s.TopBlocked) == 0 {
		s.TopBlocked = aggregateBlocked(s.Queries, 10)
	}
}

type apiError struct {
	Code int
	Msg  string
}

func (e *apiError) Error() string { return e.Msg }

func isNotFound(err error) bool {
	var ae *apiError
	return errorsAs(err, &ae) && ae.Code == http.StatusNotFound
}

func errorsAs(err error, target **apiError) bool {
	for err != nil {
		if e, ok := err.(*apiError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func (c *Client) ensureV6(ctx context.Context) error {
	c.mu.Lock()
	if c.sid != "" && time.Now().Before(c.sidExpiry) {
		c.mu.Unlock()
		return nil
	}
	password := c.cfg.Secret
	c.mu.Unlock()
	if password == "" {
		return nil
	}
	body := map[string]any{"password": password}
	var resp struct {
		Session struct {
			Valid    bool    `json:"valid"`
			SID      string  `json:"sid"`
			CSRF     string  `json:"csrf"`
			Validity float64 `json:"validity"`
		} `json:"session"`
	}
	if err := c.rawJSON(ctx, http.MethodPost, c.base+"/api/auth", body, &resp, false); err != nil {
		return err
	}
	if !resp.Session.Valid || resp.Session.SID == "" {
		return fmt.Errorf("Pi-hole authentication failed")
	}
	validity := time.Duration(resp.Session.Validity * float64(time.Second))
	if validity < time.Minute {
		validity = 5 * time.Minute
	}
	c.mu.Lock()
	c.sid = resp.Session.SID
	c.csrf = resp.Session.CSRF
	c.sidExpiry = time.Now().Add(validity - 30*time.Second)
	c.mu.Unlock()
	return nil
}

func (c *Client) v6JSON(ctx context.Context, method, path string, in, out any) error {
	if err := c.ensureV6(ctx); err != nil {
		return err
	}
	err := c.v6Once(ctx, method, path, in, out, true)
	if err != nil && isUnauthorized(err) {
		c.mu.Lock()
		c.sid = ""
		c.sidExpiry = time.Time{}
		c.mu.Unlock()
		if err := c.ensureV6(ctx); err != nil {
			return err
		}
		err = c.v6Once(ctx, method, path, in, out, true)
	}
	return err
}

func (c *Client) v6Once(ctx context.Context, method, path string, in, out any, auth bool) error {
	return c.rawJSON(ctx, method, c.base+path, in, out, auth)
}

func (c *Client) v6Get(ctx context.Context, path string) ([]byte, error) {
	if err := c.ensureV6(ctx); err != nil {
		return nil, err
	}
	return c.rawBytes(ctx, http.MethodGet, c.base+path, nil)
}

func (c *Client) rawJSON(ctx context.Context, method, fullURL string, in, out any, auth bool) error {
	data, err := c.rawBytes(ctx, method, fullURL, in)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("Pi-hole returned invalid JSON: %w", err)
	}
	return nil
}

func (c *Client) rawBytes(ctx context.Context, method, fullURL string, in any) ([]byte, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.mu.Lock()
	sid, csrf := c.sid, c.csrf
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set("X-FTL-SID", sid)
		if csrf != "" {
			req.Header.Set("X-FTL-CSRF", csrf)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Pi-hole at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if resp.StatusCode == http.StatusUnauthorized {
			msg = "Pi-hole authentication failed or the API session expired"
		}
		if len(msg) > 240 {
			msg = msg[:240]
		}
		return nil, &apiError{Code: resp.StatusCode, Msg: fmt.Sprintf("Pi-hole: %s (%s)", msg, resp.Status)}
	}
	return data, nil
}

func isUnauthorized(err error) bool {
	var ae *apiError
	return errorsAs(err, &ae) && ae.Code == http.StatusUnauthorized
}

func (c *Client) v5JSON(ctx context.Context, query string, out any) error {
	q := strings.TrimLeft(query, "?&")
	full := c.base + "/admin/api.php?" + q
	if c.cfg.Secret != "" {
		full += "&auth=" + url.QueryEscape(c.cfg.Secret)
	}
	return c.rawJSON(ctx, http.MethodGet, full, nil, out, false)
}

// AddBlock adds a blocklist URL, an exact domain, or a regex (prefixed
// `regex:`). URLs trigger a gravity update on v6.
func (c *Client) AddBlock(ctx context.Context, entry string) (string, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", fmt.Errorf("nothing to add")
	}
	if err := c.detect(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	kind := "exact"
	value := entry
	isList := false
	if strings.HasPrefix(strings.ToLower(entry), "regex:") {
		kind = "regex"
		value = strings.TrimSpace(entry[len("regex:"):])
	} else if u, err := url.Parse(entry); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		isList = true
	}
	if value == "" {
		return "", fmt.Errorf("empty domain or list URL")
	}
	if v == 6 {
		if isList {
			body := map[string]any{
				"address": value, "type": "block", "comment": "Added from Tash dashboard",
				"groups": []int{0}, "enabled": true,
			}
			if err := c.v6JSON(ctx, http.MethodPost, "/api/lists", body, nil); err != nil {
				return "", err
			}
			out, err := c.RunGravity(ctx)
			if err != nil {
				return "blocklist added; gravity update failed: " + err.Error(), nil
			}
			return "blocklist added; gravity: " + firstLine(out), nil
		}
		body := map[string]any{
			"domain": value, "comment": "Added from Tash dashboard",
			"groups": []int{0}, "enabled": true,
		}
		path := "/api/domains/deny/" + kind
		if err := c.v6JSON(ctx, http.MethodPost, path, body, nil); err != nil {
			return "", err
		}
		if kind == "regex" {
			return "regex denylist entry added for " + value, nil
		}
		return "exact denylist entry added for " + value, nil
	}
	// v5 supports list=black&add=... for lists and exact domains.
	if kind == "regex" {
		return "", fmt.Errorf("Pi-hole v5 regex entries must be added in its web UI")
	}
	q := "list=black&add=" + url.QueryEscape(value)
	if err := c.v5JSON(ctx, q, nil); err != nil {
		return "", err
	}
	if isList {
		return "blocklist added to Pi-hole v5; run `pihole -g` on the Pi-hole host to apply it", nil
	}
	return "exact denylist entry added", nil
}

// AddAllow adds an allowlist URL, an exact domain, or a regex (prefixed
// `regex:`). URLs trigger a gravity update on v6.
func (c *Client) AddAllow(ctx context.Context, entry string) (string, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", fmt.Errorf("nothing to add")
	}
	if err := c.detect(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	kind := "exact"
	value := entry
	isList := false
	if strings.HasPrefix(strings.ToLower(entry), "regex:") {
		kind = "regex"
		value = strings.TrimSpace(entry[len("regex:"):])
	} else if u, err := url.Parse(entry); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		isList = true
	}
	if value == "" {
		return "", fmt.Errorf("empty domain or list URL")
	}
	if v == 6 {
		if isList {
			body := map[string]any{
				"address": value, "type": "allow", "comment": "Added from Tash dashboard",
				"groups": []int{0}, "enabled": true,
			}
			if err := c.v6JSON(ctx, http.MethodPost, "/api/lists", body, nil); err != nil {
				return "", err
			}
			out, err := c.RunGravity(ctx)
			if err != nil {
				return "allowlist added; gravity update failed: " + err.Error(), nil
			}
			return "allowlist added; gravity: " + firstLine(out), nil
		}
		body := map[string]any{
			"domain": value, "comment": "Added from Tash dashboard",
			"groups": []int{0}, "enabled": true,
		}
		path := "/api/domains/allow/" + kind
		if err := c.v6JSON(ctx, http.MethodPost, path, body, nil); err != nil {
			return "", err
		}
		if kind == "regex" {
			return "regex allowlist entry added for " + value, nil
		}
		return "exact allowlist entry added for " + value, nil
	}
	if kind == "regex" {
		return "", fmt.Errorf("Pi-hole v5 regex entries must be added in its web UI")
	}
	q := "list=white&add=" + url.QueryEscape(value)
	if err := c.v5JSON(ctx, q, nil); err != nil {
		return "", err
	}
	if isList {
		return "allowlist added to Pi-hole v5; run `pihole -g` on the Pi-hole host to apply it", nil
	}
	return "exact allowlist entry added", nil
}

// RunGravity triggers a Pi-hole v6 gravity update.
func (c *Client) RunGravity(ctx context.Context) (string, error) {
	if err := c.detect(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	if v != 6 {
		return "", fmt.Errorf("gravity API requires Pi-hole v6; run `pihole -g` on the host")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/action/gravity?color=false", nil)
	if err != nil {
		return "", err
	}
	if err := c.ensureV6(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	req.Header.Set("X-FTL-SID", c.sid)
	if c.csrf != "" {
		req.Header.Set("X-FTL-CSRF", c.csrf)
	}
	c.mu.Unlock()
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gravity update failed: %s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}

// SetBlocking enables or disables Pi-hole blocking.
func (c *Client) SetBlocking(ctx context.Context, enabled bool) error {
	if err := c.detect(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	if v == 6 {
		return c.v6JSON(ctx, http.MethodPost, "/api/dns/blocking", map[string]any{"blocking": enabled, "timer": nil}, nil)
	}
	q := "disable="
	if enabled {
		q += "0"
	} else {
		q += "1"
	}
	return c.v5JSON(ctx, q, nil)
}

func blockedStatusV6(status string) bool {
	s := strings.ToUpper(status)
	switch s {
	case "GRAVITY", "REGEX", "DENIED", "BLOCKED", "EXTERNAL_BLOCKED_IP",
		"EXTERNAL_BLOCKED_NULL", "EXTERNAL_BLOCKED_NXRA", "GRAVITY_CNAME",
		"REGEX_CNAME", "DENIED_CNAME", "BLOCKED_CNAME":
		return true
	}
	return strings.Contains(s, "BLOCK") || strings.Contains(s, "GRAVITY") || strings.Contains(s, "DENIED")
}

func blockedCodeV5(code int) bool {
	switch code {
	case 1, 4, 5, 6, 7, 8, 9, 10, 11:
		return true
	}
	return false
}

func statusLabelV5(code int) string {
	m := map[int]string{
		1: "GRAVITY", 2: "FORWARDED", 3: "CACHE", 4: "REGEX", 5: "DENIED",
		6: "EXTERNAL_BLOCKED_IP", 7: "EXTERNAL_BLOCKED_NULL", 8: "EXTERNAL_BLOCKED_NXRA",
		9: "GRAVITY_CNAME", 10: "REGEX_CNAME", 11: "DENIED_CNAME", 12: "RETRIED",
		13: "RETRIED_IGNORED", 14: "ALREADY_FORWARDED", 15: "DATABASE_BUSY",
	}
	if s, ok := m[code]; ok {
		return s
	}
	return "UNKNOWN"
}

func aggregateBlocked(queries []Query, n int) []TopItem {
	counts := map[string]int64{}
	for _, q := range queries {
		if q.Blocked {
			counts[q.Domain]++
		}
	}
	out := make([]TopItem, 0, len(counts))
	for k, v := range counts {
		out = append(out, TopItem{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Name < out[j].Name
		}
		return out[i].Count > out[j].Count
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	if s == "" {
		return "updated"
	}
	return s
}

func jsonInt(m map[string]json.RawMessage, key string) int64 {
	raw, ok := m[key]
	if !ok {
		return 0
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int64(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseFloat(s, 64)
		return int64(v)
	}
	return 0
}

func jsonFloat(m map[string]json.RawMessage, key string) float64 {
	raw, ok := m[key]
	if !ok {
		return 0
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	return 0
}

func jsonString(m map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := m[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}
