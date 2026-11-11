// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"testing"

	nativeoperation_model "forgejo.org/models/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestOperationReadinessRequiresEveryDeclaredStage(t *testing.T) {
	require.NoError(t, validateOperationImplementations(operationImplementations()))
	for _, stage := range []string{"parse", "submit", "execute", "recover"} {
		for _, kind := range nativeoperation_model.SupportedKinds() {
			if kind == nativeoperation_model.KindRefPublish && stage == "execute" {
				continue // Publication claims and executes only at authenticated receive.
			}
			t.Run(kind+"/"+stage, func(t *testing.T) {
				implementations := operationImplementations()
				for i := range implementations {
					if implementations[i].kind != kind {
						continue
					}
					switch stage {
					case "parse":
						implementations[i].parse = nil
					case "submit":
						implementations[i].submit = nil
					case "execute":
						implementations[i].execute = nil
					case "recover":
						implementations[i].recover = nil
					}
				}
				require.ErrorIs(t, validateOperationImplementations(implementations), ErrKindUnavailable)
			})
		}
	}
}

func TestOperationReadinessRejectsMissingDuplicateAndWrongExecutionMode(t *testing.T) {
	for _, change := range []string{"missing", "duplicate", "unknown", "receive on another kind", "receive executor", "publication on submit"} {
		t.Run(change, func(t *testing.T) {
			implementations := operationImplementations()
			switch change {
			case "missing":
				implementations = implementations[1:]
			case "duplicate":
				implementations = append(implementations, implementations[0])
			case "unknown":
				implementations[0].kind = "unsupported.kind"
			case "receive on another kind":
				implementations[1].mode = executeOnReceive
				implementations[1].execute = nil
			case "receive executor":
				implementations[0].execute = (*Service).executeMerge
			case "publication on submit":
				implementations[0].mode = executeOnSubmit
				implementations[0].execute = (*Service).executeMerge
			}
			require.ErrorIs(t, validateOperationImplementations(implementations), ErrKindUnavailable)
		})
	}
}
