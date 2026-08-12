.PHONY: release-artifacts release-publish

release-artifacts:
	go run -mod=readonly ./internal/cmd/release-artifacts -mode build

release-publish:
	go run -mod=readonly ./internal/cmd/release-artifacts -mode publish
