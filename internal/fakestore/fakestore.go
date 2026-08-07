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
	"time"
)

// SubmissionRef is the submission summary embedded in an application.
//
// Only an id and a resource location, matching the live API — it deliberately
// carries no status. Modelling a status here previously let the CLI render a
// STATUS column that could only ever be blank against the real Store.
type SubmissionRef struct {
	ID               string `json:"id"`
	ResourceLocation string `json:"resourceLocation,omitempty"`
}

// App models the fields of an application used by pcenter, with the field
// names the live API really uses (captured from FeedFlow on 2026-08-06).
type App struct {
	ID                                 string         `json:"id"`
	PrimaryName                        string         `json:"primaryName,omitempty"`
	PackageFamilyName                  string         `json:"packageFamilyName,omitempty"`
	PackageIdentityName                string         `json:"packageIdentityName,omitempty"`
	PublisherName                      string         `json:"publisherName,omitempty"`
	FirstPublishedDate                 string         `json:"firstPublishedDate,omitempty"`
	HasAdvancedListingPermission       bool           `json:"hasAdvancedListingPermission,omitempty"`
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

// MutationScenario scripts failures for a state-changing endpoint.
// ApplyOnFailure models the gateway returning an error after the operation succeeded.
type MutationScenario struct {
	Failures       int
	Status         int
	ApplyOnFailure bool
}

// Options defines initial fake state and scripted failures.
type Options struct {
	AppID               string
	App                 App
	Submissions         map[string]json.RawMessage
	Rollouts            map[string]Rollout
	ReviewPages         []ReviewPage
	Failures            []Failure
	Responses           []Response
	AccessToken         string
	CreateSubmissionID  string
	CreateSubmission    json.RawMessage
	CommitStatuses      []string
	CommitStatusDetails json.RawMessage
	StatusQueues        map[string][]string
	FinalizeScenario    MutationScenario
	CreateScenario      MutationScenario
	CommitScenario      MutationScenario
	DeleteScenario      MutationScenario
	BlobEnabled         bool
	BlobForbiddenCount  int
	// RejectToken makes the token endpoint answer as Azure AD does for a bad
	// client secret: 400 invalid_client, which is permanent and must not retry.
	RejectToken bool
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
	mu             sync.Mutex
	server         *httptest.Server
	options        Options
	failures       []Failure
	journal        []Request
	app            App
	submissions    map[string]json.RawMessage
	rollouts       map[string]Rollout
	statusQueue    map[string][]string
	finalize       MutationScenario
	create         MutationScenario
	commit         MutationScenario
	deleteDraft    MutationScenario
	blobForbidden  int
	blobGeneration int
	blobUploads    [][]byte
}

// New starts a fake Store server and registers cleanup with t.
func New(t testing.TB, options Options) *Server {
	t.Helper()
	if options.AccessToken == "" {
		options.AccessToken = "fake-access-token"
	}
	s := &Server{
		options:        options,
		failures:       append([]Failure(nil), options.Failures...),
		app:            options.App,
		submissions:    cloneRawMessages(options.Submissions),
		rollouts:       cloneRollouts(options.Rollouts),
		statusQueue:    cloneStatusQueues(options.StatusQueues),
		finalize:       options.FinalizeScenario,
		create:         options.CreateScenario,
		commit:         options.CommitScenario,
		deleteDraft:    options.DeleteScenario,
		blobForbidden:  options.BlobForbiddenCount,
		blobGeneration: 1,
	}
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

// SetFailures replaces the remaining generic failure script.
func (s *Server) SetFailures(failures []Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append([]Failure(nil), failures...)
}

// SetStatusQueue scripts status endpoint responses for an existing submission.
func (s *Server) SetStatusQueue(submissionID string, statuses []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusQueue[submissionID] = append([]string(nil), statuses...)
}

// BlobUploads returns captured successful upload request bodies.
func (s *Server) BlobUploads() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([][]byte, len(s.blobUploads))
	for index, upload := range s.blobUploads {
		result[index] = append([]byte(nil), upload...)
	}
	return result
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.record(r, body)
	if s.injectFailure(w, r) {
		return
	}

	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/blob/upload.zip":
		s.handleBlobUpload(w, body)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/token"):
		if s.options.RejectToken {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":             "invalid_client",
				"error_description": "AADSTS7000215: Invalid client secret provided.",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": s.options.AccessToken, "token_type": "Bearer", "expires_in": 3600})
	case r.Method == http.MethodGet && r.URL.Path == "/v1.0/my/applications/"+s.options.AppID:
		s.mu.Lock()
		app := s.app
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, app)
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/my/applications/"+s.options.AppID+"/submissions":
		s.handleCreate(w)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1.0/my/applications/"+s.options.AppID+"/submissions/"):
		s.handleDelete(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1.0/my/applications/"+s.options.AppID+"/submissions/"):
		s.handleSubmissionPOST(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1.0/my/applications/"+s.options.AppID+"/submissions/"):
		s.handleSubmissionPUT(w, r, body)
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
		s.mu.Lock()
		rollout, ok := s.rollouts[id]
		s.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "rollout not found"})
			return
		}
		writeJSON(w, http.StatusOK, rollout)
		return
	}
	if strings.HasSuffix(remainder, "/status") {
		s.handleSubmissionStatus(w, strings.TrimSuffix(remainder, "/status"))
		return
	}
	s.mu.Lock()
	submission, ok := s.submissions[remainder]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "submission not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(submission)
}

