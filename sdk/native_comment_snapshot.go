package extensions

import (
	"context"
	"errors"
)

var (
	ErrCommentSnapshotQuery   = errors.New("invalid comment preparation query")
	ErrCommentSnapshotChanged = errors.New("comment preparation changed during read")
)

type RepositoryCommentSnapshotRequest struct {
	Subject string `json:"subject"`
	Index   string `json:"index"`
}

func ValidRepositoryCommentSnapshotRequest(q RepositoryCommentSnapshotRequest) bool {
	return (q.Subject == "issue" || q.Subject == "pull") && positivePullID(q.Index)
}

// RepositoryCommentSnapshot is an observation for preparing an ordinary human
// comment. It grants no authority and records no operation. The mutation owner
// must claim this revision and recheck the live admitted human at commit.
type RepositoryCommentSnapshot struct {
	RepositoryID   string                     `json:"repository_id"`
	ActorID        string                     `json:"actor_id"`
	Subject        string                     `json:"subject"`
	SubjectID      string                     `json:"subject_id"`
	IssueID        string                     `json:"issue_id"`
	Index          string                     `json:"index"`
	NativeRevision int64                      `json:"native_revision"`
	Action         RepositoryActionCapability `json:"action"`
}

func validRepositoryCommentSnapshot(p *RepositoryCommentSnapshot, repository, actor string, q RepositoryCommentSnapshotRequest) bool {
	return p != nil && p.RepositoryID == repository && p.ActorID == actor && p.Subject == q.Subject && p.Index == q.Index &&
		positivePullID(p.SubjectID) && positivePullID(p.IssueID) && (p.Subject != "issue" || p.SubjectID == p.IssueID) &&
		p.NativeRevision >= 0 && (p.Action.State == "allowed" || p.Action.State == "denied") && len(p.Action.Reason) <= 256
}

func (a Authority) RepositoryCommentSnapshot(ctx context.Context, repository string, q RepositoryCommentSnapshotRequest) (RepositoryCommentSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryCommentSnapshotRequest(q) {
		return RepositoryCommentSnapshot{}, ErrCommentSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryCommentSnapshot, RepositoryID: repository, CommentSnapshot: &q})
	if err != nil {
		return RepositoryCommentSnapshot{}, err
	}
	return *r.CommentSnapshot, nil
}
