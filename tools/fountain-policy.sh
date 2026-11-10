#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later

readonly fountain_maintained_branch=codex/fountain-extensions
readonly fountain_ci_tag_prefix=ci/fountain-
readonly fountain_full_ci_tag_prefix=ci/fountain-full-
readonly fountain_ci_workflow=fountain-ci.yml
readonly fountain_ci_workflow_path=".github/workflows/$fountain_ci_workflow"
readonly fountain_full_ci_workflow=fountain-full-ci.yml
readonly fountain_full_ci_workflow_path=".github/workflows/$fountain_full_ci_workflow"
readonly fountain_release_workflow=fountain-release.yml
readonly fountain_ci_gate='Fountain CI'
readonly fountain_actions_app_id=15368
readonly fountain_ci_event=push
readonly fountain_push_budget_seconds=300
readonly -a fountain_required_lanes=(SOURCE UNIT DATABASE BROWSER PACKAGING)
readonly -a fountain_workflows=(
  "$fountain_ci_workflow_path"
  "$fountain_full_ci_workflow_path"
  ".github/workflows/$fountain_release_workflow"
)
