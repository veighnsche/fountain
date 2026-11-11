// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

// Disclosed process-death barriers around the four atomic SQL primaries.
const (
	CrashPointThreadResolveBeforePrimary   = "thread-resolve-before-primary"
	CrashPointThreadResolveAfterPrimary    = "thread-resolve-after-primary"
	CrashPointLifecycleIssueBeforePrimary  = "lifecycle-issue-before-primary"
	CrashPointLifecycleIssueAfterPrimary   = "lifecycle-issue-after-primary"
	CrashPointLifecycleReviewBeforePrimary = "lifecycle-review-before-primary"
	CrashPointLifecycleReviewAfterPrimary  = "lifecycle-review-after-primary"
	CrashPointLifecycleNoteBeforePrimary   = "lifecycle-note-before-primary"
	CrashPointLifecycleNoteAfterPrimary    = "lifecycle-note-after-primary"
)