func (s *Server) handleSubmissionStatus(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.submissions[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "submission not found"})
		return
	}
	status := submissionStatus(raw)
	if queue := s.statusQueue[id]; len(queue) > 0 {
		status = queue[0]
		if len(queue) > 1 {
			s.statusQueue[id] = queue[1:]
		}
	}
	// The real endpoint returns the submission's own statusDetails; the option
	// is only an override for scripted commit scenarios.
	details := s.options.CommitStatusDetails
	if len(details) == 0 {
		details = submissionStatusDetails(raw)
	}
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "statusDetails": details})
}

func (s *Server) handleCreate(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.app.PendingApplicationSubmission != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "pending submission exists"})
		return
	}
	apply := func() {
		id := s.options.CreateSubmissionID
		if id == "" {
			id = "created"
		}
		raw := append(json.RawMessage(nil), s.options.CreateSubmission...)
		if len(raw) == 0 {
			raw = json.RawMessage(fmt.Sprintf(`{"id":%q,"status":"PendingCommit","listings":{}}`, id))
		}
		raw = withRawField(raw, "id", id)
		raw = withRawField(raw, "status", "PendingCommit")
		if s.options.BlobEnabled {
			raw = withRawField(raw, "fileUploadUrl", s.blobURL())
		}
		s.submissions[id] = raw
		s.app.PendingApplicationSubmission = &SubmissionRef{ID: id}
	}
	if status, failed := applyScenario(&s.create, apply); failed {
		writeMutationFailure(w, status)
		return
	}
	apply()
	id := s.app.PendingApplicationSubmission.ID
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.submissions[id])
}

func (s *Server) handleSubmissionPUT(w http.ResponseWriter, r *http.Request, body []byte) {
	prefix := "/v1.0/my/applications/" + s.options.AppID + "/submissions/"
	id := strings.TrimPrefix(r.URL.Path, prefix)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.submissions[id]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "submission not found"})
		return
	}
	if !json.Valid(body) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	updated := append(json.RawMessage(nil), body...)
	updated = withRawField(updated, "id", id)
	updated = withRawField(updated, "status", "PendingCommit")
	if s.options.BlobEnabled {
		updated = withRawField(updated, "fileUploadUrl", s.blobURL())
	}
	s.submissions[id] = updated
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(updated)
}

func (s *Server) handleBlobUpload(w http.ResponseWriter, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blobForbidden > 0 {
		s.blobForbidden--
		s.blobGeneration++
		if pending := s.app.PendingApplicationSubmission; pending != nil {
			if raw, ok := s.submissions[pending.ID]; ok {
				s.submissions[pending.ID] = withRawField(raw, "fileUploadUrl", s.blobURL())
			}
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "expired SAS"})
		return
	}
	s.blobUploads = append(s.blobUploads, append([]byte(nil), body...))
	w.Header().Set("ETag", `"fake-etag"`)
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) blobURL() string {
	return fmt.Sprintf("%s/blob/upload.zip?sv=1&sig=fake+signature+%d", s.server.URL, s.blobGeneration)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	prefix := "/v1.0/my/applications/" + s.options.AppID + "/submissions/"
	id := strings.TrimPrefix(r.URL.Path, prefix)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.app.PendingApplicationSubmission == nil || s.app.PendingApplicationSubmission.ID != id {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "draft not found"})
		return
	}
	apply := func() {
		delete(s.submissions, id)
		delete(s.statusQueue, id)
		s.app.PendingApplicationSubmission = nil
	}
	if status, failed := applyScenario(&s.deleteDraft, apply); failed {
		writeMutationFailure(w, status)
		return
	}
	apply()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSubmissionPOST(w http.ResponseWriter, r *http.Request) {
	prefix := "/v1.0/my/applications/" + s.options.AppID + "/submissions/"
	remainder := strings.TrimPrefix(r.URL.Path, prefix)
	operationIndex := strings.LastIndexByte(remainder, '/')
	if operationIndex < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "operation not found"})
		return
	}
	id, operation := remainder[:operationIndex], remainder[operationIndex+1:]
	switch operation {
	case "finalizepackagerollout":
		s.handleFinalize(w, id)
	case "commit":
		s.handleCommit(w, id)
	case "updatepackagerolloutpercentage":
		s.handleSetPercentage(w, r, id)
	case "haltpackagerollout":
		s.handleHalt(w, id)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "operation not found"})
	}
}

