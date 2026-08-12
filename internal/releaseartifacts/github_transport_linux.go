//go:build linux

package releaseartifacts

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const releaseCABundlePath = "/etc/ssl/certs/ca-certificates.crt"

func newReleaseHTTPTransport() (*http.Transport, error) {
	roots, err := loadReleaseSystemRoots()
	if err != nil {
		return nil, fmt.Errorf("load trusted release TLS roots: %w", err)
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}, nil
}

func loadReleaseSystemRoots() (*x509.CertPool, error) {
	canonical, err := filepath.EvalSymlinks(releaseCABundlePath)
	if err != nil || canonical != releaseCABundlePath {
		return nil, errors.New("release CA bundle path is absent, noncanonical, or symbolic")
	}
	for component := releaseCABundlePath; ; component = filepath.Dir(component) {
		info, err := os.Lstat(component)
		if err != nil {
			return nil, fmt.Errorf("inspect release CA path component %s: %w", component, err)
		}
		status, ok := info.Sys().(*syscall.Stat_t)
		if !ok || status.Uid != 0 || info.Mode()&os.ModeSymlink != 0 ||
			info.Mode().Perm()&0o022 != 0 ||
			info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, fmt.Errorf("release CA path component %s is not root-controlled", component)
		}
		if component == string(filepath.Separator) {
			break
		}
	}
	file, err := os.Open(releaseCABundlePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4<<20 {
		return nil, errors.Join(errors.New("release CA bundle identity or size is unsafe"), err)
	}
	visible, err := os.Lstat(releaseCABundlePath)
	if err != nil || !os.SameFile(info, visible) || visible.Mode() != info.Mode() ||
		visible.Size() != info.Size() {
		return nil, errors.Join(errors.New("release CA bundle changed while opening"), err)
	}
	content, err := io.ReadAll(io.LimitReader(file, info.Size()+1))
	if err != nil || int64(len(content)) != info.Size() {
		return nil, errors.Join(errors.New("read complete release CA bundle"), err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(content) {
		return nil, errors.New("release CA bundle contains no certificates")
	}
	return pool, nil
}
