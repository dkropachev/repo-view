package processpolicy

import "testing"

func TestValidateGitAcceptsReviewedReadAndPlumbingForms(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{
		{"rev-parse", "--verify", "HEAD^{commit}"},
		{"-C", "/repo", "status", "--porcelain=v1", "--untracked-files=no"},
		{"--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "diff.ignoreSubmodules=dirty", "diff", "--no-ext-diff", "HEAD", "--", "tool.sh"},
		{"config", "--local", "--get", "core.filemode"},
		GitRepositoryConfigArguments(),
		{"replace", "--list"},
		{"hash-object", "-w", "--no-filters", "--", "/work/file"},
		{"clone", "--depth", "1", "--no-tags", "--", "https://github.com/example/repo.git", "/tmp/repo"},
	} {
		if err := ValidateGit(arguments...); err != nil {
			t.Errorf("ValidateGit(%q) = %v", arguments, err)
		}
	}
}

func TestValidateGitWorktreeConfigRejectsDelegatingKeys(t *testing.T) {
	t.Parallel()
	if err := ValidateGitWorktreeConfig([]byte(
		"core.repositoryformatversion\x00user.name\x00remote.origin.url\x00",
	)); err != nil {
		t.Fatalf("ordinary local configuration rejected: %v", err)
	}
	for _, name := range []string{
		"alias.run", "core.attributesFile", "core.excludesFile", "core.worktree",
		"filter.answer.clean", "filter.answer.process", "include.path", "includeIf.onbranch:main.path",
		"diff.answer.command", "diff.answer.textconv", "merge.answer.driver",
		"difftool.answer.cmd", "gpg.ssh.program", "pager.status",
	} {
		if err := ValidateGitWorktreeConfig([]byte(name + "\x00")); err == nil {
			t.Errorf("delegating local configuration accepted: %s", name)
		}
	}
	for _, malformed := range [][]byte{[]byte("filter.answer.clean"), []byte("bad name\x00")} {
		if err := ValidateGitWorktreeConfig(malformed); err == nil {
			t.Errorf("malformed local configuration accepted: %q", malformed)
		}
	}
}

func TestValidateGitRejectsProgramDelegation(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{
		{"-c", "alias.pwn=!sh -c id", "pwn"},
		{"config", "alias.pwn", "!bash -c id"},
		{"diff", "--ext-diff", "HEAD"},
		{"show", "--textconv", "HEAD:file"},
		{"grep", "--open-files-in-pager=python3", "needle"},
		{"grep", "-O/bin/bash", "needle"},
		{"cat-file", "--filters", "HEAD:file"},
		{"cat-file", "--t", "HEAD:file"},
		{"diff", "--text", "HEAD"},
		{"grep", "--op=cat", "needle"},
		{"show", "--show-signature", "HEAD"},
		{"show", "--format=%G?", "HEAD"},
		{"for-each-ref", "--format=%(signature:grade)", "refs/heads"},
		{"clone", "--upload-pack=/tmp/tool", "https://example.invalid/repo", "/tmp/repo"},
		{"clone", "--depth", "1", "--no-tags", "--", "ext::bash -c id", "/tmp/repo"},
		{"difftool", "--tool", "evil"},
		{"replace", "HEAD", "other"},
		{"hash-object", "--path=tool.sh", "file"},
	} {
		if err := ValidateGit(arguments...); err == nil {
			t.Errorf("ValidateGit accepted %q", arguments)
		}
	}
}
