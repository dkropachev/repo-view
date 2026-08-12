//go:build linux

package workspace

import (
	"errors"
	"fmt"
	"strings"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

const (
	captureScratchFixedBlocks = 64
	captureScratchFixedInodes = 64
)

type captureScratchEstimate struct {
	bytes  uint64
	inodes uint64
}

func (arm *ArmAuthority) captureScratchAvailable(result []worktreeEntry) (bool, error) {
	if arm.capture == nil {
		return false, errors.New("workspace capture scratch descriptor is absent")
	}
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(arm.capture.Fd()), &stats); err != nil {
		return false, fmt.Errorf("read workspace capture scratch capacity: %w", err)
	}
	if stats.Type != unix.TMPFS_MAGIC || stats.Bsize <= 0 {
		return false, errors.New("workspace capture scratch is not on the bounded tmpfs")
	}
	blockSize := uint64(stats.Bsize)
	estimate, err := estimateCaptureScratch(
		arm.pair.baseManifest,
		result,
		len(arm.pair.baseRevision),
		blockSize,
	)
	if err != nil {
		return false, err
	}
	availableBytes, ok := captureCheckedMultiply(stats.Bavail, blockSize)
	if !ok {
		return false, errors.New("workspace capture scratch capacity overflows")
	}
	return estimate.bytes <= availableBytes && estimate.inodes <= stats.Ffree, nil
}

func captureFileUpdates(base, result []worktreeEntry) []worktreeEntry {
	// The immutable snapshot authenticated every base file's raw bytes and mode,
	// and the frozen-tree scan independently digested every result file. Exact
	// matches may therefore retain the blob already loaded from baseRevision;
	// hashing them again would duplicate the whole source tree in bounded scratch.
	baseFiles := make(map[string]worktreeEntry, len(base))
	for _, entry := range base {
		if entry.kind == snapshot.ManifestKindFile {
			baseFiles[entry.path] = entry
		}
	}
	updates := make([]worktreeEntry, 0)
	for _, entry := range result {
		if entry.kind != snapshot.ManifestKindFile {
			continue
		}
		previous, exists := baseFiles[entry.path]
		if !exists || previous.digest != entry.digest || previous.mode != entry.mode {
			updates = append(updates, entry)
		}
	}
	return updates
}

func estimateCaptureScratch(
	base, result []worktreeEntry,
	objectIDLength int,
	blockSize uint64,
) (captureScratchEstimate, error) {
	if objectIDLength != 40 && objectIDLength != 64 {
		return captureScratchEstimate{}, errors.New("capture scratch object format is invalid")
	}
	if blockSize == 0 {
		return captureScratchEstimate{}, errors.New("capture scratch block size is invalid")
	}
	estimate := captureScratchEstimate{
		bytes:  captureScratchFixedBlocks * blockSize,
		inodes: captureScratchFixedInodes,
	}
	baseFiles := make(map[string]worktreeEntry, len(base))
	for _, entry := range base {
		if entry.kind == snapshot.ManifestKindFile {
			baseFiles[entry.path] = entry
		}
	}
	var largestObject uint64
	objectCount := uint64(0)
	for _, entry := range captureFileUpdates(base, result) {
		previous, exists := baseFiles[entry.path]
		if exists && previous.digest == entry.digest {
			continue
		}
		if entry.size < 0 {
			return captureScratchEstimate{}, errors.New("capture blob size is negative")
		}
		objectBytes, err := captureLooseObjectBlocks(uint64(entry.size), blockSize)
		if err != nil || !captureAdd(&estimate.bytes, objectBytes) {
			return captureScratchEstimate{}, errors.Join(
				errors.New("capture blob storage estimate overflows"),
				err,
			)
		}
		largestObject = max(largestObject, objectBytes)
		objectCount++
	}

	treePayloads, err := captureTreePayloadBounds(result, objectIDLength)
	if err != nil {
		return captureScratchEstimate{}, err
	}
	for _, payload := range treePayloads {
		objectBytes, objectErr := captureLooseObjectBlocks(payload, blockSize)
		if objectErr != nil || !captureAdd(&estimate.bytes, objectBytes) {
			return captureScratchEstimate{}, errors.Join(
				errors.New("capture tree storage estimate overflows"),
				objectErr,
			)
		}
		largestObject = max(largestObject, objectBytes)
		objectCount++
	}
	if !captureAdd(&estimate.bytes, largestObject) {
		return captureScratchEstimate{}, errors.New("capture transient object estimate overflows")
	}

	baseIndex, err := captureIndexBound(base, objectIDLength)
	if err != nil {
		return captureScratchEstimate{}, err
	}
	resultIndex, err := captureIndexBound(result, objectIDLength)
	if err != nil {
		return captureScratchEstimate{}, err
	}
	indexBytes, err := captureRoundToBlock(max(baseIndex, resultIndex), blockSize)
	if err != nil {
		return captureScratchEstimate{}, err
	}
	indexBytes, ok := captureCheckedMultiply(indexBytes, 3)
	if !ok || !captureAdd(&estimate.bytes, indexBytes) {
		return captureScratchEstimate{}, errors.New("capture index storage estimate overflows")
	}

	fanoutDirectories := min(objectCount, 256)
	directoryBytes, ok := captureCheckedMultiply(
		fanoutDirectories+uint64(len(treePayloads)),
		blockSize,
	)
	if !ok || !captureAdd(&estimate.bytes, directoryBytes) {
		return captureScratchEstimate{}, errors.New("capture directory storage estimate overflows")
	}
	safetyBytes, err := captureRoundToBlock(estimate.bytes/8, blockSize)
	if err != nil || !captureAdd(&estimate.bytes, safetyBytes) {
		return captureScratchEstimate{}, errors.Join(
			errors.New("capture storage safety estimate overflows"),
			err,
		)
	}
	estimate.inodes += objectCount + fanoutDirectories + uint64(len(treePayloads))
	return estimate, nil
}

