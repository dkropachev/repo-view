package navigationcommand

import "testing"

func TestValidatedScopeSifterSubcommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "plain", command: "scopesifter find Symbol --root . --json", want: "find"},
		{name: "single quoted shell", command: "/usr/bin/zsh -lc 'scopesifter changed --root . --base HEAD''^ --json'", want: "changed"},
		{name: "double quoted shell", command: `/usr/bin/zsh -lc "scopesifter inspect pkg/file.go:12 --root . --json"`, want: "inspect"},
		{name: "non navigation command", command: "go test ./..."},
		{name: "executable suffix", command: "notscopesifter find Symbol --json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidatedScopeSifterSubcommand(test.command)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ValidatedScopeSifterSubcommand(%q) = %q, want %q", test.command, got, test.want)
			}
		})
	}
}

func TestValidatedScopeSifterSubcommandRejectsUnsafeCommands(t *testing.T) {
	tests := []string{
		"printf fake scopesifter find Symbol --root . --json",
		"/usr/bin/zsh -lc 'for x in A B; do scopesifter find $x --root . --json; done'",
		"/usr/bin/zsh -lc 'scopesifter find A --root . --json && scopesifter find B --root . --json'",
		"/usr/bin/zsh -lc 'scopesifter find A --root . --json > /dev/null'",
		"/usr/bin/zsh -lc 'scopesifter find A --root .'",
		`scopesifter find "$SYMBOL" --root . --json`,
		`scopesifter find "$@" --root . --json`,
		`scopesifter find "$?" --root . --json`,
		`scopesifter find Symbol* --root . --json`,
		`scopesifter inspect pkg/\{one,two\}.go:1 --root . --json`,
		`scopesifter inspect pkg/file\ name.go:1 --root . --json`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			if subcommand, err := ValidatedScopeSifterSubcommand(command); err == nil {
				t.Fatalf("ValidatedScopeSifterSubcommand(%q) = %q, want error", command, subcommand)
			}
		})
	}
}
