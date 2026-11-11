// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"errors"
	"strconv"
	"strings"
)

// These codecs retain the historic labels for older readers. New writers also
// carry the typed target; a contradictory pair refuses instead of changing repo.
func EncodeRefWriteTarget(target RepositoryTarget, ref string) string {
	if target == TargetWiki {
		return "wiki/" + ref
	}
	return ref
}

func DecodeRefWriteTarget(ref string) (RepositoryTarget, string) {
	if plain, ok := strings.CutPrefix(ref, "wiki/"); ok {
		return TargetWiki, plain
	}
	return TargetCode, ref
}

func EncodeScopedRefTarget(target RepositoryTarget, ref string) string {
	if target == TargetWiki {
		return "wiki:" + ref
	}
	return ref
}

func DecodeScopedRefTarget(ref string) (RepositoryTarget, string) {
	if plain, ok := strings.CutPrefix(ref, "wiki:"); ok {
		return TargetWiki, plain
	}
	return TargetCode, ref
}

func ResolveRepositoryTarget(explicit, legacy RepositoryTarget) (RepositoryTarget, error) {
	if explicit == "" {
		return legacy, nil
	}
	if explicit != TargetCode && explicit != TargetWiki || explicit != legacy {
		return "", errors.New("repository target conflicts with recorded legacy target")
	}
	return explicit, nil
}

func RefWriteRepositoryTarget(explicit RepositoryTarget, resource string) (RepositoryTarget, error) {
	parts := strings.SplitN(resource, "/", 3)
	if len(parts) != 3 {
		return "", errors.New("ref-write resource names no repository target")
	}
	target, _ := DecodeRefWriteTarget(parts[2])
	return ResolveRepositoryTarget(explicit, target)
}

func ScopedRefRepositoryTarget(ref ScopedRef) (RepositoryTarget, string, error) {
	legacy, plain := DecodeScopedRefTarget(ref.Ref)
	target, err := ResolveRepositoryTarget(ref.RepositoryTarget, legacy)
	return target, plain, err
}

// ReceiveRepositoryTarget decodes the retained receive resource. An unmarked
// legacy resource is distinguishable so recovery can fence an ambiguous wiki.
func ReceiveRepositoryTarget(explicit RepositoryTarget, resource string) (RepositoryTarget, bool, error) {
	_, suffix, marked := strings.Cut(resource, "/")
	legacy := TargetCode
	if marked {
		switch suffix {
		case "main":
		case "wiki":
			legacy = TargetWiki
		default:
			return "", true, errors.New("receive resource names no repository target")
		}
	}
	target, err := ResolveRepositoryTarget(explicit, legacy)
	return target, marked, err
}

func ReceiveResource(repositoryID int64, target RepositoryTarget) string {
	name := "main"
	if target == TargetWiki {
		name = "wiki"
	}
	return strconv.FormatInt(repositoryID, 10) + "/" + name
}