func captureTreePayloadBounds(
	entries []worktreeEntry,
	objectIDLength int,
) (map[string]uint64, error) {
	payloads := map[string]uint64{".": 0}
	objectIDBytes := uint64(objectIDLength / 2)
	for _, entry := range entries {
		if entry.path == "." {
			continue
		}
		separator := strings.LastIndexByte(entry.path, '/')
		parent, name := ".", entry.path
		if separator >= 0 {
			parent, name = entry.path[:separator], entry.path[separator+1:]
		}
		if entry.kind == snapshot.ManifestKindDirectory {
			if _, exists := payloads[entry.path]; !exists {
				payloads[entry.path] = 0
			}
		}
		entryBytes := uint64(len(name)) + objectIDBytes + 16
		value := payloads[parent]
		if !captureAdd(&value, entryBytes) {
			return nil, errors.New("capture tree payload estimate overflows")
		}
		payloads[parent] = value
	}
	return payloads, nil
}

func captureIndexBound(entries []worktreeEntry, objectIDLength int) (uint64, error) {
	total := uint64(4 << 10)
	for _, entry := range entries {
		if entry.path == "." {
			continue
		}
		entryBytes := uint64(len(entry.path) + objectIDLength/2 + 128)
		rounded, err := captureRoundToBlock(entryBytes, 8)
		if err != nil || !captureAdd(&total, rounded) {
			return 0, errors.Join(errors.New("capture index estimate overflows"), err)
		}
	}
	return total, nil
}

func captureLooseObjectBlocks(payload, blockSize uint64) (uint64, error) {
	source, ok := captureCheckedAdd(payload, 64)
	if !ok {
		return 0, errors.New("capture object header estimate overflows")
	}
	compressed := source
	for _, overhead := range []uint64{source >> 12, source >> 14, source >> 25, 13} {
		if !captureAdd(&compressed, overhead) {
			return 0, errors.New("capture compressed object estimate overflows")
		}
	}
	return captureRoundToBlock(compressed, blockSize)
}

func captureRoundToBlock(value, blockSize uint64) (uint64, error) {
	if blockSize == 0 {
		return 0, errors.New("capture block size is zero")
	}
	withPadding, ok := captureCheckedAdd(value, blockSize-1)
	if !ok {
		return 0, errors.New("capture block rounding overflows")
	}
	return withPadding / blockSize * blockSize, nil
}

func captureAdd(target *uint64, value uint64) bool {
	updated, ok := captureCheckedAdd(*target, value)
	if ok {
		*target = updated
	}
	return ok
}

func captureCheckedAdd(left, right uint64) (uint64, bool) {
	if left > ^uint64(0)-right {
		return 0, false
	}
	return left + right, true
}

func captureCheckedMultiply(left, right uint64) (uint64, bool) {
	if left != 0 && right > ^uint64(0)/left {
		return 0, false
	}
	return left * right, true
}
