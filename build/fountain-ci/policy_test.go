// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package fountainci

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type qualificationPolicy struct {
	values           map[string]string
	workflows, lanes []string
}

func readQualificationPolicy(t *testing.T) qualificationPolicy {
	t.Helper()
	policyPath, err := filepath.Abs(filepath.Join("..", "..", "tools", "fountain-policy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", "-c", `
source "$1"
for variable in fountain_maintained_branch fountain_ci_tag_prefix fountain_full_ci_tag_prefix fountain_ci_workflow fountain_ci_workflow_path fountain_full_ci_workflow_path fountain_release_workflow fountain_ci_gate fountain_actions_app_id fountain_ci_event fountain_push_budget_seconds; do
  printf '%s\t%s\n' "$variable" "${!variable}"
done
for workflow in "${fountain_workflows[@]}"; do printf 'workflow\t%s\n' "$workflow"; done
for lane in "${fountain_required_lanes[@]}"; do printf 'lane\t%s\n' "$lane"; done
`, "policy", policyPath)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read policy from another working directory: %v: %s", err, output)
	}
	policy := qualificationPolicy{values: make(map[string]string)}
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		key, value, found := strings.Cut(line, "\t")
		if !found {
			t.Fatalf("invalid policy value: %q", line)
		}
		switch key {
		case "workflow":
			policy.workflows = append(policy.workflows, value)
		case "lane":
			policy.lanes = append(policy.lanes, value)
		default:
			policy.values[key] = value
		}
	}
	return policy
}

func TestQualificationWorkflowDeclarationsMatchPolicy(t *testing.T) {
	policy := readQualificationPolicy(t)
	repo := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(repo, ".github", "workflows", "fountain-*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(files))
	for _, file := range files {
		relative, err := filepath.Rel(repo, file)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, filepath.ToSlash(relative))
	}
	got := slices.Clone(policy.workflows)
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) || len(got) != 3 {
		t.Fatalf("workflow lint inventory omitted or repeated a workflow: got %v, want %v", got, want)
	}
	for _, test := range []struct {
		path string
		tags []string
		gate string
	}{
		{policy.values["fountain_ci_workflow_path"], []string{policy.values["fountain_ci_tag_prefix"] + "*", "!" + policy.values["fountain_full_ci_tag_prefix"] + "*"}, policy.values["fountain_ci_gate"]},
		{policy.values["fountain_full_ci_workflow_path"], []string{policy.values["fountain_full_ci_tag_prefix"] + "*"}, "Fountain full CI"},
	} {
		t.Run(test.path, func(t *testing.T) {
			var workflow struct {
				On struct {
					Push        struct{ Branches, Tags []string }
					PullRequest struct{ Branches []string } `yaml:"pull_request"`
				} `yaml:"on"`
				Jobs map[string]struct{ Name string }
			}
			raw, err := os.ReadFile(filepath.Join(repo, test.path))
			if err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(raw, &workflow); err != nil {
				t.Fatal(err)
			}
			branch := []string{policy.values["fountain_maintained_branch"]}
			if !reflect.DeepEqual(workflow.On.Push.Branches, branch) || !reflect.DeepEqual(workflow.On.PullRequest.Branches, branch) || !reflect.DeepEqual(workflow.On.Push.Tags, test.tags) {
				t.Fatalf("workflow pre-execution triggers disagree with qualification: %+v", workflow.On)
			}
			if workflow.Jobs["ci"].Name != test.gate {
				t.Fatalf("aggregate gate identity changed: %q", workflow.Jobs["ci"].Name)
			}
		})
	}
}

func TestLocalSessionDeltaInventory(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tools", "fountain-session-delta.txt"))
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Fields(string(raw))
	want := []string{"FOUNTAIN.md", "file.go", "file_lifecycle_test.go", "file_test.go", "go.mod", "go.sum", "memory.go", "memory_lifecycle_test.go"}
	if len(paths) != len(want) {
		t.Fatalf("local session delta must cover exactly eight paths: %v", paths)
	}
	goFiles := 0
	for i, path := range paths {
		if path != "third_party/go-chi-session/"+want[i] {
			t.Fatalf("local session inventory changed provenance: %s", path)
		}
		if _, err := os.Stat(filepath.Join("..", "..", path)); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(path, ".go") {
			goFiles++
		}
	}
	if goFiles != 5 {
		t.Fatalf("formatting inventory lost locally modified source or tests: %d", goFiles)
	}
}

