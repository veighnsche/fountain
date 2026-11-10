package extensions

import (
	"context"
	"errors"
	"unicode/utf8"
)

var (
	ErrPullFilesQuery    = errors.New("invalid pull files query")
	ErrPullFilesChanged  = errors.New("pull diff changed")
	ErrPullFilesTooLarge = errors.New("pull file list exceeds read limit")
)

// A continuation must carry the exact pair returned by the first page.
// Base is the PR's native diff base, not a mutable target branch name.
type RepositoryPullFilesRequest struct {
	Index  string `json:"index"`
	Base   string `json:"base,omitempty"`
	Head   string `json:"head,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

func ValidRepositoryPullFilesRequest(q RepositoryPullFilesRequest) bool {
	return positivePullID(q.Index) && q.Limit > 0 && q.Limit <= 10 && len(q.Cursor) <= 4096 &&
		(q.Base == "" && q.Head == "" && q.Cursor == "" || ValidPullDiffCommit(q.Base) && ValidPullDiffCommit(q.Head))
}

func ValidPullDiffCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type RepositoryPullFile struct {
	OldPath    string  `json:"old_path"`
	NewPath    string  `json:"new_path"`
	Change     string  `json:"change"`
	PatchState string  `json:"patch_state"`
	Patch      *string `json:"patch"`
	Additions  *int    `json:"additions"`
	Deletions  *int    `json:"deletions"`
}
type RepositoryPullFiles struct {
	RepositoryID string               `json:"repository_id"`
	PullID       string               `json:"pull_id"`
	Index        string               `json:"index"`
	Base         string               `json:"base"`
	Head         string               `json:"head"`
	Items        []RepositoryPullFile `json:"items"`
	NextCursor   string               `json:"next_cursor,omitempty"`
}

func ValidRepositoryPullFiles(p *RepositoryPullFiles, repository string, q RepositoryPullFilesRequest) bool {
	if p == nil || p.RepositoryID != repository || !positivePullID(p.PullID) || p.Index != q.Index || !ValidPullDiffCommit(p.Base) || !ValidPullDiffCommit(p.Head) || q.Base != "" && (p.Base != q.Base || p.Head != q.Head) || p.Items == nil || len(p.Items) > q.Limit || len(p.NextCursor) > 4096 || p.NextCursor != "" && len(p.Items) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, f := range p.Items {
		key := f.OldPath + "\x00" + f.NewPath
		if seen[key] || f.OldPath == "" && f.NewPath == "" || len(f.OldPath) > 4096 || len(f.NewPath) > 4096 || !utf8.ValidString(f.OldPath) || !utf8.ValidString(f.NewPath) || f.Additions != nil && *f.Additions < 0 || f.Deletions != nil && *f.Deletions < 0 {
			return false
		}
		seen[key] = true
		switch f.Change {
		case "added", "deleted", "modified", "renamed", "copied", "unknown":
		default:
			return false
		}
		switch f.PatchState {
		case "complete", "truncated":
			if f.Patch == nil || len(*f.Patch) > 16384 {
				return false
			}
		case "binary", "unavailable":
			if f.Patch != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (a Authority) RepositoryPullFiles(ctx context.Context, repository string, q RepositoryPullFilesRequest) (RepositoryPullFiles, error) {
	if !positivePullID(repository) || !ValidRepositoryPullFilesRequest(q) {
		return RepositoryPullFiles{}, errors.New("invalid pull files query")
	}
	result, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryPullFiles, RepositoryID: repository, PullFiles: &q})
	if err != nil {
		return RepositoryPullFiles{}, err
	}
	return *result.PullFiles, nil
}

func pullFilesCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest: ErrPullFilesQuery,
		conflict:   ErrPullFilesChanged,
		tooLarge:   ErrPullFilesTooLarge,
		notFound:   ErrRepositoryNotVisible,
	}).forStatus(status)
}
