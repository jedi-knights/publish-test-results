package publish

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
    "strconv"
    "time"
)

// Client is a thin GitHub Checks API driver. Only the two endpoints
// we use (POST /check-runs and PATCH /check-runs/{id}) are wrapped;
// anything else can be added when needed rather than pre-emptively.
type Client struct {
    HTTPClient *http.Client
    BaseURL    string // default: https://api.github.com
    Token      string
    Owner      string
    Repo       string
    UserAgent  string

    // MaxRetries caps how many times we retry a request on 429 or
    // 5xx. Zero means one attempt total.
    MaxRetries int
    // Now is the clock used for retry backoff. Overridable for tests.
    Now func() time.Time
    // Sleep is the delay function used between retries. Overridable
    // for tests — pass a no-op to eliminate wall-clock delays.
    Sleep func(time.Duration)
}

// NewClient constructs a Client with production defaults. Tests use
// the struct literal to inject their own HTTPClient and BaseURL.
func NewClient(token, owner, repo string) *Client {
    return &Client{
        HTTPClient: &http.Client{Timeout: 30 * time.Second},
        BaseURL:    "https://api.github.com",
        Token:      token,
        Owner:      owner,
        Repo:       repo,
        UserAgent:  "publish-test-results/0.1",
        MaxRetries: 3,
        Now:        time.Now,
        Sleep:      time.Sleep,
    }
}

// CreateCheckRun POSTs a new check-run. The response's ID is what
// UpdateCheckRun needs to append further annotations.
func (c *Client) CreateCheckRun(ctx context.Context, body CheckRunCreate) (*CheckRunResponse, error) {
    path := fmt.Sprintf("/repos/%s/%s/check-runs", c.Owner, c.Repo)
    return c.do(ctx, http.MethodPost, path, body)
}

// UpdateCheckRun PATCHes an existing check-run — used to append more
// annotations in batches of MaxAnnotationsPerRequest.
func (c *Client) UpdateCheckRun(ctx context.Context, id int64, body CheckRunUpdate) (*CheckRunResponse, error) {
    path := fmt.Sprintf("/repos/%s/%s/check-runs/%d", c.Owner, c.Repo, id)
    return c.do(ctx, http.MethodPatch, path, body)
}

// APIError carries the HTTP status and the server's response body so
// callers can decide whether to retry or surface it verbatim.
type APIError struct {
    Method     string
    URL        string
    StatusCode int
    Body       string
}

func (e *APIError) Error() string {
    return fmt.Sprintf("%s %s: %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*CheckRunResponse, error) {
    payload, err := json.Marshal(body)
    if err != nil {
        return nil, fmt.Errorf("marshal: %w", err)
    }

    url := c.BaseURL + path

    var last error
    for attempt := 0; attempt <= c.MaxRetries; attempt++ {
        if attempt > 0 {
            c.Sleep(backoffDuration(attempt))
        }
        req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
        if err != nil {
            return nil, fmt.Errorf("build request: %w", err)
        }
        req.Header.Set("Accept", "application/vnd.github+json")
        req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Authorization", "Bearer "+c.Token)
        if c.UserAgent != "" {
            req.Header.Set("User-Agent", c.UserAgent)
        }

        resp, err := c.HTTPClient.Do(req)
        if err != nil {
            last = err
            if !shouldRetryTransport(err) {
                return nil, fmt.Errorf("%s %s: %w", method, url, err)
            }
            continue
        }

        respBody, _ := io.ReadAll(resp.Body)
        _ = resp.Body.Close()

        switch {
        case resp.StatusCode >= 200 && resp.StatusCode < 300:
            var out CheckRunResponse
            if err := json.Unmarshal(respBody, &out); err != nil {
                return nil, fmt.Errorf("decode response: %w", err)
            }
            return &out, nil
        case shouldRetryStatus(resp.StatusCode):
            last = &APIError{Method: method, URL: url, StatusCode: resp.StatusCode, Body: string(respBody)}
            if wait := parseRetryAfter(resp.Header.Get("Retry-After")); wait > 0 {
                c.Sleep(wait)
            }
            continue
        default:
            return nil, &APIError{
                Method:     method,
                URL:        url,
                StatusCode: resp.StatusCode,
                Body:       string(respBody),
            }
        }
    }
    if last == nil {
        last = errors.New("no attempts made")
    }
    return nil, fmt.Errorf("giving up after %d attempts: %w", c.MaxRetries+1, last)
}

// backoffDuration returns exponential backoff with a modest cap.
// attempt=1 → 500ms, attempt=2 → 1s, attempt=3 → 2s, capped at 10s.
func backoffDuration(attempt int) time.Duration {
    const base = 500 * time.Millisecond
    d := base << (attempt - 1)
    if d > 10*time.Second {
        d = 10 * time.Second
    }
    return d
}

func shouldRetryStatus(code int) bool {
    return code == http.StatusTooManyRequests || (code >= 500 && code < 600)
}

func shouldRetryTransport(err error) bool {
    // net.Error's Timeout / Temporary would be nice here but many
    // transport errors are worth retrying too. Retry on any non-nil
    // transport error; caller controls MaxRetries.
    return err != nil
}

// parseRetryAfter accepts either a seconds count or an HTTP date, but
// only the seconds form is common in GitHub's responses. Returns 0
// for unparseable values so the caller falls back to exponential
// backoff.
func parseRetryAfter(header string) time.Duration {
    if header == "" {
        return 0
    }
    seconds, err := strconv.Atoi(header)
    if err != nil || seconds < 0 {
        return 0
    }
    return time.Duration(seconds) * time.Second
}
