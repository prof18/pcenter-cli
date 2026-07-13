// Package fakestore provides a stateful in-process Partner Center server for tests.
package fakestore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// SubmissionRef is the submission summary embedded in an application.
type SubmissionRef struct {
	ID            string          `json:"id"`
	Status        string          `json:"status,omitempty"`
	StatusDetails json.RawMessage `json:"statusDetails,omitempty"`
}

// App models the fields of an application used by pcenter.
type App struct {
	ID                                 string         `json:"id"`
	Name                               string         `json:"name,omitempty"`
	LastPublishedApplicationSubmission *SubmissionRef `json:"lastPublishedApplicationSubmission,omitempty"`
	PendingApplicationSubmission       *SubmissionRef `json:"pendingApplicationSubmission,omitempty"`
}

// Rollout models package rollout state.
type Rollout struct {
	IsPackageRollout         bool    `json:"isPackageRollout"`
	PackageRolloutPercentage float64 `json:"packageRolloutPercentage"`
	PackageRolloutStatus     string  `json:"packageRolloutStatus"`
	FallbackSubmissionID     string  `json:"fallbackSubmissionId,omitempty"`
}

// ReviewPage is one response page from the reviews endpoint.
type ReviewPage struct {
	Value      []json.RawMessage `json:"Value"`
	TotalCount int               `json:"TotalCount"`
}

// Failure injects Count responses before normal endpoint handling resumes.
type Failure struct {
	Method     string
	Path       string
	Status     int
	Body       string
	RetryAfter string
	Count      int
}

// Response adds a simple canned route after failure injection has run.
type Response struct {
	Method string
	Path   string
	Status int
	Body   string
}

// Options defines initial fake state and scripted failures.
type Options struct {
	AppID       string
	App         App
	Submissions map[string]json.RawMessage
	Rollouts    map[string]Rollout
	ReviewPages []ReviewPage
	Failures    []Failure
	Responses   []Response
	AccessToken string
}

// Request is a sanitized journal entry. It records presence, never credential values.
type Request struct {
	Method               string
	Path                 string
	Query                string
	Body                 json.RawMessage
	AuthorizationPresent bool
	CorrelationIDPresent bool
	ContentType          string
}

// Server is an httptest implementation of the Partner Center surface used by pcenter.
type Server struct {
	mu       sync.Mutex
	server   *httptest.Server
	options  Options
	failures []Failure
	journal  []Request
}

// New starts a fake Store server and registers cleanup with t.
func New(t testing.TB, options Options) *Server {
	t.Helper()
	if options.AccessToken == "" {
		options.AccessToken = "fake-access-token"
	}
	s := &Server{options: options, failures: append([]Failure(nil), options.Failures...)}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

// APIBase is suitable for PCENTER_API_BASE.
func (s *Server) APIBase() string { return s.server.URL + "/v1.0/my" }

// LoginBase is suitable for PCENTER_LOGIN_BASE.
func (s *Server) LoginBase() string { return s.server.URL }

// Journal returns a snapshot of received requests.
func (s *Server) Journal() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Request, len(s.journal))
	copy(result, s.journal)
	return result
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.record(r, body)
	if s.injectFailure(w, r) {
		return
	}

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/token"):
		writeJSON(w, http.StatusOK, map[string]any{"access_token": s.options.AccessToken, "token_type": "Bearer", "expires_in": 3600})
	case r.Method == http.MethodGet && r.URL.Path == "/v1.0/my/applications/"+s.options.AppID:
		writeJSON(w, http.StatusOK, s.options.App)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.0/my/applications/"+s.options.AppID+"/submissions/"):
		s.handleSubmissionGET(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1.0/my/analytics/reviews":
		s.handleReviews(w, r)
	default:
		if !s.handleCannedResponse(w, r) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "fake endpoint not found"})
		}
	}
}

func (s *Server) record(r *http.Request, body []byte) {
	entry := Request{
		Method:               r.Method,
		Path:                 r.URL.Path,
		Query:                r.URL.RawQuery,
		AuthorizationPresent: r.Header.Get("Authorization") != "",
		CorrelationIDPresent: r.Header.Get("MS-CorrelationId") != "",
		ContentType:          r.Header.Get("Content-Type"),
	}
	if len(body) > 0 {
		entry.Body = append(json.RawMessage(nil), body...)
	}
	s.mu.Lock()
	s.journal = append(s.journal, entry)
	s.mu.Unlock()
}

func (s *Server) handleCannedResponse(w http.ResponseWriter, r *http.Request) bool {
	for _, response := range s.options.Responses {
		if response.Method == r.Method && response.Path == r.URL.Path {
			status := response.Status
			if status == 0 {
				status = http.StatusOK
			}
			body := response.Body
			if body == "" {
				body = `{}`
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return true
		}
	}
	return false
}

func (s *Server) injectFailure(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.failures {
		failure := &s.failures[index]
		if failure.Count > 0 && failure.Method == r.Method && failure.Path == r.URL.Path {
			failure.Count--
			if failure.RetryAfter != "" {
				w.Header().Set("Retry-After", failure.RetryAfter)
			}
			body := failure.Body
			if body == "" {
				body = fmt.Sprintf(`{"error":"injected failure","status":%d}`, failure.Status)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(failure.Status)
			_, _ = io.WriteString(w, body)
			return true
		}
	}
	return false
}

func (s *Server) handleSubmissionGET(w http.ResponseWriter, r *http.Request) {
	prefix := "/v1.0/my/applications/" + s.options.AppID + "/submissions/"
	remainder := strings.TrimPrefix(r.URL.Path, prefix)
	if strings.HasSuffix(remainder, "/packagerollout") {
		id := strings.TrimSuffix(remainder, "/packagerollout")
		rollout, ok := s.options.Rollouts[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "rollout not found"})
			return
		}
		writeJSON(w, http.StatusOK, rollout)
		return
	}
	submission, ok := s.options.Submissions[remainder]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "submission not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(submission)
}

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	if len(s.options.ReviewPages) == 0 {
		writeJSON(w, http.StatusOK, ReviewPage{Value: []json.RawMessage{}, TotalCount: 0})
		return
	}
	page := 0
	if rawSkip := r.URL.Query().Get("skip"); rawSkip != "" {
		if parsed, err := strconv.Atoi(rawSkip); err == nil {
			page = parsed
		}
	}
	if page < 0 || page >= len(s.options.ReviewPages) {
		writeJSON(w, http.StatusOK, ReviewPage{Value: []json.RawMessage{}, TotalCount: s.options.ReviewPages[0].TotalCount})
		return
	}
	payload := map[string]any{
		"Value":      s.options.ReviewPages[page].Value,
		"TotalCount": s.options.ReviewPages[page].TotalCount,
	}
	if page+1 < len(s.options.ReviewPages) {
		payload["@nextLink"] = fmt.Sprintf("/v1.0/my/analytics/reviews?applicationId=%s&skip=%d", s.options.AppID, page+1)
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
