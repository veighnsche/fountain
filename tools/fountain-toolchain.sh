#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later

fountain_go_toolchain() {
  [ "$#" -eq 1 ] || return 1
  awk '
    { sub(/\/\/.*/, "") }
    $1 == "toolchain" {
      if (NF != 2 || $2 !~ /^go[0-9]+\.[0-9]+\.[0-9]+$/) invalid = 1
      pins[FILENAME] = $2
      count[FILENAME]++
    }
    END {
      if (invalid || count[ARGV[1]] != 1 || count[ARGV[2]] != 1 || pins[ARGV[1]] != pins[ARGV[2]]) exit 1
      print pins[ARGV[1]]
    }
  ' "$1/go.mod" "$1/sdk/go.mod"
}
