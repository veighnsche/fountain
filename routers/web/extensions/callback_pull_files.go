package extensions

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/modules/git"
)

const pullPatchLimit = 16384

// Keep subprocess output bounded and propagate the actual command failure.
// The UI diff helper logs subprocess errors; an API must not call that an empty
// complete diff, so this reader uses the same mature Git command owner directly.
type pullDiffBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *pullDiffBuffer) Len() int      { return b.buffer.Len() }
func (b *pullDiffBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *pullDiffBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		b.exceeded = true
		return remaining, io.ErrShortWrite
	}
	return b.buffer.Write(p)
}

func pullDiffCommand(ctx context.Context, repo *git.Repository, base, head string, output *pullDiffBuffer, kind string, paths ...string) error {
	cmd := git.NewCommand(ctx, "--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-relative", "--ignore-submodules=none", "--submodule=short", "--find-renames=50%", "--diff-algorithm=histogram")
	switch kind {
	case "raw":
		cmd.AddArguments("--raw", "-z", "--no-abbrev")
	case "stat":
		cmd.AddArguments("--numstat", "-z")
	case "patch":
		cmd.AddArguments("--patch", "--src-prefix=a/", "--dst-prefix=b/", "--unified=3")
	}
	return cmd.AddDynamicArguments(base, head).AddDashesAndList(paths...).Run(&git.RunOpts{Dir: repo.Path, Stdout: output})
}

type nativePullFile struct{ old, new, change string }

func pullFileRecords(raw []byte) ([]nativePullFile, bool) {
	parts := bytes.Split(raw, []byte{0})
	files := []nativePullFile{}
	for i := 0; i < len(parts)-1; {
		header := strings.Fields(string(parts[i]))
		i++
		if len(header) != 5 || !strings.HasPrefix(header[0], ":") || i >= len(parts)-1 {
			return nil, false
		}
		path := string(parts[i])
		i++
		f := nativePullFile{old: path, new: path, change: "unknown"}
		switch header[4][0] {
		case 'A':
			f.old, f.change = "", "added"
		case 'D':
			f.new, f.change = "", "deleted"
		case 'M', 'T':
			f.change = "modified"
		case 'R', 'C':
			if i >= len(parts)-1 {
				return nil, false
			}
			f.new = string(parts[i])
			i++
			f.change = "renamed"
			if header[4][0] == 'C' {
				f.change = "copied"
			}
		}
		if path == "" || f.old == "" && f.new == "" || len(f.old) > 4096 || len(f.new) > 4096 || !utf8.ValidString(f.old) || !utf8.ValidString(f.new) {
			return nil, false
		}
		files = append(files, f)
		if len(files) > 10000 {
			return nil, false
		}
	}
	return files, len(parts) > 0 && len(parts[len(parts)-1]) == 0
}

// Numstat uses NUL-delimited names, including a distinct old/new rename pair.
// Refuse to attribute multiple/mismatched records to one file.
func pullFileStat(raw []byte, f nativePullFile) (add, del *int, binary, valid bool) {
	fields := bytes.SplitN(raw, []byte{'\t'}, 3)
	if len(fields) != 3 {
		return nil, nil, false, false
	}
	paths := bytes.Split(fields[2], []byte{0})
	if len(paths) == 4 && len(paths[0]) == 0 && len(paths[3]) == 0 {
		if string(paths[1]) != f.old || string(paths[2]) != f.new {
			return nil, nil, false, false
		}
	} else if len(paths) == 2 && len(paths[1]) == 0 {
		path := f.new
		if path == "" {
			path = f.old
		}
		if string(paths[0]) != path {
			return nil, nil, false, false
		}
	} else {
		return nil, nil, false, false
	}
	if string(fields[0]) == "-" && string(fields[1]) == "-" {
		return nil, nil, true, true
	}
	a, e1 := strconv.Atoi(string(fields[0]))
	d, e2 := strconv.Atoi(string(fields[1]))
	if e1 != nil || e2 != nil || a < 0 || d < 0 {
		return nil, nil, false, false
	}
	return &a, &d, false, true
}

func readPullFile(ctx context.Context, repo *git.Repository, base, head string, f nativePullFile) (extension.RepositoryPullFile, error) {
	item := extension.RepositoryPullFile{OldPath: f.old, NewPath: f.new, Change: f.change, PatchState: "unavailable"}
	paths := []string{}
	if f.old != "" {
		paths = append(paths, f.old)
	}
	if f.new != "" && f.new != f.old {
		paths = append(paths, f.new)
	}
	stat := &pullDiffBuffer{limit: 16384}
	if err := pullDiffCommand(ctx, repo, base, head, stat, "stat", paths...); err != nil {
		return item, err
	}
	add, del, binary, valid := pullFileStat(stat.Bytes(), f)
	if !valid {
		return item, nil
	}
	if binary {
		item.PatchState = "binary"
		return item, nil
	}
	item.Additions, item.Deletions = add, del
	patch := &pullDiffBuffer{limit: pullPatchLimit}
	err := pullDiffCommand(ctx, repo, base, head, patch, "patch", paths...)
	if err != nil && !patch.exceeded {
		return item, err
	}
	data := patch.Bytes()
	if patch.exceeded {
		// Clipping may split the final UTF-8 rune. Interior invalid bytes are
		// unrepresentable as text, not silently replaced by the JSON encoder.
		for len(data) > 0 && !utf8.Valid(data) && len(patch.Bytes())-len(data) < 4 {
			data = data[:len(data)-1]
		}
	}
	if !utf8.Valid(data) {
		item.PatchState = "unavailable"
		return item, nil
	}
	text := string(data)
	item.Patch = &text
	item.PatchState = "complete"
	if patch.exceeded {
		item.PatchState = "truncated"
	}
	return item, nil
}

func resolveRepositoryPullFiles(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryPullFiles, int) {
	q := *request.PullFiles
	comparison, status := openPullComparison(ctx, admission, request.RepositoryID, q.Index)
	if status != http.StatusOK {
		return nil, status
	}
	defer comparison.native.Close()
	if status := comparison.verifyObjects(); status != http.StatusOK {
		return nil, status
	}
	base, head, native := comparison.base, comparison.head, comparison.native
	if q.Base != "" && (q.Base != base || q.Head != head) {
		return nil, http.StatusConflict
	}
	id := strconv.FormatInt(comparison.pullID, 10)
	offset, status := comparison.offset(q.Cursor, 0)
	if status != http.StatusOK {
		return nil, status
	}
	raw := &pullDiffBuffer{limit: 2 * 1024 * 1024}
	err := pullDiffCommand(ctx, native, base, head, raw, "raw")
	if raw.exceeded {
		return nil, http.StatusRequestEntityTooLarge
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	files, valid := pullFileRecords(raw.Bytes())
	if !valid {
		return nil, http.StatusServiceUnavailable
	}
	if offset > 0 && offset >= len(files) {
		return nil, http.StatusBadRequest
	}
	end := min(offset+q.Limit, len(files))
	page := &extension.RepositoryPullFiles{RepositoryID: request.RepositoryID, PullID: id, Index: q.Index, Base: base, Head: head, Items: []extension.RepositoryPullFile{}}
	for _, f := range files[offset:end] {
		item, err := readPullFile(ctx, native, base, head, f)
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		page.Items = append(page.Items, item)
	}
	if end < len(files) {
		page.NextCursor = comparison.cursor(end)
	}
	if status := comparison.recheck(ctx, admission); status != http.StatusOK {
		return nil, status
	}
	return page, http.StatusOK
}
