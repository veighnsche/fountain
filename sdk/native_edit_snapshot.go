package extensions

import (
	"context"
	"errors"
)

const OperationRepositoryEditSnapshot = "repository.edit.snapshot"

var (
	ErrEditSnapshotQuery   = errors.New("invalid edit preparation query")
	ErrEditSnapshotChanged = errors.New("edit preparation changed during read")
)

type RepositoryEditSnapshotRequest struct {
	Subject string `json:"subject"`
	Index   string `json:"index"`
}

func ValidRepositoryEditSnapshotRequest(q RepositoryEditSnapshotRequest) bool {
	return (q.Subject == "issue" || q.Subject == "pull") && positivePullID(q.Index)
}

// RepositoryEditSnapshot prepares title/description editing only. BodyDigest is
// SHA-256 of the exact stored UTF-8 Markdown; ContentVersion is the native body
// CAS version. NativeRevision is an idle observation, not a write grant. Dispatch
// must claim that revision and recheck live human, parent and edit conditions.
type RepositoryEditSnapshot struct {
	RepositoryID   string                     `json:"repository_id"`
	ActorID        string                     `json:"actor_id"`
	Subject        string                     `json:"subject"`
	SubjectID      string                     `json:"subject_id"`
	IssueID        string                     `json:"issue_id"`
	Index          string                     `json:"index"`
	Title          string                     `json:"title"`
	BodyDigest     string                     `json:"body_digest"`
	ContentVersion int64                      `json:"content_version"`
	NativeRevision int64                      `json:"native_revision"`
	TitleAction    RepositoryActionCapability `json:"title_action"`
	BodyAction     RepositoryActionCapability `json:"body_action"`
}

func validRepositoryEditSnapshot(p *RepositoryEditSnapshot, repository, actor string, q RepositoryEditSnapshotRequest) bool {
	if p == nil || p.RepositoryID != repository || p.ActorID != actor || p.Subject != q.Subject || p.Index != q.Index || !positivePullID(p.SubjectID) || !positivePullID(p.IssueID) || p.Subject == "issue" && p.SubjectID != p.IssueID || p.ContentVersion < 0 || p.NativeRevision < 0 || len(p.BodyDigest) != 64 {
		return false
	}
	for _, action := range []RepositoryActionCapability{p.TitleAction, p.BodyAction} {
		if action.State != "allowed" && action.State != "denied" || len(action.Reason) > 256 {
			return false
		}
	}
	for _, c := range p.BodyDigest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (a Authority) RepositoryEditSnapshot(ctx context.Context, repository string, q RepositoryEditSnapshotRequest) (RepositoryEditSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryEditSnapshotRequest(q) {
		return RepositoryEditSnapshot{}, ErrEditSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryEditSnapshot, RepositoryID: repository, EditSnapshot: &q})
	if err != nil {
		return RepositoryEditSnapshot{}, err
	}
	return *r.EditSnapshot, nil
}
