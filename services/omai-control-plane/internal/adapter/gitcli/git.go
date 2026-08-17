package gitcli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

type Repository struct {
	workspaces port.WorkspaceRepository
	commands   port.WorkspaceCommandRunner
	maxOutput  int
}

const managedWorktreeRoot = ".git/omai-worktrees"

func New(workspaces port.WorkspaceRepository, commands port.WorkspaceCommandRunner, maxOutput int) *Repository {
	return &Repository{workspaces: workspaces, commands: commands, maxOutput: maxOutput}
}

func (r *Repository) Init(ctx context.Context, principal domain.Principal, workspaceID string) (domain.GitStatus, error) {
	workspace, err := r.workspaces.Get(ctx, principal, workspaceID)
	if err != nil {
		return domain.GitStatus{}, err
	}
	if workspace.RepoRoot == "" {
		result, runErr := r.commands.Run(ctx, principal, domain.CommandSpec{
			WorkspaceID: workspace.ID, Command: "git", Args: []string{"init", "--quiet", "--"}, MaxOutputBytes: r.maxOutput,
		})
		if runErr != nil {
			return domain.GitStatus{}, runErr
		}
		if result.ExitCode != 0 {
			return domain.GitStatus{}, fmt.Errorf("git init exited with code %d: %s", result.ExitCode, strings.TrimSpace(string(result.Output)))
		}
		workspace, err = r.workspaces.Resolve(ctx, principal, workspace.Root)
		if err != nil {
			return domain.GitStatus{}, err
		}
		if workspace.RepoRoot == "" {
			return domain.GitStatus{}, fmt.Errorf("%w: git repository was not detected after initialization", domain.ErrUnavailable)
		}
	}
	return r.Status(ctx, principal, workspace.ID)
}

func (r *Repository) Status(ctx context.Context, principal domain.Principal, workspaceID string) (domain.GitStatus, error) {
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return domain.GitStatus{}, err
	}
	commandResult, err := r.git(ctx, principal, workspace, cwd, "status", "--porcelain=v1", "--branch", "-z")
	if err != nil {
		return domain.GitStatus{}, err
	}
	parts := strings.Split(string(commandResult.Output), "\x00")
	result := domain.GitStatus{}
	for _, part := range parts {
		if strings.HasPrefix(part, "## ") {
			result.Branch = strings.TrimPrefix(strings.SplitN(strings.TrimPrefix(part, "## "), "...", 2)[0], "No commits yet on ")
			continue
		}
		if len(part) >= 4 {
			result.Files = append(result.Files, domain.GitFileStatus{Status: part[:2], Path: part[3:]})
		}
	}
	result.DefaultBranch = r.defaultBranch(ctx, principal, workspace, cwd, result.Branch)
	return result, nil
}
func (r *Repository) Diff(ctx context.Context, principal domain.Principal, workspaceID, path string, staged bool) (string, error) {
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return "", err
	}
	args := []string{"diff", "--no-ext-diff"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	if path != "" {
		if !safeRelative(path) {
			return "", domain.ErrInvalid
		}
		args = append(args, path)
	}
	result, err := r.git(ctx, principal, workspace, cwd, args...)
	return string(result.Output), err
}

