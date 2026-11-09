package extensions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json" //nolint:depguard // Private native cursor requires strict standard JSON decoding.
	"io"
	"net/http"
	"strconv"
	"strings"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"xorm.io/builder"
)

type readableCatalogCursor struct {
	Instance string `json:"instance"`
	Actor    string `json:"actor"`
	Query    string `json:"query"`
	After    string `json:"after"`
}

func readableRepositoryCatalog(ctx context.Context, admission *callbackAdmission, q extension.RepositoryCatalogRequest) (*extension.RepositoryPage, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	after := int64(0)
	if q.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return nil, http.StatusBadRequest
		}
		var c readableCatalogCursor
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Instance != admission.instanceID || c.Actor != strconv.FormatInt(user.ID, 10) || c.Query != q.Query || mustParsePositiveID(c.After) == 0 {
			return nil, http.StatusBadRequest
		}
		after = mustParsePositiveID(c.After)
	}
	// Native access predicates include owned, collaboration, organisation-team
	// and public visibility. Administrators retain native all-repository access.
	accessibleUnit := func(kind unit.Type) builder.Cond {
		condition := builder.NewCond()
		if !user.IsAdmin {
			condition = repo_model.AccessibleRepositoryCondition(user, kind)
		}
		return condition.And(builder.In("repository.id", builder.Select("repo_id").From("repo_unit").Where(builder.Eq{"type": kind})))
	}
	condition := builder.NewCond()
	_, codeRead := admission.capabilities[extension.CapabilityRepositoryRead]
	_, issueRead := admission.capabilities[extension.CapabilityRepositoryIssuesRead]
	if codeRead {
		condition = accessibleUnit(unit.TypeCode)
	}
	if issueRead {
		if codeRead {
			condition = condition.Or(accessibleUnit(unit.TypeIssues))
		} else {
			condition = accessibleUnit(unit.TypeIssues)
		}
	}
	if !codeRead && !issueRead {
		return nil, http.StatusForbidden
	}
	condition = condition.And(builder.Gt{"repository.id": after})
	if query := strings.ToLower(strings.TrimSpace(q.Query)); query != "" {
		if owner, name, found := strings.Cut(query, "/"); found {
			condition = condition.And(builder.Like{"repository.owner_name", owner}, builder.Like{"repository.lower_name", name})
		} else {
			condition = condition.And(builder.Like{"repository.lower_name", query})
		}
	}
	var rows []*repo_model.Repository
	if err := db.GetEngine(ctx).Where(condition).Cols("id").Asc("id").Limit(q.Limit + 1).Find(&rows); err != nil {
		return nil, http.StatusServiceUnavailable
	}
	result := &extension.RepositoryPage{Items: []extension.Repository{}}
	more := len(rows) > q.Limit
	if more {
		rows = rows[:q.Limit]
	}
	for _, row := range rows {
		repository, status := resolveCollaborationRepository(ctx, admission, strconv.FormatInt(row.ID, 10))
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK {
			return nil, status
		}
		result.Items = append(result.Items, *repository)
	}
	// Re-resolve selected resources after assembly: no cached unit/permission
	// view may disclose a repository after its current access is withdrawn.
	for _, item := range result.Items {
		after, status := resolveCollaborationRepository(ctx, admission, item.ID)
		if status == http.StatusNotFound || status == http.StatusOK && *after != item {
			return nil, http.StatusConflict
		}
		if status != http.StatusOK {
			return nil, status
		}
	}
	current, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	if current.IsAdmin != user.IsAdmin || current.IsRestricted != user.IsRestricted {
		return nil, http.StatusConflict
	}
	if more {
		data, err := json.Marshal(readableCatalogCursor{Instance: admission.instanceID, Actor: strconv.FormatInt(user.ID, 10), Query: q.Query, After: strconv.FormatInt(rows[len(rows)-1].ID, 10)})
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		result.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return result, http.StatusOK
}