func (s *Server) handleFinalize(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rollout, ok := s.rollouts[id]
	if !ok || rollout.PackageRolloutStatus != "PackageRolloutInProgress" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "rollout is not in progress"})
		return
	}
	apply := func() {
		rollout.IsPackageRollout = false
		rollout.PackageRolloutPercentage = 100
		rollout.PackageRolloutStatus = "PackageRolloutCompleted"
		s.rollouts[id] = rollout
	}
	if status, failed := applyScenario(&s.finalize, apply); failed {
		writeMutationFailure(w, status)
		return
	}
	apply()
	writeJSON(w, http.StatusOK, s.rollouts[id])
}

func (s *Server) handleCommit(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.submissions[id]
	if !ok || submissionStatus(raw) != "PendingCommit" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "submission is not pending commit"})
		return
	}
	apply := func() {
		statuses := append([]string(nil), s.options.CommitStatuses...)
		if len(statuses) == 0 {
			statuses = []string{"CommitStarted"}
		}
		s.statusQueue[id] = statuses
		s.submissions[id] = withSubmissionStatus(raw, statuses[0])
	}
	if status, failed := applyScenario(&s.commit, apply); failed {
		writeMutationFailure(w, status)
		return
	}
	apply()
	writeJSON(w, http.StatusOK, map[string]string{"status": firstStatus(s.options.CommitStatuses)})
}

func (s *Server) handleSetPercentage(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rollout, ok := s.rollouts[id]
	if !ok || rollout.PackageRolloutStatus != "PackageRolloutInProgress" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "rollout is not in progress"})
		return
	}
	percentage, err := strconv.ParseFloat(r.URL.Query().Get("percentage"), 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid percentage"})
		return
	}
	rollout.PackageRolloutPercentage = percentage
	s.rollouts[id] = rollout
	writeJSON(w, http.StatusOK, rollout)
}

func (s *Server) handleHalt(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rollout, ok := s.rollouts[id]
	if !ok || rollout.PackageRolloutStatus != "PackageRolloutInProgress" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "rollout is not in progress"})
		return
	}
	rollout.PackageRolloutPercentage = 0
	rollout.PackageRolloutStatus = "PackageRolloutStopped"
	s.rollouts[id] = rollout
	writeJSON(w, http.StatusOK, rollout)
}

func applyScenario(scenario *MutationScenario, apply func()) (int, bool) {
	if scenario.Failures <= 0 {
		return 0, false
	}
	scenario.Failures--
	if scenario.ApplyOnFailure {
		apply()
	}
	status := scenario.Status
	if status == 0 {
		status = http.StatusGatewayTimeout
	}
	return status, true
}

func writeMutationFailure(w http.ResponseWriter, status int) {
	writeJSON(w, status, map[string]string{"error": "injected mutation failure"})
}

func submissionStatus(raw json.RawMessage) string {
	var value struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.Status
}

func submissionStatusDetails(raw json.RawMessage) json.RawMessage {
	var value struct {
		StatusDetails json.RawMessage `json:"statusDetails"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.StatusDetails
}

func withSubmissionStatus(raw json.RawMessage, status string) json.RawMessage {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	value["status"] = status
	updated, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return updated
}

func withRawField(raw json.RawMessage, field, value string) json.RawMessage {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return raw
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	object[field] = encoded
	updated, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return updated
}

func firstStatus(statuses []string) string {
	if len(statuses) == 0 {
		return "CommitStarted"
	}
	return statuses[0]
}

func cloneRawMessages(source map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		result[key] = append(json.RawMessage(nil), value...)
	}
	return result
}

func cloneRollouts(source map[string]Rollout) map[string]Rollout {
	result := make(map[string]Rollout, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneStatusQueues(source map[string][]string) map[string][]string {
	result := make(map[string][]string, len(source))
	for key, value := range source {
		result[key] = append([]string(nil), value...)
	}
	return result
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
