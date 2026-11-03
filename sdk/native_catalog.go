package extensions

import (
	"context"
	"errors"
	"strconv"
	"unicode/utf8"
)

var (
	ErrRepositoryCatalogQuery   = errors.New("invalid repository catalogue query")
	ErrRepositoryCatalogChanged = errors.New("repository catalogue changed during read")
)

type RepositoryCatalogRequest struct {
	Query  string `json:"query"`
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

func ValidRepositoryCatalogRequest(q RepositoryCatalogRequest) bool {
	return q.Limit >= 1 && q.Limit <= 50 && len(q.Query) <= 256 && utf8.ValidString(q.Query) && len(q.Cursor) <= 4096 && utf8.ValidString(q.Cursor)
}

func validRepositoryCatalog(p *RepositoryPage, q RepositoryCatalogRequest) bool {
	if p == nil || p.Items == nil || len(p.Items) > q.Limit || len(p.NextCursor) > 4096 {
		return false
	}
	last := int64(0)
	for _, r := range p.Items {
		id, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil || !positivePullID(r.ID) || id <= last || r.Owner == "" || r.Name == "" || len(r.Owner) > 255 || len(r.Name) > 255 || !utf8.ValidString(r.Owner) || !utf8.ValidString(r.Name) {
			return false
		}
		switch r.Permission {
		case "read", "write", "admin":
		default:
			return false
		}
		last = id
	}
	return true
}

func (a Authority) ReadableRepositories(ctx context.Context, q RepositoryCatalogRequest) (RepositoryPage, error) {
	if !ValidRepositoryCatalogRequest(q) {
		return RepositoryPage{}, ErrRepositoryCatalogQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationReadableRepositories, Catalog: &q})
	if err != nil {
		return RepositoryPage{}, err
	}
	return *r.Page, nil
}
