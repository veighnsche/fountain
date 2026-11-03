// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// IssueGraphPayload changes one repository's native issue graph. New issues
// have request-local keys; existing issues use their native database ID. The
// host interprets no accepted-intent, split, consolidation or spending policy.
// Those belong to the extension. All changes commit together or none do.
type IssueGraphPayload struct {
	Intake       *IssueIntakeAuthorization `json:"intake,omitempty"`
	Creates      []IssueGraphCreate        `json:"creates,omitempty"`
	Edits        []IssueGraphEdit          `json:"edits,omitempty"`
	Dependencies []IssueGraphDependency    `json:"dependencies,omitempty"`
}

type IssueGraphCreate struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// IssueGraphEdit changes exact native requirements while retaining the issue,
// author, history and status. Expected values reject accidental stale edits.
type IssueGraphEdit struct {
	IssueID                int64  `json:"issue_id"`
	ExpectedTitle          string `json:"expected_title"`
	ExpectedBody           string `json:"expected_body"`
	ExpectedContentVersion int    `json:"expected_content_version"`
	Title                  string `json:"title"`
	Body                   string `json:"body"`
}

// IssueGraphRef names exactly one existing issue or a request-local new key.
type IssueGraphRef struct {
	IssueID int64  `json:"issue_id,omitempty"`
	Key     string `json:"key,omitempty"`
}

// IssueGraphDependency is an exact edge add or remove. An add requires absence;
// a remove requires presence. Issue depends on Prerequisite.
type IssueGraphDependency struct {
	Issue        IssueGraphRef `json:"issue"`
	Prerequisite IssueGraphRef `json:"prerequisite"`
	Remove       bool          `json:"remove,omitempty"`
}

const (
	MaxIssueGraphChanges   = 64
	MaxIssueGraphBodyBytes = 64 << 10
)

// MaxIssueGraphPayloadBytes leaves room for the authorization envelope within
// the existing 64 KiB private callback request limit.
const MaxIssueGraphPayloadBytes = 48 << 10

var ErrInvalidIssueGraphPayload = errors.New("invalid issue graph payload")

func ValidateIssueGraphPayload(p IssueGraphPayload) error {
	if a := p.Intake; a != nil {
		if !ValidIssueIntakeRequest(IssueIntakeAuthorizationRequest{Source: a.Claims.Source, Revision: a.Claims.Revision, Digest: a.Claims.Digest}) || a.Claims.Installation == "" || !decimalID(a.Claims.Actor) || !decimalID(a.Claims.Repository) || a.Claims.Expires <= 0 || !intakeDigest.MatchString(a.RequestHash) || a.Slot < 0 || a.Slot >= 16 || a.Step < 0 || a.Step >= 32 || len(p.Creates)+len(p.Edits) != 1 || len(p.Dependencies) != 0 {
			return ErrInvalidIssueGraphPayload
		}
	}
	n := len(p.Creates) + len(p.Edits) + len(p.Dependencies)
	if n == 0 || n > MaxIssueGraphChanges {
		return ErrInvalidIssueGraphPayload
	}
	validText := func(title, body string) bool {
		return strings.TrimSpace(title) == title && title != "" && len(title) <= 255 && len(body) <= MaxIssueGraphBodyBytes && utf8.ValidString(title) && utf8.ValidString(body) && !strings.ContainsRune(title, 0) && !strings.ContainsRune(body, 0)
	}
	keys := map[string]bool{}
	for _, c := range p.Creates {
		if c.Key == "" || len(c.Key) > 64 || keys[c.Key] || !validText(c.Title, c.Body) {
			return ErrInvalidIssueGraphPayload
		}
		for _, ch := range c.Key {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return ErrInvalidIssueGraphPayload
			}
		}
		keys[c.Key] = true
	}
	edits := map[int64]bool{}
	for _, e := range p.Edits {
		if e.IssueID <= 0 || edits[e.IssueID] || e.ExpectedContentVersion < 0 || !validText(e.Title, e.Body) || len(e.ExpectedTitle) > 255 || len(e.ExpectedBody) > MaxIssueGraphBodyBytes {
			return ErrInvalidIssueGraphPayload
		}
		edits[e.IssueID] = true
	}
	ref := func(r IssueGraphRef) string {
		if r.IssueID > 0 && r.Key == "" {
			return "id:" + strconv.FormatInt(r.IssueID, 10)
		}
		if r.IssueID == 0 && keys[r.Key] {
			return "key:" + r.Key
		}
		return ""
	}
	edges := map[string]bool{}
	for _, e := range p.Dependencies {
		a, b := ref(e.Issue), ref(e.Prerequisite)
		if a == "" || b == "" || a == b || edges[a+"/"+b] || e.Remove && (e.Issue.Key != "" || e.Prerequisite.Key != "") {
			return ErrInvalidIssueGraphPayload
		}
		edges[a+"/"+b] = true
	}
	encoded, err := json.Marshal(p)
	if err != nil || len(encoded) > MaxIssueGraphPayloadBytes {
		return ErrInvalidIssueGraphPayload
	}
	return nil
}

// IssueGraphReceipt records native identity for every new or edited issue and
// every changed edge. Creation keys bind extension provenance to native IDs.
type IssueGraphReceipt struct {
	ActorID      int64                         `json:"actor_id"`
	RepositoryID int64                         `json:"repository_id"`
	Issues       []IssueGraphIssueReceipt      `json:"issues"`
	Dependencies []IssueGraphDependencyReceipt `json:"dependencies"`
}
type IssueGraphIssueReceipt struct {
	Key            string `json:"key,omitempty"`
	IssueID        int64  `json:"issue_id"`
	IssueIndex     int64  `json:"issue_index"`
	ContentVersion int    `json:"content_version"`
}
type IssueGraphDependencyReceipt struct {
	IssueID        int64 `json:"issue_id"`
	PrerequisiteID int64 `json:"prerequisite_id"`
	Removed        bool  `json:"removed"`
}
