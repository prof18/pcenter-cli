package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	storetypes "github.com/prof18/pcenter-cli/internal/store/types"
)

var (
	tokenBackoff     = Backoff{Base: 2 * time.Second, Cap: 30 * time.Second, Attempts: 4}
	transportBackoff = Backoff{Base: 5 * time.Second, Cap: 60 * time.Second, Attempts: 4}
)

// Clock makes all retry sleeping deterministic under test.
type Clock interface {
	Sleep(context.Context, time.Duration) error
	Now() time.Time
}

// ClientOptions configures a Partner Center client.
type ClientOptions struct {
	APIBase       string
	LoginBase     string
	TenantID      string
	ClientID      string
	ClientSecret  string
	HTTPClient    *http.Client
	Clock         Clock
	Rand          Rand
	CorrelationID string
	VerboseLog    func(string)
}

// Client implements authenticated Partner Center JSON requests.
type Client struct {
	apiBase       string
	loginBase     string
	tenantID      string
	clientID      string
	clientSecret  string
	httpClient    *http.Client
	clock         Clock
	rand          Rand
	correlationID string
	verboseLog    func(string)
	redactor      Redactor

	tokenMu sync.Mutex
	token   string
}

// APIError is returned for non-successful HTTP responses.
type APIError struct {
	Method        string
	URL           string
	StatusCode    int
	Body          string
	CorrelationID string
	RetryAfter    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Partner Center %s %s failed with HTTP %d (correlation %s): %s", e.Method, e.URL, e.StatusCode, e.CorrelationID, e.Body)
}

// NewClient validates options and constructs a client.
func NewClient(options ClientOptions) (*Client, error) {
	for name, value := range map[string]string{
		"API base": options.APIBase, "login base": options.LoginBase, "tenant id": options.TenantID,
		"client id": options.ClientID, "client secret": options.ClientSecret, "correlation id": options.CorrelationID,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required", name)
		}
	}
	if options.HTTPClient == nil {
		options.HTTPClient = http.DefaultClient
	}
	if options.Clock == nil {
		options.Clock = realClock{}
	}
	if options.Rand == nil {
		options.Rand = defaultRand{}
	}
	return &Client{
		apiBase:       strings.TrimRight(options.APIBase, "/"),
		loginBase:     strings.TrimRight(options.LoginBase, "/"),
		tenantID:      options.TenantID,
		clientID:      options.ClientID,
		clientSecret:  options.ClientSecret,
		httpClient:    options.HTTPClient,
		clock:         options.Clock,
		rand:          options.Rand,
		correlationID: options.CorrelationID,
		verboseLog:    options.VerboseLog,
		redactor:      NewRedactor(options.ClientSecret),
	}, nil
}

// Application gets an application resource.
func (c *Client) Application(ctx context.Context, appID string) (storetypes.Application, error) {
	var result storetypes.Application
	err := c.getJSON(ctx, "/applications/"+url.PathEscape(appID), &result)
	return result, err
}

// Submission gets a submission and preserves its complete raw JSON.
func (c *Client) Submission(ctx context.Context, appID, submissionID string) (storetypes.Submission, error) {
	path := "/applications/" + url.PathEscape(appID) + "/submissions/" + url.PathEscape(submissionID)
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return storetypes.Submission{}, err
	}
	var result storetypes.Submission
	if err := json.Unmarshal(body, &result); err != nil {
		return storetypes.Submission{}, fmt.Errorf("decode submission: %w", err)
	}
	result.Raw = append(json.RawMessage(nil), body...)
	return result, nil
}

// SubmissionStatus gets the dedicated submission status resource.
func (c *Client) SubmissionStatus(ctx context.Context, appID, submissionID string) (storetypes.SubmissionStatus, error) {
	var result storetypes.SubmissionStatus
	path := "/applications/" + url.PathEscape(appID) + "/submissions/" + url.PathEscape(submissionID) + "/status"
	err := c.getJSON(ctx, path, &result)
	return result, err
}

// Rollout gets package rollout state.
func (c *Client) Rollout(ctx context.Context, appID, submissionID string) (storetypes.Rollout, error) {
	var result storetypes.Rollout
	path := "/applications/" + url.PathEscape(appID) + "/submissions/" + url.PathEscape(submissionID) + "/packagerollout"
	err := c.getJSON(ctx, path, &result)
	return result, err
}

// ReviewQuery contains documented reviews endpoint filters.
type ReviewQuery struct {
	ApplicationID string
	StartDate     string
	EndDate       string
	Top           int
	Skip          int
	Filter        string
	OrderBy       string
}

// DoJSON performs a raw Store API request. Flow packages use it for endpoint-specific mutations.
func (c *Client) DoJSON(ctx context.Context, method, path string, body json.RawMessage) (json.RawMessage, error) {
	result, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(result), nil
}

// Reviews gets one reviews page.
func (c *Client) Reviews(ctx context.Context, query ReviewQuery) (storetypes.ReviewPage, error) {
	values := url.Values{}
	values.Set("applicationId", query.ApplicationID)
	if query.StartDate != "" {
		values.Set("startDate", query.StartDate)
	}
	if query.EndDate != "" {
		values.Set("endDate", query.EndDate)
	}
	if query.Top > 0 {
		values.Set("top", strconv.Itoa(query.Top))
	}
	if query.Skip >= 0 {
		values.Set("skip", strconv.Itoa(query.Skip))
	}
	if query.Filter != "" {
		values.Set("filter", query.Filter)
	}
	if query.OrderBy != "" {
		values.Set("orderby", query.OrderBy)
	}
	var result storetypes.ReviewPage
	err := c.getJSON(ctx, "/analytics/reviews?"+values.Encode(), &result)
	return result, err
}