func (r *Repository) DiffFiles(ctx context.Context, principal domain.Principal, workspaceID, mode, path string, contextLines int) ([]domain.GitFileDiff, error) {
	if mode != "git" && mode != "branch" {
		return nil, fmt.Errorf("%w: diff mode must be git or branch", domain.ErrInvalid)
	}
	if path != "" && !safeRelative(path) {
		return nil, fmt.Errorf("%w: invalid diff path", domain.ErrInvalid)
	}
	if contextLines == 0 {
		contextLines = 3
	}
	if contextLines < 0 || contextLines > 10_000 {
		return nil, fmt.Errorf("%w: context lines must be between 0 and 10000", domain.ErrInvalid)
	}

	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	status, err := r.Status(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	reference, err := r.diffReference(ctx, principal, workspace, cwd, mode, status)
	if err != nil {
		return nil, err
	}

	items := make(map[string]string)
	if reference != "" {
		args := []string{"diff", "--name-status", "-z", "--no-renames", reference, "--"}
		if path != "" {
			args = append(args, path)
		}
		result, runErr := r.git(ctx, principal, workspace, cwd, args...)
		if runErr != nil {
			return nil, runErr
		}
		for file, fileStatus := range parseNameStatus(result.Output) {
			items[file] = fileStatus
		}
	}
	for _, file := range status.Files {
		if file.Status != "??" || !matchesPath(file.Path, path) {
			continue
		}
		items[file.Path] = "added"
	}

	paths := make([]string, 0, len(items))
	for file := range items {
		paths = append(paths, file)
	}
	sort.Strings(paths)

	result := make([]domain.GitFileDiff, 0, len(paths))
	total := 0
	for _, file := range paths {
		patch, patchErr := r.filePatch(ctx, principal, workspace, cwd, reference, file, items[file] == "added" && isUntracked(status.Files, file), contextLines)
		if patchErr != nil {
			return nil, patchErr
		}
		additions, deletions := patchStats(patch)
		if total+len(patch) > r.maxOutput {
			patch = ""
		} else {
			total += len(patch)
		}
		result = append(result, domain.GitFileDiff{
			File: file, Patch: patch, Additions: additions, Deletions: deletions, Status: items[file],
		})
	}
	return result, nil
}
func (r *Repository) Stage(ctx context.Context, principal domain.Principal, workspaceID string, paths []string, all bool) (domain.GitStatus, error) {
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return domain.GitStatus{}, err
	}
	paths, err = validatedPaths(paths, all)
	if err != nil {
		return domain.GitStatus{}, err
	}
	args := []string{"add"}
	if all {
		args = append(args, "--all")
	}
	args = append(args, "--")
	args = append(args, paths...)
	if _, err := r.git(ctx, principal, workspace, cwd, args...); err != nil {
		return domain.GitStatus{}, err
	}
	return r.Status(ctx, principal, workspaceID)
}
func (r *Repository) Unstage(ctx context.Context, principal domain.Principal, workspaceID string, paths []string, all bool) (domain.GitStatus, error) {
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return domain.GitStatus{}, err
	}
	paths, err = validatedPaths(paths, all)
	if err != nil {
		return domain.GitStatus{}, err
	}
	if all {
		paths = []string{"."}
	}
	if _, headErr := r.git(ctx, principal, workspace, cwd, "rev-parse", "--verify", "HEAD"); headErr == nil {
		args := append([]string{"reset", "--quiet", "HEAD", "--"}, paths...)
		if _, err := r.git(ctx, principal, workspace, cwd, args...); err != nil {
			return domain.GitStatus{}, err
		}
	} else {
		args := append([]string{"rm", "--cached", "--ignore-unmatch", "-r", "--"}, paths...)
		if _, err := r.git(ctx, principal, workspace, cwd, args...); err != nil {
			return domain.GitStatus{}, err
		}
	}
	return r.Status(ctx, principal, workspaceID)
}
func (r *Repository) Commit(ctx context.Context, principal domain.Principal, workspaceID, message string) (string, error) {
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, '\x00') {
		return "", domain.ErrInvalid
	}
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return "", err
	}
	if _, err := r.git(ctx, principal, workspace, cwd, "commit", "--message", message); err != nil {
		return "", err
	}
	result, err := r.git(ctx, principal, workspace, cwd, "rev-parse", "HEAD")
	return strings.TrimSpace(string(result.Output)), err
}
func (r *Repository) ListWorktrees(ctx context.Context, principal domain.Principal, workspaceID string) ([]domain.Worktree, error) {
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	commandResult, err := r.git(ctx, principal, workspace, cwd, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(commandResult.Output), "\x00")
	var result []domain.Worktree
	var item domain.Worktree
	for _, field := range fields {
		switch {
		case strings.HasPrefix(field, "worktree "):
			if item.Path != "" {
				result = append(result, item)
			}
			path, err := publicExecutionPath(commandResult.WorkspaceRoot, workspace.Root, strings.TrimPrefix(field, "worktree "))
			if err != nil {
				return nil, err
			}
			item = domain.Worktree{Path: path}
		case strings.HasPrefix(field, "HEAD "):
			item.Head = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch refs/heads/"):
			item.Branch = strings.TrimPrefix(field, "branch refs/heads/")
		}
	}
	if item.Path != "" {
		result = append(result, item)
	}
	return result, nil
}
func (r *Repository) CreateWorktree(ctx context.Context, principal domain.Principal, workspaceID, branch, base string) (domain.Worktree, error) {
	if !safeRef(branch) || (base != "" && !safeRef(base)) {
		return domain.Worktree{}, domain.ErrInvalid
	}
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return domain.Worktree{}, err
	}
	if cwd != "" {
		return domain.Worktree{}, fmt.Errorf("%w: managed worktrees require the workspace root to be the repository root", domain.ErrInvalid)
	}
	relativePath := filepath.ToSlash(filepath.Join(filepath.FromSlash(managedWorktreeRoot), filepath.FromSlash(branch)))
	path := filepath.Join(workspace.Root, filepath.FromSlash(relativePath))
	args := []string{"worktree", "add", "-b", branch, relativePath}
	if base != "" {
		args = append(args, base)
	}
	if _, err := r.git(ctx, principal, workspace, cwd, args...); err != nil {
		return domain.Worktree{}, err
	}
	result, err := r.git(ctx, principal, workspace, relativePath, "rev-parse", "HEAD")
	return domain.Worktree{Path: path, Branch: branch, Head: strings.TrimSpace(string(result.Output))}, err
}
func (r *Repository) RemoveWorktree(ctx context.Context, principal domain.Principal, workspaceID, path string) error {
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return err
	}
	expected := filepath.Join(workspace.Root, filepath.FromSlash(managedWorktreeRoot)) + string(filepath.Separator)
	absolute, err := filepath.Abs(path)
	if err != nil || !strings.HasPrefix(filepath.Clean(absolute)+string(filepath.Separator), expected) {
		return domain.ErrForbidden
	}
	relative, err := filepath.Rel(workspace.Root, absolute)
	if err != nil {
		return domain.ErrInvalid
	}
	_, err = r.git(ctx, principal, workspace, cwd, "worktree", "remove", "--", filepath.ToSlash(relative))
	return err
}
func (r *Repository) Merge(ctx context.Context, principal domain.Principal, workspaceID, branch string) (string, bool, error) {
	if !safeRef(branch) {
		return "", false, domain.ErrInvalid
	}
	workspace, cwd, err := r.root(ctx, principal, workspaceID)
	if err != nil {
		return "", false, err
	}
	before, err := r.git(ctx, principal, workspace, cwd, "rev-parse", "HEAD")
	if err != nil {
		return "", false, err
	}
	if _, err := r.git(ctx, principal, workspace, cwd, "merge", "--no-edit", "--", branch); err != nil {
		return "", false, err
	}
	after, err := r.git(ctx, principal, workspace, cwd, "rev-parse", "HEAD")
	return strings.TrimSpace(string(after.Output)), strings.TrimSpace(string(before.Output)) != strings.TrimSpace(string(after.Output)), err
}
func (r *Repository) root(ctx context.Context, principal domain.Principal, id string) (domain.Workspace, string, error) {
	workspace, err := r.workspaces.Get(ctx, principal, id)
	if err != nil {
		return domain.Workspace{}, "", err
	}
	if workspace.RepoRoot == "" {
		return domain.Workspace{}, "", fmt.Errorf("%w: workspace is not a git repository", domain.ErrInvalid)
	}
	relative, err := filepath.Rel(workspace.Root, workspace.RepoRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return domain.Workspace{}, "", fmt.Errorf("%w: repository root escaped workspace", domain.ErrForbidden)
	}
	if relative == "." {
		relative = ""
	}
	return workspace, filepath.ToSlash(relative), nil
}
func (r *Repository) git(ctx context.Context, principal domain.Principal, workspace domain.Workspace, cwd string, args ...string) (domain.CommandResult, error) {
	result, err := r.gitResult(ctx, principal, workspace, cwd, args...)
	if err != nil {
		return domain.CommandResult{}, err
	}
	if result.ExitCode != 0 {
		return domain.CommandResult{}, fmt.Errorf("git %s exited with code %d: %s", args[0], result.ExitCode, strings.TrimSpace(string(result.Output)))
	}
	return result, nil
}

