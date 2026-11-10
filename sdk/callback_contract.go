// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import "errors"

type callbackSelectors uint64

const (
	callbackSelectorHumanMerge callbackSelectors = 1 << iota
	callbackSelectorHumanPullCreate
	callbackSelectorHumanManualMerge
	callbackSelectorHumanReviewRequest
	callbackSelectorHumanThread
	callbackSelectorHumanInline
	callbackSelectorHumanReview
	callbackSelectorHumanState
	callbackSelectorHumanLock
	callbackSelectorHumanIssueCreate
	callbackSelectorHumanEdit
	callbackSelectorHumanComment
	callbackSelectorRepositorySource
	callbackSelectorIssueIntakeAuthorize
	callbackSelectorRuntimeControllerSelect
	callbackSelectorRuntimeControllerAuthorize
	callbackSelectorRuntimeControllerGrant
	callbackSelectorRepositoryIssuesBody
	callbackSelectorRepositoryIssuesState
	callbackSelectorRepositoryComments
	callbackSelectorRepositoryCommentSnapshot
	callbackSelectorRepositoryEditSnapshot
	callbackSelectorRepositoryReviewRequestSnapshot
	callbackSelectorRepositoryThreadSnapshot
	callbackSelectorRepositoryInlineSnapshot
	callbackSelectorRepositoryPullCreateSnapshot
	callbackSelectorRepositoryMergeSnapshot
	callbackSelectorRepositoryManualMergeSnapshot
	callbackSelectorRepositoryReviewSnapshot
	callbackSelectorRepositoryStateSnapshot
	callbackSelectorRepositoryLockSnapshot
	callbackSelectorRepositoryPulls
	callbackSelectorRepositoryPullFiles
	callbackSelectorRepositoryPullCommits
	callbackSelectorRepositoryReviews
	callbackSelectorRepositoryThreads
	callbackSelectorRepositoryCapabilities
	callbackSelectorReadableRepositories
	callbackSelectorRepositoryChecks
)

type callbackOperation struct {
	name        string
	capability  string
	selectors   callbackSelectors
	statusError func(int) error
}

type callbackVariant struct {
	operations []callbackOperation
	present    func(CallbackResponse) bool
}

