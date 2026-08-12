.PHONY: tokenbench-privileged-linux

tokenbench-privileged-linux:
	go run -mod=readonly ./benchmarks/tokenbench/cmd/privileged-linux-tests
