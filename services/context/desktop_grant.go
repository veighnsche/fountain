// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package context

// DesktopGrantRoute is trusted route composition, independent of the request
// payload and its current account permissions. The blank repository kind keeps
// the consent meaning of retained enrollments.
type DesktopGrantRoute string

const (
	DesktopRepositoryGrantRoute DesktopGrantRoute = ""
	DesktopIssueGrantRoute      DesktopGrantRoute = "issues"
)
