// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import "context"

const (
	ContextHeader            = "X-Forgejo-Extension-Context"
	AdmissionHeader          = "X-Forgejo-Extension-Admission"
	LifecycleAdmissionHeader = "X-Forgejo-Extension-Lifecycle-Admission"
	SessionGenerationHeader  = "X-Extension-Session-Generation"
	SocketEnv                = "FORGEJO_EXTENSION_SOCKET"
	CallbackEnv              = "FORGEJO_EXTENSION_CALLBACK_SOCKET"
	ServiceCallbackEnv       = "FORGEJO_EXTENSION_SERVICE_CALLBACK_SOCKET"
	ServiceCallbackPeerEnv   = "FORGEJO_EXTENSION_SERVICE_CALLBACK_PEER_UID"
	DataEnv                  = "FORGEJO_EXTENSION_DATA"

	CapabilityActorRead               = "native.actor.read"
	CapabilityRepositoryRead          = "native.repository.read"
	CapabilityRepositoryIssuesRead    = "native.repository.issues.read"
	CapabilityOwnedRepositoriesSearch = "native.repositories.owned.search"
	CapabilityOrganizationOwnership   = "native.organization.ownership"
	CapabilityPublicKeysRead          = "native.user.public_keys.read"
	CapabilityContributionAuthorize   = "native.contribution.authorize"
	CapabilityServiceBridge           = "native.service.bridge"
	CapabilityBackgroundOperations    = "native.background.operations"
	CapabilityIssueIntake             = "native.repository.issues.intake"
	CapabilityHumanComments           = "native.repository.comments.write"
	CapabilityHumanEdits              = "native.repository.discussions.edit"
	CapabilityHumanStates             = "native.repository.discussions.state"
	// Human review summaries and inline creation share current human admission.
	CapabilityHumanReviews       = "native.repository.reviews.write"
	CapabilityHumanMerges        = "native.repository.merges.write"
	CapabilityHumanLocks         = "native.repository.discussions.lock"
	CapabilityHumanIssueCreate   = "native.repository.issues.create"
	CapabilityHumanPullCreate    = "native.repository.pulls.create"
	CapabilityExtensionLifecycle = "native.extension.lifecycle"
	PolicyForgejoUsername        = "forgejo.username"

	// BrowserContractVersion is the canonical browser mount contract
	// implemented by the host and published in sdk/browser. The host
	// renders it as data-extension-contract-version; the mount module
	// refuses any other value. Bump only with a coordinated contract change.
	BrowserContractVersion = 1
)

type Actor struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	SiteAdmin bool   `json:"site_admin"`
}

type Repository struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	Permission string `json:"permission"`
	// Configured navigation fact only, not proof that this ref exists or a write grant.
	DefaultBranch string `json:"default_branch,omitempty"`
}

type Contribution struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Scope  string `json:"scope"`
	Action string `json:"action"`
}

// Authority contains presentation facts. Its admission is private and omitted
// from JSON even when an extension encodes the whole value for browser code.
type Authority struct {
	ExtensionID       string       `json:"extension_id"`
	InstanceID        string       `json:"instance_id"`
	SessionGeneration string       `json:"session_generation"`
	Contribution      Contribution `json:"contribution"`
	Actor             Actor        `json:"actor"`
	Repository        *Repository  `json:"repository,omitempty"`
	admission         string
	callbackSocket    string
	serviceCallback   bool
	servicePeerUID    uint32
	servicePeerPinned bool
}

type RepositoryPage struct {
	Items      []Repository `json:"items"`
	NextCursor string       `json:"next_cursor"`
}

type PublicKey struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type ContributionRequest struct {
	ActorID      string       `json:"actor_id"`
	Contribution Contribution `json:"contribution"`
	RepositoryID string       `json:"repository_id,omitempty"`
}

type ContributionDecision struct {
	Allowed    bool   `json:"allowed"`
	DenialCode string `json:"denial_code,omitempty"`
}

type PolicyRequest struct {
	Operation string `json:"operation"`
	Username  string `json:"username"`
	UserID    string `json:"user_id,omitempty"`
}

type PolicyDecision struct {
	Allowed    bool   `json:"allowed"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type NativeClient interface {
	CurrentActor(context.Context) (Actor, error)
	Repository(context.Context, string) (Repository, error)
	SearchOwnedRepositories(context.Context, string, string, int) (RepositoryPage, error)
	OrganizationOwner(context.Context, string) (bool, error)
	PublicSSHKeys(context.Context) ([]PublicKey, error)
}

type (
	ContributionAuthorizer func(context.Context, ContributionRequest) (ContributionDecision, error)
	PolicyHandler          func(context.Context, PolicyRequest) (PolicyDecision, error)
)