func (r *Repository) gitResult(ctx context.Context, principal domain.Principal, workspace domain.Workspace, cwd string, args ...string) (domain.CommandResult, error) {
	return r.commands.Run(ctx, principal, domain.CommandSpec{
		WorkspaceID: workspace.ID, Command: "git", Args: append([]string{"-C", ".", "--no-pager"}, args...),
		CWD: cwd, MaxOutputBytes: r.maxOutput,
	})
}

func (r *Repository) defaultBranch(ctx context.Context, principal domain.Principal, workspace domain.Workspace, cwd, current string) string {
	remote, err := r.gitResult(ctx, principal, workspace, cwd, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil && remote.ExitCode == 0 {
		value := strings.TrimSpace(string(remote.Output))
		if name := strings.TrimPrefix(value, "origin/"); name != value && safeRef(name) {
			return name
		}
	}
	for _, candidate := range []string{"main", "master"} {
		found, foundErr := r.gitResult(ctx, principal, workspace, cwd, "show-ref", "--verify", "--quiet", "refs/heads/"+candidate)
		if foundErr == nil && found.ExitCode == 0 {
			return candidate
		}
	}
	if safeRef(current) {
		return current
	}
	return ""
}

func (r *Repository) diffReference(ctx context.Context, principal domain.Principal, workspace domain.Workspace, cwd, mode string, status domain.GitStatus) (string, error) {
	if mode == "git" {
		head, err := r.gitResult(ctx, principal, workspace, cwd, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return "", err
		}
		if head.ExitCode != 0 {
			return "", nil
		}
		return "HEAD", nil
	}
	if status.DefaultBranch == "" {
		return "", fmt.Errorf("%w: default branch is unavailable", domain.ErrNotFound)
	}
	for _, reference := range []string{"refs/remotes/origin/" + status.DefaultBranch, "refs/heads/" + status.DefaultBranch} {
		found, err := r.gitResult(ctx, principal, workspace, cwd, "show-ref", "--verify", "--quiet", reference)
		if err != nil {
			return "", err
		}
		if found.ExitCode == 0 {
			return reference, nil
		}
	}
	return "", fmt.Errorf("%w: default branch reference is unavailable", domain.ErrNotFound)
}

func (r *Repository) filePatch(ctx context.Context, principal domain.Principal, workspace domain.Workspace, cwd, reference, path string, untracked bool, contextLines int) (string, error) {
	contextArg := "--unified=" + strconv.Itoa(contextLines)
	args := []string{"diff", "--no-ext-diff", "--no-color", "--no-renames", contextArg}
	if untracked {
		args = append(args, "--no-index", "--", "/dev/null", path)
	} else {
		args = append(args, reference, "--", path)
	}
	result, err := r.gitResult(ctx, principal, workspace, cwd, args...)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 && !(untracked && result.ExitCode == 1) {
		return "", fmt.Errorf("git diff exited with code %d: %s", result.ExitCode, strings.TrimSpace(string(result.Output)))
	}
	return string(result.Output), nil
}

func parseNameStatus(output []byte) map[string]string {
	fields := strings.Split(string(output), "\x00")
	result := make(map[string]string)
	for index := 0; index+1 < len(fields); index += 2 {
		code, path := fields[index], fields[index+1]
		if code == "" || path == "" {
			continue
		}
		status := "modified"
		switch code[0] {
		case 'A':
			status = "added"
		case 'D':
			status = "deleted"
		}
		result[filepath.ToSlash(path)] = status
	}
	return result
}

func matchesPath(file, filter string) bool {
	if filter == "" || file == filter {
		return true
	}
	return strings.HasPrefix(file, strings.TrimSuffix(filter, "/")+"/")
}

func isUntracked(files []domain.GitFileStatus, path string) bool {
	for _, file := range files {
		if file.Path == path && file.Status == "??" {
			return true
		}
	}
	return false
}

func patchStats(patch string) (int32, int32) {
	var additions, deletions int32
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '+':
			additions++
		case '-':
			deletions++
		}
	}
	return additions, deletions
}