// callbackVariants describes the wire union. Host handlers and semantic result
// validators remain with the code that owns each operation.
var callbackVariants = []callbackVariant{
	{
		operations: []callbackOperation{
			{OperationRuntimeControllerRead, CapabilityRuntimeControl, 0, nil},
		},
		present: func(result CallbackResponse) bool { return result.RuntimeController != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRuntimeControllerSelect, CapabilityRuntimeControl, callbackSelectorRuntimeControllerSelect, nil},
		},
		present: func(result CallbackResponse) bool { return result.RuntimeSelection != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRuntimeControllerAuthorize, CapabilityRuntimeControl, callbackSelectorRuntimeControllerAuthorize, nil},
			{OperationRuntimeControllerGrant, CapabilityRuntimeControl, callbackSelectorRuntimeControllerGrant, nil},
		},
		present: func(result CallbackResponse) bool { return result.RuntimeGrant != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanMergeSubmit, CapabilityHumanMerges, callbackSelectorHumanMerge, humanMergeCallbackStatusError},
			{OperationHumanMergeGet, CapabilityHumanMerges, callbackSelectorHumanMerge, humanMergeCallbackStatusError},
			{OperationHumanMergeCancel, CapabilityHumanMerges, callbackSelectorHumanMerge, humanMergeCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanMerge != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanPullCreateSubmit, CapabilityHumanPullCreate, callbackSelectorHumanPullCreate, humanPullCreateCallbackStatusError},
			{OperationHumanPullCreateGet, CapabilityHumanPullCreate, callbackSelectorHumanPullCreate, humanPullCreateCallbackStatusError},
			{OperationHumanPullCreateCancel, CapabilityHumanPullCreate, callbackSelectorHumanPullCreate, humanPullCreateCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanPullCreate != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanManualMergeSubmit, CapabilityHumanMerges, callbackSelectorHumanManualMerge, humanManualMergeCallbackStatusError},
			{OperationHumanManualMergeGet, CapabilityHumanMerges, callbackSelectorHumanManualMerge, humanManualMergeCallbackStatusError},
			{OperationHumanManualMergeCancel, CapabilityHumanMerges, callbackSelectorHumanManualMerge, humanManualMergeCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanManualMerge != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanReviewRequestSubmit, CapabilityHumanReviews, callbackSelectorHumanReviewRequest, humanReviewRequestCallbackStatusError},
			{OperationHumanReviewRequestGet, CapabilityHumanReviews, callbackSelectorHumanReviewRequest, humanReviewRequestCallbackStatusError},
			{OperationHumanReviewRequestCancel, CapabilityHumanReviews, callbackSelectorHumanReviewRequest, humanReviewRequestCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanReviewRequest != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanThreadSubmit, CapabilityHumanReviews, callbackSelectorHumanThread, humanThreadCallbackStatusError},
			{OperationHumanThreadGet, CapabilityHumanReviews, callbackSelectorHumanThread, humanThreadCallbackStatusError},
			{OperationHumanThreadCancel, CapabilityHumanReviews, callbackSelectorHumanThread, humanThreadCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanThread != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanInlineSubmit, CapabilityHumanReviews, callbackSelectorHumanInline, humanInlineCallbackStatusError},
			{OperationHumanInlineGet, CapabilityHumanReviews, callbackSelectorHumanInline, humanInlineCallbackStatusError},
			{OperationHumanInlineCancel, CapabilityHumanReviews, callbackSelectorHumanInline, humanInlineCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanInline != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryReviewRequestSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryReviewRequestSnapshot, reviewRequestSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.ReviewRequestSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryThreadSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryThreadSnapshot, threadSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.ThreadSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryInlineSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryInlineSnapshot, inlineSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.InlineSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryPullCreateSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryPullCreateSnapshot, pullCreateSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.PullCreateSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryMergeSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryMergeSnapshot, mergeSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.MergeSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryManualMergeSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryManualMergeSnapshot, manualMergeSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.ManualMergeSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryReviewSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryReviewSnapshot, reviewSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.ReviewSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryStateSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryStateSnapshot, stateSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.StateSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryLockSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryLockSnapshot, lockSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.LockSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanReviewSubmit, CapabilityHumanReviews, callbackSelectorHumanReview, humanReviewCallbackStatusError},
			{OperationHumanReviewGet, CapabilityHumanReviews, callbackSelectorHumanReview, humanReviewCallbackStatusError},
			{OperationHumanReviewCancel, CapabilityHumanReviews, callbackSelectorHumanReview, humanReviewCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanReview != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanStateSubmit, CapabilityHumanStates, callbackSelectorHumanState, humanStateCallbackStatusError},
			{OperationHumanStateGet, CapabilityHumanStates, callbackSelectorHumanState, humanStateCallbackStatusError},
			{OperationHumanStateCancel, CapabilityHumanStates, callbackSelectorHumanState, humanStateCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanState != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanLockSubmit, CapabilityHumanLocks, callbackSelectorHumanLock, humanLockCallbackStatusError},
			{OperationHumanLockGet, CapabilityHumanLocks, callbackSelectorHumanLock, humanLockCallbackStatusError},
			{OperationHumanLockCancel, CapabilityHumanLocks, callbackSelectorHumanLock, humanLockCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanLock != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanIssueCreateSubmit, CapabilityHumanIssueCreate, callbackSelectorHumanIssueCreate, humanIssueCreateCallbackStatusError},
			{OperationHumanIssueCreateGet, CapabilityHumanIssueCreate, callbackSelectorHumanIssueCreate, humanIssueCreateCallbackStatusError},
			{OperationHumanIssueCreateCancel, CapabilityHumanIssueCreate, callbackSelectorHumanIssueCreate, humanIssueCreateCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanIssueCreate != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryIssueCreateSnapshot, CapabilityRepositoryRead, 0, issueCreateSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.IssueCreateSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanEditSubmit, CapabilityHumanEdits, callbackSelectorHumanEdit, humanEditCallbackStatusError},
			{OperationHumanEditGet, CapabilityHumanEdits, callbackSelectorHumanEdit, humanEditCallbackStatusError},
			{OperationHumanEditCancel, CapabilityHumanEdits, callbackSelectorHumanEdit, humanEditCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanEdit != nil },
	},
	{
		operations: []callbackOperation{
			{OperationHumanCommentSubmit, CapabilityHumanComments, callbackSelectorHumanComment, humanCommentCallbackStatusError},
			{OperationHumanCommentGet, CapabilityHumanComments, callbackSelectorHumanComment, humanCommentCallbackStatusError},
			{OperationHumanCommentCancel, CapabilityHumanComments, callbackSelectorHumanComment, humanCommentCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.HumanComment != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryCapabilities, CapabilityRepositoryRead, callbackSelectorRepositoryCapabilities, repositoryCapabilitiesCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Capabilities != nil },
	},
	{
		operations: []callbackOperation{
			{OperationReadableRepositories, CapabilityRepositoryRead, callbackSelectorReadableRepositories, repositoryCatalogCallbackStatusError},
			{OperationOwnedRepositories, CapabilityOwnedRepositoriesSearch, 0, nil},
		},
		present: func(result CallbackResponse) bool { return result.Page != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryChecks, CapabilityRepositoryRead, callbackSelectorRepositoryChecks, checksCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Checks != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryThreads, CapabilityRepositoryRead, callbackSelectorRepositoryThreads, threadsCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Threads != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryReviews, CapabilityRepositoryRead, callbackSelectorRepositoryReviews, repositoryCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Reviews != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryPullFiles, CapabilityRepositoryRead, callbackSelectorRepositoryPullFiles, pullFilesCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.PullFiles != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryPullCommits, CapabilityRepositoryRead, callbackSelectorRepositoryPullCommits, pullCommitsCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.PullCommits != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryPulls, CapabilityRepositoryRead, callbackSelectorRepositoryPulls, repositoryCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Pulls != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryComments, CapabilityRepositoryRead, callbackSelectorRepositoryComments, repositoryCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Comments != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryEditSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryEditSnapshot, editSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.EditSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryCommentSnapshot, CapabilityRepositoryRead, callbackSelectorRepositoryCommentSnapshot, commentSnapshotCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.CommentSnapshot != nil },
	},
	{
		operations: []callbackOperation{
			{OperationIssueIntakeAuthorize, CapabilityIssueIntake, callbackSelectorIssueIntakeAuthorize, nil},
		},
		present: func(result CallbackResponse) bool { return result.IssueIntake != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositoryIssues, CapabilityRepositoryRead, callbackSelectorRepositoryIssuesBody | callbackSelectorRepositoryIssuesState, repositoryCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Issues != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepositorySource, CapabilityRepositoryRead, callbackSelectorRepositorySource, repositoryCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Source != nil },
	},
	{
		operations: []callbackOperation{
			{OperationCurrentActor, CapabilityActorRead, 0, nil},
		},
		present: func(result CallbackResponse) bool { return result.Actor != nil },
	},
	{
		operations: []callbackOperation{
			{OperationRepository, CapabilityRepositoryRead, 0, repositoryCallbackStatusError},
		},
		present: func(result CallbackResponse) bool { return result.Repository != nil },
	},
	{
		operations: []callbackOperation{
			{OperationOrganizationOwner, CapabilityOrganizationOwnership, 0, nil},
		},
		present: func(result CallbackResponse) bool { return result.Owner != nil },
	},
	{
		operations: []callbackOperation{
			{OperationPublicSSHKeys, CapabilityPublicKeysRead, 0, nil},
		},
		present: func(result CallbackResponse) bool { return result.PublicKeys != nil },
	},
}

