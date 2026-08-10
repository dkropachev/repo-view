package tokenbench

import (
	"strings"
	"testing"
)

func TestTreatmentNeutralityRejectsNamesPoliciesAndHints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		developer string
		prompt    string
	}{
		{prompt: "Use repo_view to inspect the implementation."},
		{prompt: "Call the repo-view MCP server first."},
		{prompt: "Explain the Model Context Protocol integration."},
		{prompt: "You have access to additional tools."},
		{prompt: "Prefer the navigation CLI over reading files."},
		{developer: "Do not use shell commands.", prompt: "Explain the change."},
		{developer: "Run git before answering.", prompt: "Explain the change."},
	}
	for index, test := range tests {
		if err := ValidateTreatmentNeutrality(
			test.developer,
			[]byte(test.prompt),
		); err == nil {
			t.Fatalf("treatment-bearing text %d was accepted", index)
		}
	}
}

func TestTreatmentNeutralityAllowsOrdinaryRepositoryTasks(t *testing.T) {
	t.Parallel()
	developer := "Answer using repository evidence. Be precise and concise."
	prompt := []byte(
		"Find why the server command fails, explain the data flow, and cite relevant paths.",
	)
	if err := ValidateTreatmentNeutrality(developer, prompt); err != nil {
		t.Fatalf("ordinary repository task rejected: %v", err)
	}
}

func TestSuiteValidationRejectsTreatmentBearingDeveloperInstructions(t *testing.T) {
	t.Parallel()
	suite := validLoadedSuite().suite
	suite.DeveloperInstructions = "Use the available tools before answering."
	if err := suite.Validate(); err == nil ||
		!strings.Contains(err.Error(), "tool") {
		t.Fatalf("suite validation error = %v, want tool-policy rejection", err)
	}
}
