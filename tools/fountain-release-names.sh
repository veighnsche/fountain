#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later

fountain_release_names() {
  [[ $# == 1 && $1 =~ ^[0-9]+\.[0-9]+\.[0-9]+-fountain\.[1-9][0-9]*$ ]] || return 1
  local fountain_compatibility=gitea-1.22.0
  fountain_release_version=$1
  fountain_full_version="$fountain_release_version+$fountain_compatibility"
  fountain_source_root="fountain-$fountain_release_version"
  fountain_source_archive="$fountain_source_root-source.tar.gz"
  fountain_prepared_root="forgejo-src-$fountain_full_version"
  fountain_prepared_archive="$fountain_prepared_root.tar.gz"
  fountain_platform=linux/amd64
  fountain_binary="fountain-$fountain_release_version-${fountain_platform//\//-}"
  fountain_binary_archive="$fountain_binary.gz"
}