func publicExecutionPath(executionRoot, publicRoot, value string) (string, error) {
	if executionRoot == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("%w: executor returned an invalid worktree path", domain.ErrUnavailable)
	}
	relative, err := filepath.Rel(filepath.Clean(executionRoot), filepath.Clean(value))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: executor worktree escaped the workspace", domain.ErrUnavailable)
	}
	return filepath.Join(publicRoot, relative), nil
}
func safeRelative(value string) bool {
	return value != "" && !filepath.IsAbs(value) && filepath.Clean(value) != ".." && !strings.HasPrefix(filepath.Clean(value), ".."+string(filepath.Separator))
}
func validatedPaths(paths []string, all bool) ([]string, error) {
	if all && len(paths) != 0 {
		return nil, domain.ErrInvalid
	}
	if !all && len(paths) == 0 {
		return nil, domain.ErrInvalid
	}
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		clean := filepath.ToSlash(filepath.Clean(path))
		if !safeRelative(clean) {
			return nil, domain.ErrInvalid
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		result = append(result, clean)
	}
	return result, nil
}
func safeRef(value string) bool {
	if value == "" || len(value) > 200 || strings.HasPrefix(value, "-") || strings.ContainsAny(value, " ~^:?*[\\\x00\r\n") || strings.Contains(value, "..") || strings.HasSuffix(value, ".lock") {
		return false
	}
	_, err := strconv.Unquote(strconv.Quote(value))
	return err == nil
}