// ReviewsNext follows a relative or absolute @nextLink.
func (c *Client) ReviewsNext(ctx context.Context, nextLink string) (storetypes.ReviewPage, error) {
	var result storetypes.ReviewPage
	err := c.getJSON(ctx, nextLink, &result)
	return result, err
}

func (c *Client) getJSON(ctx context.Context, path string, destination any) error {
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("decode Partner Center response: %w", err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	requestURL := c.resolveURL(path)
	maxAttempts := 1
	if method == http.MethodGet || method == http.MethodPut {
		maxAttempts = transportBackoff.Attempts
	}
	refreshed := false
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		statusCode, responseHeader, responseBody, err := c.send(ctx, method, requestURL, body, token)
		if err == nil && statusCode >= 200 && statusCode < 300 {
			return responseBody, nil
		}
		if err == nil && statusCode == http.StatusUnauthorized && !refreshed {
			c.clearToken()
			refreshed = true
			attempt--
			continue
		}
		transient := err != nil || isTransientStatus(statusCode)
		if transient && attempt < maxAttempts {
			delay := transportBackoff.Delay(attempt, c.rand)
			if statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable {
				if retryAfter, ok := transportBackoff.RetryAfter(responseHeader.Get("Retry-After"), c.clock.Now()); ok {
					delay = retryAfter
				}
			}
			if sleepErr := c.clock.Sleep(ctx, delay); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("request to Partner Center with %s %s failed (correlation %s): %w", method, c.redactor.Redact(requestURL), c.correlationID, err)
		}
		return nil, &APIError{
			Method: method, URL: c.redactor.Redact(requestURL), StatusCode: statusCode,
			Body: c.redactor.Redact(string(responseBody)), CorrelationID: c.correlationID,
			RetryAfter: responseHeader.Get("Retry-After"),
		}
	}
	return nil, errors.New("request attempts exhausted")
}

func (c *Client) send(ctx context.Context, method, requestURL string, body []byte, token string) (int, http.Header, []byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	var reader io.Reader
	if body != nil || method != http.MethodGet {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(requestCtx, method, requestURL, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("MS-CorrelationId", c.correlationID)
	if method != http.MethodGet || body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.verboseLog != nil {
		c.verboseLog(c.redactor.Redact(method + " " + requestURL))
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		_ = response.Body.Close()
		return response.StatusCode, response.Header.Clone(), nil, readErr
	}
	if closeErr := response.Body.Close(); closeErr != nil {
		return response.StatusCode, response.Header.Clone(), nil, closeErr
	}
	return response.StatusCode, response.Header.Clone(), responseBody, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"resource":      {"https://manage.devcenter.microsoft.com"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	tokenURL := c.loginBase + "/" + url.PathEscape(c.tenantID) + "/oauth2/token"
	for attempt := 1; attempt <= tokenBackoff.Attempts; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			cancel()
			return "", err
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, requestErr := c.httpClient.Do(request)
		var responseBody []byte
		if requestErr == nil {
			responseBody, err = io.ReadAll(response.Body)
			if closeErr := response.Body.Close(); err == nil {
				err = closeErr
			}
		}
		cancel()
		if requestErr == nil && err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
			var tokenResponse struct {
				AccessToken string `json:"access_token"`
			}
			if decodeErr := json.Unmarshal(responseBody, &tokenResponse); decodeErr != nil {
				return "", fmt.Errorf("decode token response: %w", decodeErr)
			}
			if tokenResponse.AccessToken == "" {
				return "", errors.New("token response did not contain access_token")
			}
			c.token = tokenResponse.AccessToken
			c.redactor = NewRedactor(c.clientSecret, c.token)
			return c.token, nil
		}
		transient := requestErr != nil || (response != nil && isTransientStatus(response.StatusCode))
		if transient && attempt < tokenBackoff.Attempts {
			delay := tokenBackoff.Delay(attempt, c.rand)
			if response != nil && (response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable) {
				if retryAfter, ok := tokenBackoff.RetryAfter(response.Header.Get("Retry-After"), c.clock.Now()); ok {
					delay = retryAfter
				}
			}
			if sleepErr := c.clock.Sleep(ctx, delay); sleepErr != nil {
				return "", sleepErr
			}
			continue
		}
		if requestErr != nil {
			return "", fmt.Errorf("token request failed: %w", requestErr)
		}
		if err != nil {
			return "", fmt.Errorf("read token response: %w", err)
		}
		return "", fmt.Errorf("token request failed with HTTP %d: %s", response.StatusCode, c.redactor.Redact(string(responseBody)))
	}
	return "", errors.New("token attempts exhausted")
}

func (c *Client) clearToken() {
	c.tokenMu.Lock()
	c.token = ""
	c.tokenMu.Unlock()
}

func (c *Client) resolveURL(path string) string {
	if parsed, err := url.Parse(path); err == nil && parsed.IsAbs() {
		return path
	}
	if strings.HasPrefix(path, "/v1.0/my/") {
		base, err := url.Parse(c.apiBase)
		if err == nil {
			return base.Scheme + "://" + base.Host + path
		}
	}
	return c.apiBase + "/" + strings.TrimLeft(path, "/")
}

func isTransientStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

type realClock struct{}

func (realClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (realClock) Now() time.Time { return time.Now() }

type defaultRand struct{}

func (defaultRand) Float64() float64 { return rand.Float64() }
