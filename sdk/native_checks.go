package extensions

import (
	"context"
	"errors"
	"unicode/utf8"
)

var (
	ErrChecksQuery    = errors.New("invalid check query")
	ErrChecksChanged  = errors.New("checks or protection changed")
	ErrChecksTooLarge = errors.New("checks exceed read limit")
)

type RepositoryChecksRequest struct {
	Kind   string `json:"kind,omitempty"` // empty/status, job
	Index  string `json:"index"`
	Head   string `json:"head,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

func ValidRepositoryChecksRequest(q RepositoryChecksRequest) bool {
	return (q.Kind == "" || q.Kind == "status" || q.Kind == "job") && positivePullID(q.Index) && q.Limit > 0 && q.Limit <= 50 && len(q.Cursor) <= 4096 &&
		(q.Head == "" && q.Cursor == "" || ValidPullDiffCommit(q.Head))
}

// Only public PR requirement facts are projected, never administrative identity
// allowlists. This is not an actor-bound merge verdict or mutation grant.
type RepositoryProtection struct {
	Branch              string   `json:"branch"`
	Protected           bool     `json:"protected"`
	StatusChecks        bool     `json:"status_checks"`
	RequiredContexts    []string `json:"required_contexts"`
	RequiredApprovals   int64    `json:"required_approvals"`
	BlockRejected       bool     `json:"block_rejected"`
	BlockReviewRequests bool     `json:"block_review_requests"`
	RequireUpToDate     bool     `json:"require_up_to_date"`
	RequireSigned       bool     `json:"require_signed"`
	DismissStale        bool     `json:"dismiss_stale"`
	IgnoreStale         bool     `json:"ignore_stale"`
	ApplyToAdmins       bool     `json:"apply_to_admins"`
	ProtectedFiles      string   `json:"protected_files"`
	UnprotectedFiles    string   `json:"unprotected_files"`
}
type RepositoryCommitCheck struct {
	Job         *RepositoryCheckJob `json:"job"`
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	State       string              `json:"state"`
	Description string              `json:"description"`
	DetailsURL  string              `json:"details_url"`
	Required    *bool               `json:"required"`
}

// Payloads, task credentials and runner labels stay in the native owner.
type RepositoryCheckJob struct {
	RunID          string  `json:"run_id"`
	RunNumber      string  `json:"run_number"`
	Workflow       string  `json:"workflow"`
	Ref            string  `json:"ref"`
	Event          string  `json:"event"`
	TriggerEvent   string  `json:"trigger_event"`
	RunState       string  `json:"run_state"`
	NeedApproval   bool    `json:"need_approval"`
	WorkflowCommit string  `json:"workflow_commit"`
	JobID          string  `json:"job_id"`
	Attempt        string  `json:"attempt"`
	StartedAt      *string `json:"started_at"`
	StoppedAt      *string `json:"stopped_at"`
}
type RepositoryChecks struct {
	Kind            string                  `json:"kind"`
	RepositoryID    string                  `json:"repository_id"`
	PullID          string                  `json:"pull_id"`
	Index           string                  `json:"index"`
	Head            string                  `json:"head"`
	Revision        string                  `json:"revision"`
	Protection      *RepositoryProtection   `json:"protection"`
	MissingRequired []string                `json:"missing_required"`
	InvalidRequired []string                `json:"invalid_required"`
	StatusGate      string                  `json:"status_gate"`
	Items           []RepositoryCommitCheck `json:"items"`
	NextCursor      string                  `json:"next_cursor,omitempty"`
}

func ValidRepositoryChecks(p *RepositoryChecks, repository string, q RepositoryChecksRequest) bool {
	kind := q.Kind
	if kind == "" {
		kind = "status"
	}
	if p == nil || p.Kind != kind || p.RepositoryID != repository || !positivePullID(p.PullID) || p.Index != q.Index || !ValidPullDiffCommit(p.Head) || q.Head != "" && p.Head != q.Head || len(p.Revision) != 64 || p.Items == nil || len(p.Items) > q.Limit || p.MissingRequired == nil || p.InvalidRequired == nil || len(p.NextCursor) > 4096 || p.NextCursor != "" && len(p.Items) == 0 {
		return false
	}
	for _, c := range p.Revision {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	if r := p.Protection; r != nil {
		if r.Branch == "" || r.RequiredContexts == nil || r.RequiredApprovals < 0 || r.RequiredApprovals > 9007199254740991 {
			return false
		}
	}
	switch p.StatusGate {
	case "not_required", "pending", "success", "failure", "error", "warning", "unknown":
	default:
		return false
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, c := range p.Items {
		if !positivePullID(c.ID) || ids[c.ID] || kind == "status" && names[c.Name] || c.Name == "" || len(c.Name) > 4096 || !utf8.ValidString(c.Name) || len(c.State) > 64 || !utf8.ValidString(c.State) {
			return false
		}
		if kind == "job" {
			j := c.Job
			if j == nil || !positivePullID(j.RunID) || !positivePullID(j.RunNumber) || !(j.Attempt == "0" || positivePullID(j.Attempt)) || j.WorkflowCommit != "" && !ValidPullDiffCommit(j.WorkflowCommit) || c.Required != nil {
				return false
			}
		} else if c.Job != nil {
			return false
		}
		ids[c.ID] = true
		names[c.Name] = true
	}
	return true
}

func (a Authority) RepositoryChecks(ctx context.Context, repo string, q RepositoryChecksRequest) (RepositoryChecks, error) {
	if !positivePullID(repo) || !ValidRepositoryChecksRequest(q) {
		return RepositoryChecks{}, ErrChecksQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryChecks, RepositoryID: repo, Checks: &q})
	if err != nil {
		return RepositoryChecks{}, err
	}
	return *r.Checks, nil
}

func checksCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest: ErrChecksQuery,
		conflict:   ErrChecksChanged,
		tooLarge:   ErrChecksTooLarge,
		notFound:   ErrRepositoryNotVisible,
	}).forStatus(status)
}