type callbackSelector struct {
	mask    callbackSelectors
	human   bool
	message string
	present func(CallbackRequest) bool
}

var callbackRequestSelectors = []callbackSelector{
	{callbackSelectorHumanMerge, true, "unexpected human merge selector", func(request CallbackRequest) bool {
		return request.HumanMerge != nil || request.MergeOperationID != "" || request.MergeInstallationID != ""
	}},
	{callbackSelectorHumanPullCreate, true, "unexpected human pull creation selector", func(request CallbackRequest) bool {
		return request.HumanPullCreate != nil || request.PullCreateOperationID != "" || request.PullCreateInstallationID != ""
	}},
	{callbackSelectorHumanManualMerge, true, "unexpected human manual merge selector", func(request CallbackRequest) bool {
		return request.HumanManualMerge != nil || request.ManualMergeOperationID != "" || request.ManualMergeInstallationID != ""
	}},
	{callbackSelectorHumanReviewRequest, true, "unexpected human reviewer selector", func(request CallbackRequest) bool {
		return request.HumanReviewRequest != nil || request.ReviewRequestOperationID != "" || request.ReviewRequestInstallationID != ""
	}},
	{callbackSelectorHumanThread, true, "unexpected human thread selector", func(request CallbackRequest) bool {
		return request.HumanThread != nil || request.ThreadOperationID != "" || request.ThreadInstallationID != ""
	}},
	{callbackSelectorHumanInline, true, "unexpected human inline selector", func(request CallbackRequest) bool {
		return request.HumanInline != nil || request.InlineOperationID != "" || request.InlineInstallationID != ""
	}},
	{callbackSelectorHumanReview, true, "unexpected human review selector", func(request CallbackRequest) bool {
		return request.HumanReview != nil || request.ReviewOperationID != "" || request.ReviewInstallationID != ""
	}},
	{callbackSelectorHumanState, true, "unexpected human state selector", func(request CallbackRequest) bool {
		return request.HumanState != nil || request.StateOperationID != "" || request.StateInstallationID != ""
	}},
	{callbackSelectorHumanLock, true, "unexpected human lock selector", func(request CallbackRequest) bool {
		return request.HumanLock != nil || request.LockOperationID != "" || request.LockInstallationID != ""
	}},
	{callbackSelectorHumanIssueCreate, true, "unexpected human issue creation selector", func(request CallbackRequest) bool {
		return request.HumanIssueCreate != nil || request.IssueCreateOperationID != "" || request.IssueCreateInstallationID != ""
	}},
	{callbackSelectorHumanEdit, true, "unexpected human edit selector", func(request CallbackRequest) bool {
		return request.HumanEdit != nil || request.EditOperationID != "" || request.EditInstallationID != ""
	}},
	{callbackSelectorHumanComment, true, "unexpected human comment selector", func(request CallbackRequest) bool {
		return request.HumanComment != nil || request.CommentOperationID != "" || request.CommentInstallationID != ""
	}},
	{callbackSelectorRepositorySource, false, "invalid native capability request", func(request CallbackRequest) bool {
		return (request.SourceCommit != "" || len(request.SourcePaths) != 0 || len(request.SourceActors) != 0)
	}},
	{callbackSelectorIssueIntakeAuthorize, false, "unexpected issue intake fields", func(request CallbackRequest) bool { return request.IssueIntake != nil }},
	{callbackSelectorRuntimeControllerSelect, false, "unexpected controller command", func(request CallbackRequest) bool { return request.RuntimeSelection != nil }},
	{callbackSelectorRuntimeControllerAuthorize, false, "unexpected runtime authorization", func(request CallbackRequest) bool { return request.RuntimeAuthorization != nil }},
	{callbackSelectorRuntimeControllerGrant, false, "unexpected runtime grant reference", func(request CallbackRequest) bool { return request.RuntimeGrantID != "" }},
	{callbackSelectorRepositoryIssuesBody, false, "unexpected issue body selector", func(request CallbackRequest) bool { return request.CompleteIssueBodies }},
	{callbackSelectorRepositoryIssuesState, false, "unexpected issue state", func(request CallbackRequest) bool { return request.IssueState != "" }},
	{callbackSelectorRepositoryComments, false, "unexpected discussion selector", func(request CallbackRequest) bool { return request.Comments != nil }},
	{callbackSelectorRepositoryCommentSnapshot, false, "unexpected comment snapshot selector", func(request CallbackRequest) bool { return request.CommentSnapshot != nil }},
	{callbackSelectorRepositoryEditSnapshot, false, "unexpected edit snapshot selector", func(request CallbackRequest) bool { return request.EditSnapshot != nil }},
	{callbackSelectorRepositoryReviewRequestSnapshot, false, "unexpected reviewer selection", func(request CallbackRequest) bool { return request.ReviewRequestSnapshot != nil }},
	{callbackSelectorRepositoryThreadSnapshot, false, "unexpected thread preparation selector", func(request CallbackRequest) bool { return request.ThreadSnapshot != nil }},
	{callbackSelectorRepositoryInlineSnapshot, false, "unexpected inline preparation selector", func(request CallbackRequest) bool { return request.InlineSnapshot != nil }},
	{callbackSelectorRepositoryPullCreateSnapshot, false, "unexpected pull creation preparation selector", func(request CallbackRequest) bool { return request.PullCreateSnapshot != nil }},
	{callbackSelectorRepositoryMergeSnapshot, false, "unexpected merge preparation selector", func(request CallbackRequest) bool { return request.MergeSnapshot != nil }},
	{callbackSelectorRepositoryManualMergeSnapshot, false, "unexpected manual merge preparation selector", func(request CallbackRequest) bool { return request.ManualMergeSnapshot != nil }},
	{callbackSelectorRepositoryReviewSnapshot, false, "unexpected review snapshot selector", func(request CallbackRequest) bool { return request.ReviewSnapshot != nil }},
	{callbackSelectorRepositoryStateSnapshot, false, "unexpected state snapshot selector", func(request CallbackRequest) bool { return request.StateSnapshot != nil }},
	{callbackSelectorRepositoryLockSnapshot, false, "unexpected lock snapshot selector", func(request CallbackRequest) bool { return request.LockSnapshot != nil }},
	{callbackSelectorRepositoryPulls, false, "unexpected pull selector", func(request CallbackRequest) bool { return request.Pulls != nil }},
	{callbackSelectorRepositoryPullFiles, false, "unexpected pull files selector", func(request CallbackRequest) bool { return request.PullFiles != nil }},
	{callbackSelectorRepositoryPullCommits, false, "unexpected pull commits selector", func(request CallbackRequest) bool { return request.PullCommits != nil }},
	{callbackSelectorRepositoryReviews, false, "unexpected review selector", func(request CallbackRequest) bool { return request.Reviews != nil }},
	{callbackSelectorRepositoryThreads, false, "unexpected thread selector", func(request CallbackRequest) bool { return request.Threads != nil }},
	{callbackSelectorRepositoryCapabilities, false, "unexpected capability fields", func(request CallbackRequest) bool { return request.Capabilities != nil }},
	{callbackSelectorReadableRepositories, false, "unexpected repository catalogue fields", func(request CallbackRequest) bool { return request.Catalog != nil }},
	{callbackSelectorRepositoryChecks, false, "unexpected check selector", func(request CallbackRequest) bool { return request.Checks != nil }},
}

