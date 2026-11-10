// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"
)

// TransactionOptions carries one reference-transaction callback to the
// internal API. ExecProof is the host-private execution capability read from
// the hook child's capability file; it is empty for ordinary pushes. The
// proof authenticates the execution in addition to the channel's internal
// token, and must never be logged.
type TransactionOptions struct {
	Origin       execcontext.GitExecutionOrigin
	Phase        string
	OldCommitIDs []string
	NewCommitIDs []string
	RefFullNames []git.RefName
	ExecProof    string
}

// HookReferenceTransaction enforces the native-operation reservation at
// Git's reference-transaction checkpoint.
func HookReferenceTransaction(ctx context.Context, ownerName, repoName string, opts TransactionOptions) ResponseExtra {
	reqURL := setting.LocalURL + fmt.Sprintf("api/internal/hook/reference-transaction/%s/%s", url.PathEscape(ownerName), url.PathEscape(repoName))
	req := newInternalRequest(ctx, reqURL, "POST", opts)
	req.SetReadWriteTimeout(time.Duration(60+len(opts.OldCommitIDs)) * time.Second)
	_, extra := requestJSONResp(req, &ResponseText{})
	return extra
}
