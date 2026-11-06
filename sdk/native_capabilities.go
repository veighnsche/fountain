package extensions

import (
	"context"
	"errors"
)

var (
	ErrRepositoryCapabilitiesQuery   = errors.New("invalid capability query")
	ErrRepositoryCapabilitiesChanged = errors.New("capabilities changed during read")
)

type RepositoryCapabilitiesRequest struct {
	Subject string `json:"subject"` // repository, issue, pull
	Index   string `json:"index"`
}

func ValidRepositoryCapabilitiesRequest(q RepositoryCapabilitiesRequest) bool {
	return q.Subject == "repository" && q.Index == "" || (q.Subject == "issue" || q.Subject == "pull") && positivePullID(q.Index)
}

type RepositoryActionCapability struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}
type RepositoryCapabilities struct {
	Head         string                                `json:"head"`
	Base         string                                `json:"base"`
	RepositoryID string                                `json:"repository_id"`
	ActorID      string                                `json:"actor_id"`
	Subject      string                                `json:"subject"`
	Index        string                                `json:"index"`
	ResourceID   string                                `json:"resource_id"`
	Actions      map[string]RepositoryActionCapability `json:"actions"`
	MergeMethods map[string]RepositoryActionCapability `json:"merge_methods,omitempty"`
}

var repositoryCapabilityActions = []string{"issues.read", "issue.create", "issue.edit", "issue.comment", "pulls.read", "pull.create", "pull.edit", "pull.comment", "pull.review", "pull.review.comment", "pull.review.approve", "pull.review.request_changes", "thread.resolve", "pull.merge", "checks.read", "checks.jobs.read"}

func validRepositoryCapabilities(p *RepositoryCapabilities, repo string, q RepositoryCapabilitiesRequest, actor string) bool {
	if p == nil || p.RepositoryID != repo || p.ActorID != actor || p.Subject != q.Subject || p.Index != q.Index || q.Subject == "repository" && p.ResourceID != "" || q.Subject != "repository" && !positivePullID(p.ResourceID) || len(p.Actions) != len(repositoryCapabilityActions) {
		return false
	}
	if q.Subject != "pull" && (p.Head != "" || p.Base != "") || p.Head != "" && !ValidPullDiffCommit(p.Head) || p.Base != "" && !ValidPullDiffCommit(p.Base) {
		return false
	}
	for _, action := range repositoryCapabilityActions {
		fact, ok := p.Actions[action]
		if !ok || len(fact.Reason) > 256 {
			return false
		}
		switch fact.State {
		case "allowed", "denied", "unsupported", "unavailable", "unknown":
		default:
			return false
		}
	}
	if p.MergeMethods != nil {
		if q.Subject != "pull" || len(p.MergeMethods) != 5 {
			return false
		}
		for _, method := range []string{"merge", "squash", "rebase", "rebase-merge", "fast-forward-only"} {
			fact, ok := p.MergeMethods[method]
			if !ok || len(fact.Reason) > 256 {
				return false
			}
			switch fact.State {
			case "allowed", "denied", "unsupported", "unavailable", "unknown":
			default:
				return false
			}
			if fact.State == "allowed" && p.Actions["pull.merge"].State != "allowed" {
				return false
			}
		}
	}
	return true
}
func (a Authority) RepositoryCapabilities(ctx context.Context, repo string, q RepositoryCapabilitiesRequest) (RepositoryCapabilities, error) {
	if !positivePullID(repo) || !ValidRepositoryCapabilitiesRequest(q) {
		return RepositoryCapabilities{}, ErrRepositoryCapabilitiesQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryCapabilities, RepositoryID: repo, Capabilities: &q})
	if err != nil {
		return RepositoryCapabilities{}, err
	}
	return *r.Capabilities, nil
}
