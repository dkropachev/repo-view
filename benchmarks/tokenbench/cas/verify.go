package cas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

func syncObjectAt(directory *os.Root, name string) error {
	object, err := directory.Open(name)
	if err != nil {
		return fmt.Errorf("open object %s for sync: %w", name, err)
	}
	syncErr := object.Sync()
	closeErr := object.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(
			wrapError("sync object "+name, syncErr),
			wrapError("close synced object "+name, closeErr),
		)
	}
	return nil
}

func (store *Store) verifyObject(
	ctx context.Context,
	ref ObjectRef,
	destination io.Writer,
) (resultErr error) {
	hexDigest := ref.hexDigest()
	shard, _, err := store.openShard(hexDigest, false)
	if err != nil {
		return fmt.Errorf("%w: open object shard: %w", ErrIntegrity, err)
	}
	defer func() {
		resultErr = joinCloseError(resultErr, "close object shard after verification", shard)
	}()
	return verifyFileAt(ctx, shard, hexDigest[2:], ref, destination)
}

func verifyFileAt(
	ctx context.Context,
	directory *os.Root,
	name string,
	ref ObjectRef,
	destination io.Writer,
) error {
	before, err := directory.Lstat(name)
	if err != nil {
		return fmt.Errorf("%w: lstat object %s: %w", ErrIntegrity, name, err)
	}
	if err := validateObjectInfo(name, before); err != nil {
		return err
	}

	object, err := directory.Open(name)
	if err != nil {
		return fmt.Errorf("%w: open object %s: %w", ErrIntegrity, name, err)
	}
	opened, err := object.Stat()
	if err != nil {
		object.Close()
		return fmt.Errorf("%w: stat opened object %s: %w", ErrIntegrity, name, err)
	}
	if err := validateObjectInfo(name, opened); err != nil {
		object.Close()
		return err
	}
	if !os.SameFile(before, opened) || !sameObjectMetadata(before, opened) {
		object.Close()
		return fmt.Errorf("%w: object %s changed while opening", ErrIntegrity, name)
	}
	if opened.Size() != ref.Size {
		object.Close()
		return fmt.Errorf(
			"%w: object %s has size %d, want %d",
			ErrIntegrity,
			name,
			opened.Size(),
			ref.Size,
		)
	}

	digest, size, readErr := digestAndCopy(ctx, object, destination, ref.Size)
	closeErr := object.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return fmt.Errorf("read object %s: close: %w", name, closeErr)
	}
	if size != ref.Size {
		return fmt.Errorf(
			"%w: object %s yielded %d bytes, want %d",
			ErrIntegrity,
			name,
			size,
			ref.Size,
		)
	}
	if digest != ref.hexDigest() {
		return fmt.Errorf(
			"%w: object %s digest is %s, want %s",
			ErrIntegrity,
			name,
			digest,
			ref.hexDigest(),
		)
	}

	after, err := directory.Lstat(name)
	if err != nil {
		return fmt.Errorf("%w: re-lstat object %s: %w", ErrIntegrity, name, err)
	}
	if err := validateObjectInfo(name, after); err != nil {
		return err
	}
	if !os.SameFile(opened, after) || !sameObjectMetadata(opened, after) {
		return fmt.Errorf("%w: object %s changed while reading", ErrIntegrity, name)
	}
	return nil
}

func validateObjectInfo(name string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: object %s is not a regular file", ErrIntegrity, name)
	}
	if info.Mode().Perm() != objectFileMode {
		return fmt.Errorf(
			"%w: object %s has mode %s, want %s",
			ErrIntegrity,
			name,
			info.Mode().Perm(),
			objectFileMode,
		)
	}
	if multipleLinks(info) {
		return fmt.Errorf(
			"%w: %w: object %s",
			ErrIntegrity,
			errMultipleLinks,
			name,
		)
	}
	return nil
}

func sameObjectMetadata(first, second os.FileInfo) bool {
	return first.Mode() == second.Mode() &&
		first.Size() == second.Size() &&
		first.ModTime().Equal(second.ModTime())
}

func digestAndCopy(
	ctx context.Context,
	source io.Reader,
	destination io.Writer,
	expectedSize int64,
) (string, int64, error) {
	hash := sha256.New()
	var buffer [32 * 1024]byte
	var size int64
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", size, err
		}
		read, readErr := source.Read(buffer[:])
		if read < 0 || read > len(buffer) {
			return "", size, errors.New("CAS object reader returned an invalid byte count")
		}
		if read > 0 {
			emptyReads = 0
			if int64(read) > expectedSize-size {
				return "", size, fmt.Errorf(
					"%w: object contains more than %d bytes",
					ErrIntegrity,
					expectedSize,
				)
			}
			chunk := buffer[:read]
			if _, err := hash.Write(chunk); err != nil {
				return "", size, fmt.Errorf("hash CAS object: %w", err)
			}
			written, err := destination.Write(chunk)
			if err != nil {
				return "", size, fmt.Errorf("copy CAS object: %w", err)
			}
			if written != len(chunk) {
				return "", size, io.ErrShortWrite
			}
			size += int64(read)
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return "", size, io.ErrNoProgress
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", size, fmt.Errorf("read CAS object: %w", readErr)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
