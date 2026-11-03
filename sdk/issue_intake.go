package extensions

import (
	"context"
	"errors"
	"regexp"
)

const OperationIssueIntakeAuthorize = "repository.issue_intake.authorize"
const BackgroundIssueIntakePath = "/v1/background/issue-intake"

// IssueIntakeClaims authorizes only bounded issue intake for one submitted
// request. It carries no implementation, Git, pull request or deployment grant.
type IssueIntakeClaims struct {
	Installation string `json:"installation"`
	Actor        string `json:"actor"`
	Repository   string `json:"repository"`
	Source       string `json:"source"`
	Revision     int64  `json:"revision"`
	Digest       string `json:"digest"`
	Expires      int64  `json:"expires"`
}
type IssueIntakeGrant struct {
	Token  string            `json:"token"`
	Claims IssueIntakeClaims `json:"claims"`
}
type IssueIntakeAuthorizationRequest struct {
	Source   string `json:"source"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}
type IssueIntakeMutation struct {
	Grant              string `json:"grant,omitempty"`
	Action             string `json:"action"`
	Slot               int    `json:"slot"`
	Step               int    `json:"step"`
	Title              string `json:"title"`
	Body               string `json:"body"`
	ExpectedTitle      string `json:"expected_title,omitempty"`
	ExpectedBodyDigest string `json:"expected_body_digest,omitempty"`
}
type IssueIntakeReceipt struct {
	Index      int64  `json:"index"`
	IssueID    int64  `json:"issue_id"`
	Slot       int    `json:"slot"`
	Title      string `json:"title"`
	BodyDigest string `json:"body_digest"`
}

// IssueIntakeAuthorization is host-recorded provenance inside the existing
// conditional issue ledger. The signed bearer token is never recorded there.
type IssueIntakeAuthorization struct {
	Claims      IssueIntakeClaims `json:"claims"`
	RequestHash string            `json:"request_hash"`
	Slot        int               `json:"slot"`
	Step        int               `json:"step"`
}

var intakeSource = regexp.MustCompile(`^[a-f0-9]{32}$`)
var intakeDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidIssueIntakeRequest(request IssueIntakeAuthorizationRequest) bool {
	return intakeSource.MatchString(request.Source) && request.Revision >= 0 && intakeDigest.MatchString(request.Digest)
}
func ValidIssueIntakeMutation(request IssueIntakeMutation) bool {
	return (request.Action == "create" || request.Action == "refine") && request.Slot >= 0 && request.Slot < 16 && request.Step >= 0 && request.Step < 32 && len(request.Title) > 0 && len(request.Title) <= 255 && len(request.Body) > 0 && len(request.Body) <= 20*1024 && (request.Action != "refine" || len(request.ExpectedTitle) > 0 && intakeDigest.MatchString(request.ExpectedBodyDigest))
}

func (a Authority) AuthorizeIssueIntake(ctx context.Context, repository string, request IssueIntakeAuthorizationRequest) (IssueIntakeGrant, error) {
	if !decimalID(repository) || !ValidIssueIntakeRequest(request) {
		return IssueIntakeGrant{}, errors.New("invalid issue intake authorization")
	}
	response, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationIssueIntakeAuthorize, RepositoryID: repository, IssueIntake: &request})
	if err != nil {
		return IssueIntakeGrant{}, err
	}
	return *response.IssueIntake, nil
}
