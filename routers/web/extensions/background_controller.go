// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensions

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/models/extensionauth"
	"forgejo.org/modules/setting"
	extensions_service "forgejo.org/services/extensions"
	native_service "forgejo.org/services/nativeoperation"
)

// The shared private admission proves the original installation for history
// and retirement. Only claim creation additionally requires the exact live PAT
// and enrolled execution binding. Neither history nor retirement can dispatch.
func serveBackgroundController(w http.ResponseWriter, r *http.Request, admission extensions_service.BackgroundAdmission) {
	if admission.PackageID == "" || r.URL.Path == sdk.BackgroundControllerClaimPath && admission.PackageID != setting.OAuth2.FactoryAppliancePackageID {
		http.Error(w, "controller capability unavailable", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var ref sdk.ControllerReference
	var claim sdk.ControllerClaim
	var retirement sdk.ControllerRetirement
	switch r.URL.Path {
	case sdk.BackgroundControllerClaimPath:
		if !decodeBackgroundRequest(w, r, &claim) {
			return
		}
		ref.GrantID = claim.GrantID
		ref.CommandID = claim.CommandID
	case sdk.BackgroundControllerRetirePath:
		if !decodeBackgroundRequest(w, r, &retirement) {
			return
		}
		ref = retirement.ControllerReference
	default:
		if !decodeBackgroundRequest(w, r, &ref) {
			return
		}
	}
	if !sdk.ValidControllerReference(ref) {
		http.Error(w, "invalid controller reference", http.StatusBadRequest)
		return
	}
	g, err := desktopgrant.GetOperationGrant(ctx, ref.GrantID)
	if err != nil || g.InstallationID != admission.InstallationID || g.PackageID != admission.PackageID || g.CredentialKind != desktopgrant.CredentialPAT {
		http.Error(w, "controller reference unavailable", http.StatusNotFound)
		return
	}
	if r.URL.Path == sdk.BackgroundControllerGrantPath {
		if ref.CommandID != "" {
			http.Error(w, "unexpected claim reference", http.StatusBadRequest)
			return
		}
		writeBackgroundJSON(w, runtimeGrantProjection(ctx, g, strconv.FormatInt(g.RepositoryID, 10)))
		return
	}
	if len(ref.CommandID) != 32 {
		http.Error(w, "invalid claim reference", http.StatusBadRequest)
		return
	}
	if r.URL.Path == sdk.BackgroundControllerClaimPath {
		decision, err := extensionauth.VerifySubmission(ctx, extensionauth.SubmissionRequest{InstallationID: admission.InstallationID, TokenSecret: claim.Token, ActorID: g.ActorID, RepositoryID: g.RepositoryID, Kind: desktopgrant.ExecutionLaunch, PackageID: admission.PackageID, ControllerGrantID: g.ID, RequireController: true})
		if err != nil {
			http.Error(w, "execution authority unavailable", http.StatusForbidden)
			return
		}
		receipt, err := native_service.Default().ClaimApplianceExecution(ctx, g, decision, claim.ExecutionClaimRequest)
		if err != nil {
			http.Error(w, "execution claim refused", http.StatusConflict)
			return
		}
		writeBackgroundJSON(w, receipt)
		return
	}
	c, err := desktopgrant.GetExecutionClaim(ctx, ref.CommandID)
	if r.URL.Path == sdk.BackgroundControllerRetirePath && err == desktopgrant.ErrNotFound && retirement.State == desktopgrant.ClaimNotStarted && retirement.NotAfter > 0 {
		manifest, e1 := hex.DecodeString(retirement.ManifestDigest)
		digest, e2 := hex.DecodeString(retirement.ReceiptDigest)
		if e1 != nil || e2 != nil || len(manifest) != 32 || len(digest) != 32 || strings.ToLower(retirement.ManifestDigest) != retirement.ManifestDigest || strings.ToLower(retirement.ReceiptDigest) != retirement.ReceiptDigest {
			http.Error(w, "invalid retirement digest", http.StatusBadRequest)
			return
		}
		err = desktopgrant.WithPlacement(ctx, g.RepositoryID, func(ctx context.Context, _ *desktopgrant.Placement) error {
			var err error
			c, err = desktopgrant.FenceUnobservedExecution(ctx, g, ref.CommandID, retirement.ManifestDigest, retirement.ReceiptDigest, retirement.NotAfter)
			return err
		})
		if err != nil {
			http.Error(w, "claim fence refused", http.StatusConflict)
			return
		}
		writeBackgroundJSON(w, native_service.ClaimProjection(c, false))
		return
	}
	if err != nil || c.GrantID != g.ID || c.InstallationID != admission.InstallationID {
		http.Error(w, "claim not observed", http.StatusNotFound)
		return
	}
	if r.URL.Path == sdk.BackgroundControllerRetirePath {
		if retirement.NotAfter != 0 && retirement.NotAfter != c.NotAfter {
			http.Error(w, "claim deadline changed", http.StatusConflict)
			return
		}
		manifest, e1 := hex.DecodeString(retirement.ManifestDigest)
		digest, e2 := hex.DecodeString(retirement.ReceiptDigest)
		if e1 != nil || e2 != nil || len(manifest) != 32 || len(digest) != 32 || retirement.ManifestDigest != strings.ToLower(retirement.ManifestDigest) || retirement.ReceiptDigest != strings.ToLower(retirement.ReceiptDigest) {
			http.Error(w, "invalid retirement digest", http.StatusBadRequest)
			return
		}
		err = desktopgrant.WithPlacement(ctx, g.RepositoryID, func(ctx context.Context, _ *desktopgrant.Placement) error {
			var err error
			c, err = desktopgrant.RetireExecutionClaim(ctx, c.ID, g.ID, retirement.ManifestDigest, retirement.State, retirement.ReceiptDigest)
			return err
		})
		if err != nil {
			http.Error(w, "claim retirement refused", http.StatusConflict)
			return
		}
	}
	writeBackgroundJSON(w, native_service.ClaimProjection(c, false))
}
