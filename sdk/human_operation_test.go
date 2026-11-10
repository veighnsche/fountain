// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestHumanOperationIDCompatibility(t *testing.T) {
	families := []struct {
		name, prefix string
		create       func(string, string, string) (string, error)
		valid        func(string, string, string) bool
		queryErr     error
	}{
		{"comment", "human-comment:", HumanCommentOperationID, ValidHumanCommentOperationID, ErrHumanCommentQuery},
		{"edit", "human-edit:", HumanEditOperationID, ValidHumanEditOperationID, ErrHumanEditQuery},
		{"issue create", "human-issue-create:", HumanIssueCreateOperationID, ValidHumanIssueCreateOperationID, ErrHumanIssueCreateQuery},
		{"lock", "human-lock:", HumanLockOperationID, ValidHumanLockOperationID, ErrHumanLockQuery},
		{"state", "human-state:", HumanStateOperationID, ValidHumanStateOperationID, ErrHumanStateQuery},
		{"review", "human-review:", HumanReviewOperationID, ValidHumanReviewOperationID, ErrHumanReviewQuery},
		{"inline", "human-inline:", HumanInlineOperationID, ValidHumanInlineOperationID, ErrHumanInlineQuery},
		{"thread", "human-thread:", HumanThreadOperationID, ValidHumanThreadOperationID, ErrHumanThreadQuery},
		{"review request", "human-review-request:", HumanReviewRequestOperationID, ValidHumanReviewRequestOperationID, ErrHumanReviewRequestQuery},
		{"merge", "human-merge:", HumanMergeOperationID, ValidHumanMergeOperationID, ErrHumanMergeQuery},
		{"manual merge", "human-manual-merge:", HumanManualMergeOperationID, ValidHumanManualMergeOperationID, ErrHumanManualMergeQuery},
		{"pull create", "human-pull-create:", HumanPullCreateOperationID, ValidHumanPullCreateOperationID, ErrHumanPullCreateQuery},
	}
	for _, family := range families {
		t.Run(family.name, func(t *testing.T) {
			id, err := family.create("2", "7", "saved-command")
			if err != nil {
				t.Fatal(err)
			}
			// Existing journals retain this exact namespace and command digest.
			const suffix = "385ca6c21ee99ce5f378593d42225ed1bbae4c952154f3fd996ddb858986a581"
			if id != family.prefix+"2:7:"+suffix || !family.valid(id, "2", "7") {
				t.Fatalf("saved command identity changed: %q", id)
			}
			if family.valid(id, "3", "7") || family.valid(id, "2", "8") || family.valid(strings.TrimSuffix(id, suffix)+strings.ToUpper(suffix), "2", "7") || family.valid(id+"0", "2", "7") {
				t.Fatal("foreign or malformed saved identity accepted")
			}
			for _, input := range [][3]string{{"02", "7", "saved-command"}, {"2", "0", "saved-command"}, {"2", "7", "invalid command"}} {
				if _, err := family.create(input[0], input[1], input[2]); !errors.Is(err, family.queryErr) {
					t.Fatalf("invalid captured identity lost family error: %v", err)
				}
			}
		})
	}
}

func humanCommentFixture(t *testing.T) HumanCommentRequest {
	t.Helper()
	id, err := HumanCommentOperationID("2", "7", "saved-command")
	if err != nil {
		t.Fatal(err)
	}
	return HumanCommentRequest{
		OperationID: id,
		Expected: RepositoryCommentSnapshot{
			RepositoryID: "7", ActorID: "2", Subject: "issue", SubjectID: "11", IssueID: "11", Index: "3",
			NativeRevision: 9007199254740993,
			Action:         RepositoryActionCapability{State: "allowed", Reason: "issue_comment"},
		},
		NotAfter: 9007199254740993,
		Body:     "saved comment",
	}
}

func TestHumanCommentCanonicalIntentPreservesHistoricalNormalization(t *testing.T) {
	q := humanCommentFixture(t)
	q.OperationID = "saved-command"
	raw, digest, err := CanonicalHumanCommentIntent(q)
	if err != nil {
		t.Fatal(err)
	}
	const saved = `{"operation_id":"saved-command","actor_id":2,"repository_id":7,"kind":"discussion.comment.create","authorization_revision":"native-human-request","expected_native_revision":9007199254740993,"not_after":9007199254740993,"payload":{"body":"saved comment","expected":{"action":{"reason":"issue_comment","state":"allowed"},"actor_id":"2","index":"3","issue_id":"11","native_revision":9007199254740992,"repository_id":"7","subject":"issue","subject_id":"11"},"not_after":9007199254740992,"operation_id":"saved-command"}}`
	if raw != saved {
		t.Fatalf("saved intent bytes changed:\n%s", raw)
	}
	sum := sha256.Sum256([]byte(saved))
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("saved intent digest changed: %q", digest)
	}
}

func TestSubmitHumanCommentBindsReturnedIntent(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "matching saved intent"
		if changed {
			name = "another body under the same operation ID"
		}
		t.Run(name, func(t *testing.T) {
			q := humanCommentFixture(t)
			observed := q
			if changed {
				observed.Body = "a different saved comment"
			}
			_, digest, err := CanonicalHumanCommentIntent(observed)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := json.Marshal(HumanCommentReceipt{RepositoryID: 7, ActorID: 2, Subject: "issue", SubjectID: 11, IssueID: 11, Index: 3, CommentID: 19})
			if err != nil {
				t.Fatal(err)
			}
			record := OperationRecord{
				InstallationID: "11111111-2222-3333-4444-555555555555", OperationID: q.OperationID, Outcome: EffectCommitted,
				ActorID: "2", RepositoryID: "7", Kind: OperationKindHumanComment,
				EffectState: EffectCommitted, CancellationStatus: CancellationNone,
				CompletionState: CompletionComplete, IntentDigest: digest, Receipt: receipt,
			}
			socket := unixBackgroundServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != NativeCallbackPath || r.Header.Get(AdmissionHeader) != "private-admission" {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				_ = json.NewEncoder(w).Encode(CallbackResponse{HumanComment: &OperationLookup{
					InstallationID: "11111111-2222-3333-4444-555555555555", OperationID: q.OperationID, Status: EffectCommitted, Record: &record,
				}})
			}))
			authority := Authority{Actor: Actor{ID: "2"}, callbackSocket: socket, admission: "private-admission"}
			got, err := authority.SubmitHumanComment(t.Context(), "11111111-2222-3333-4444-555555555555", q)
			if changed {
				if err == nil {
					t.Fatal("accepted a result for different immutable comment content")
				}
			} else if err != nil || got.IntentDigest != digest {
				t.Fatalf("matching saved intent refused: %+v, %v", got, err)
			}
		})
	}
}