func TestExactReuseRequiresExplicitFirstAttempt(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	script, err := filepath.Abs(filepath.Join("..", "..", "tools", "fountain-ci-reuse.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []string{"", "1", "2"} {
		t.Run("attempt="+attempt, func(t *testing.T) {
			root := t.TempDir()
			fake := `#!/bin/sh
case "$2" in
  */actions/runs/42/attempts/*) date -u +%Y-%m-%dT%H:%M:%SZ ;;
  *) printf '%s\n' '{"workflow_runs":[{"id":17,"head_sha":"` + sha + `","head_branch":"ci/fountain-` + sha + `","event":"push","path":".github/workflows/fountain-ci.yml","status":"completed","conclusion":"success"}]}' ;;
esac
`
			if err := os.WriteFile(filepath.Join(root, "gh"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "bash", script)
			cmd.Dir = root
			for _, variable := range os.Environ() {
				if !strings.HasPrefix(variable, "GITHUB_RUN_ATTEMPT=") {
					cmd.Env = append(cmd.Env, variable)
				}
			}
			outputPath := filepath.Join(root, "output")
			cmd.Env = append(cmd.Env, "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "GITHUB_REPOSITORY=fixture/repo", "GITHUB_RUN_ID=42",
				"GITHUB_EVENT_NAME=push", "GITHUB_REF=refs/heads/codex/fountain-extensions", "GITHUB_SHA="+sha, "GITHUB_OUTPUT="+outputPath)
			if attempt != "" {
				cmd.Env = append(cmd.Env, "GITHUB_RUN_ATTEMPT="+attempt)
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("reuse from another working directory: %v: %s", err, output)
			}
			output, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(output), "reused=true\n") != (attempt == "1") {
				t.Fatalf("implicit or repeated attempt reused qualification: %s", output)
			}
		})
	}
}

func TestFullGateDoesNotReusePushSkips(t *testing.T) {
	policy := readQualificationPolicy(t)
	script, err := filepath.Abs(filepath.Join("..", "..", "tools", "fountain-ci-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"success", "skipped"} {
		cmd := exec.CommandContext(t.Context(), "bash", script)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "FOUNTAIN_CI_PROFILE=full", "REUSED_CHECKS=true", "GATE_STARTED_AT=0")
		for _, lane := range policy.lanes {
			cmd.Env = append(cmd.Env, lane+"_RESULT="+outcome)
		}
		if success := cmd.Run() == nil; success != (outcome == "success") {
			t.Fatalf("deep qualification changed skip/budget admission: %s", outcome)
		}
	}
}

func TestQualifiedPushChecksExactWorkflowAndGate(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const receipt = `{"workflow_runs":[{"id":17,"head_sha":"` + sha + `","head_branch":"ci/fountain-` + sha + `","event":"push","path":".github/workflows/fountain-ci.yml","status":"completed","conclusion":"success"}]}`
	const gate = `{"check_runs":[{"name":"Fountain CI","head_sha":"` + sha + `","app":{"id":15368},"status":"completed","conclusion":"success","details_url":"https://github.com/fixture/repo/actions/runs/17/job/1"}]}`
	script, err := filepath.Abs(filepath.Join("..", "..", "tools", "fountain-push.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, receipt, gate string
		qualified           bool
	}{
		{"exact", receipt, gate, true},
		{"another event", strings.Replace(receipt, `"event":"push"`, `"event":"pull_request"`, 1), gate, false},
		{"another workflow", strings.Replace(receipt, "fountain-ci.yml", "unrelated.yml", 1), gate, false},
		{"another source", strings.Replace(receipt, `"head_sha":"`+sha+`"`, `"head_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`, 1), gate, false},
		{"another application", receipt, strings.Replace(gate, `"id":15368`, `"id":42`, 1), false},
		{"another gate", receipt, strings.Replace(gate, "Fountain CI", "Unrelated CI", 1), false},
		{"another run", receipt, strings.Replace(gate, "/runs/17/", "/runs/18/", 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "tools"), 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"tools/fountain-check-clean.sh": "#!/bin/sh\nexit 0\n",
				"receipt.json":                  test.receipt,
				"gate.json":                     test.gate,
				"git": `#!/bin/bash
case "$1 $2" in
  'rev-parse --show-toplevel') echo "$PUSH_FIXTURE_ROOT" ;;
  'rev-parse HEAD') echo "$PUSH_FIXTURE_SHA" ;;
  'rev-parse --verify') exit 1 ;;
  'tag '*) ;;
  'push origin')
    if [[ $3 == "$PUSH_FIXTURE_SHA:refs/heads/codex/fountain-extensions" ]]; then
      touch "$PUSH_FIXTURE_ROOT/published"
    elif [[ $3 != "refs/tags/ci/fountain-$PUSH_FIXTURE_SHA" ]]; then
      exit 99
    fi ;;
  'ls-remote origin')
    object=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    if [[ -e $PUSH_FIXTURE_ROOT/published ]]; then object=$PUSH_FIXTURE_SHA; fi
    printf '%s\t%s\n' "$object" "$3" ;;
  'merge-base --is-ancestor') ;;
  *) printf 'unexpected git call: %s\n' "$*" >&2; exit 99 ;;
esac
`,
				"gh": `#!/bin/bash
case "$1 $2" in
  'repo view') echo fixture/repo ;;
  api*)
    if [[ $2 == *'/actions/workflows/'* ]]; then cat "$PUSH_FIXTURE_ROOT/receipt.json"
    elif [[ $2 == *'/check-runs?'* ]]; then cat "$PUSH_FIXTURE_ROOT/gate.json"
    else exit 99
    fi ;;
  *) exit 99 ;;
esac
`,
				"sleep": "#!/bin/sh\nexit 1\n",
			}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "bash", script)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "FOUNTAIN_PUSH_BOUNDED=1", "PUSH_FIXTURE_ROOT="+root, "PUSH_FIXTURE_SHA="+sha)
			output, runErr := cmd.CombinedOutput()
			_, publishedErr := os.Stat(filepath.Join(root, "published"))
			if (runErr == nil) != test.qualified || (publishedErr == nil) != test.qualified {
				t.Fatalf("unqualified maintained-branch publication: %v: %s", runErr, output)
			}
		})
	}
}