func callbackOperationSpec(operation string) (callbackOperation, *callbackVariant, bool) {
	for i := range callbackVariants {
		variant := &callbackVariants[i]
		for _, spec := range variant.operations {
			if spec.name == operation {
				return spec, variant, true
			}
		}
	}
	return callbackOperation{}, nil, false
}

// NativeCallbackCapability reports the declared capability for a callback wire
// operation. Contextual admission alternatives remain host policy.
func NativeCallbackCapability(operation string) (string, bool) {
	spec, _, ok := callbackOperationSpec(operation)
	return spec.capability, ok
}

// NeedsNativeCallback reports whether any declared capability uses the private
// host transport, including the lifecycle and background endpoint families.
func NeedsNativeCallback(capabilities []string) bool {
	for _, capability := range capabilities {
		switch capability {
		case CapabilityRepositoryIssuesRead, CapabilityBackgroundOperations, CapabilityExtensionLifecycle:
			return true
		}
		for _, variant := range callbackVariants {
			for _, operation := range variant.operations {
				if operation.capability == capability {
					return true
				}
			}
		}
	}
	return false
}

// ValidateHumanCallbackSelectors checks human selector namespaces separately so
// hosts can preserve their admission-before-payload validation order.
func ValidateHumanCallbackSelectors(request CallbackRequest) error {
	return validateCallbackSelectors(request, true)
}

// ValidateCallbackSelectors rejects selector fields belonging to another wire
// operation. It does not confer authority or validate an operation's payload.
func ValidateCallbackSelectors(request CallbackRequest) error {
	return validateCallbackSelectors(request, false)
}

func validateCallbackSelectors(request CallbackRequest, humanOnly bool) error {
	spec, _, _ := callbackOperationSpec(request.Operation)
	for _, selector := range callbackRequestSelectors {
		if (!humanOnly || selector.human) && spec.selectors&selector.mask == 0 && selector.present(request) {
			return errors.New(selector.message)
		}
	}
	return nil
}

func validCallbackResultEnvelope(result CallbackResponse, request CallbackRequest) bool {
	_, expected, ok := callbackOperationSpec(request.Operation)
	if !ok || !expected.present(result) {
		return false
	}
	fields := 0
	for _, variant := range callbackVariants {
		if variant.present(result) {
			fields++
		}
	}
	return fields == 1
}
