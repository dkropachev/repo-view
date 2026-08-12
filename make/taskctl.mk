.PHONY: taskctl-build taskctl-test taskctl-inspect-executable-sha256 taskctl-generate-source-audit taskctl-generate-source-repository-bindings taskctl-generate-source-selections taskctl-validate-source-audit taskctl-validate-source-repository-bindings taskctl-validate-source-selections

# taskctl reads these values directly. Repeated values use exact compact JSON
# arrays so Make remains a fixed, declarative entrypoint.
# These targets are conveniences for an already-trusted host. The authenticated
# security boundary starts when the fixed root-owned static launcher is execed
# directly; Make and its implicit recipe shell are outside that trust boundary.
export TASKCTL_OUTPUT
export TASKCTL_INPUT
export TASKCTL_INPUT_SHA256
export TASKCTL_GIT_EXECUTABLE
export TASKCTL_GIT_SHA256
export TASKCTL_REPOSITORIES_JSON
export TASKCTL_REPOSITORY_BINDINGS
export TASKCTL_REPOSITORY_BINDINGS_SHA256
export TASKCTL_SOURCE_SELECTIONS
export TASKCTL_SOURCE_SELECTIONS_SHA256
export TASKCTL_EXECUTABLE_SHA256

# Development compilation only. This target does not admit either output for operational use.
taskctl-build:
	mkdir -p bin
	go build -trimpath -o bin/taskctl ./cmd/taskctl
	go build -trimpath -o bin/taskctl-launcher ./internal/cmd/taskctl-launcher

taskctl-test:
	go test ./internal/taskctl ./cmd/taskctl ./internal/taskctllauncher ./internal/cmd/taskctl-launcher ./benchmarks/tokenbench/source ./internal/projectcheck -count=1

taskctl-inspect-executable-sha256:
	/usr/local/libexec/scopesifter/taskctl-launcher inspect executable-sha256

taskctl-generate-source-audit:
	/usr/local/libexec/scopesifter/taskctl-launcher generate source-audit

taskctl-generate-source-repository-bindings:
	/usr/local/libexec/scopesifter/taskctl-launcher generate source-repository-bindings

taskctl-generate-source-selections:
	/usr/local/libexec/scopesifter/taskctl-launcher generate source-selections

taskctl-validate-source-audit:
	/usr/local/libexec/scopesifter/taskctl-launcher validate source-audit

taskctl-validate-source-repository-bindings:
	/usr/local/libexec/scopesifter/taskctl-launcher validate source-repository-bindings

taskctl-validate-source-selections:
	/usr/local/libexec/scopesifter/taskctl-launcher validate source-selections
