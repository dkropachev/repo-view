package releaseartifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIBase          = "https://api.github.com"
	githubUploadBase       = "https://uploads.github.com"
	githubAPIVersion       = "2026-03-10"
	maximumGitHubJSONBytes = 4 << 20
	maximumReleasePages    = 100
	maximumTagDepth        = 16
)

type githubReleasePublisher struct {
	apiBase       *url.URL
	uploadBase    *url.URL
	client        *http.Client
	token         string
	allowTestHTTP bool
}

type githubObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}

type githubReference struct {
	Object githubObject `json:"object"`
	Ref    string       `json:"ref"`
}

type githubTag struct {
	Object githubObject `json:"object"`
}

type githubImmutableReleaseSettings struct {
	Enabled         bool `json:"enabled"`
	EnforcedByOwner bool `json:"enforced_by_owner"`
}

type githubRelease struct {
	PublishedAt *string `json:"published_at"`
	AssetsURL   string  `json:"assets_url"`
	Name        string  `json:"name"`
	TagName     string  `json:"tag_name"`
	UploadURL   string  `json:"upload_url"`
	URL         string  `json:"url"`
	ID          int64   `json:"id"`
	Draft       bool    `json:"draft"`
	Immutable   bool    `json:"immutable"`
	Prerelease  bool    `json:"prerelease"`
}

type githubReleaseAsset struct {
	Digest string `json:"digest"`
	Name   string `json:"name"`
	State  string `json:"state"`
	URL    string `json:"url"`
	ID     int64  `json:"id"`
	Size   int64  `json:"size"`
}

func publishGitHubRelease(refName, commit, token string, artifacts []openReleaseArtifact) error {
	publisher, err := newGitHubReleasePublisher(token)
	if err != nil {
		return err
	}
	return publisher.publish(refName, commit, artifacts)
}

func newGitHubReleasePublisher(token string) (*githubReleasePublisher, error) {
	if token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return nil, errors.New("release publication requires a valid GH_TOKEN")
	}
	apiBase, err := url.Parse(githubAPIBase)
	if err != nil {
		return nil, err
	}
	uploadBase, err := url.Parse(githubUploadBase)
	if err != nil {
		return nil, err
	}
	transport, err := newReleaseHTTPTransport()
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &githubReleasePublisher{
		apiBase: apiBase, uploadBase: uploadBase, client: client, token: token,
	}, nil
}

func (publisher *githubReleasePublisher) publish(
	refName, commit string,
	artifacts []openReleaseArtifact,
) (resultErr error) {
	if publisher == nil || publisher.client == nil || publisher.apiBase == nil ||
		publisher.uploadBase == nil {
		return errors.New("GitHub release publisher is incomplete")
	}
	if _, err := versionFromRef(refName); err != nil {
		return err
	}
	if !validReleaseCommit(commit) {
		return fmt.Errorf("release commit has invalid object ID %q", commit)
	}
	if err := validateOpenReleaseArtifactClosure(artifacts); err != nil {
		return err
	}
	if err := publisher.requireImmutableReleases(); err != nil {
		return err
	}
	if err := publisher.rejectExistingRelease(refName); err != nil {
		return err
	}
	if err := publisher.requireRemoteTagCommit(refName, commit); err != nil {
		return err
	}

	release, created, createErr := publisher.createDraft(refName, commit)
	if !created {
		return createErr
	}
	published := false
	defer func() {
		if published {
			return
		}
		// GitHub does not expose a conditional revision for release deletion.
		// Retain the unpublished draft instead of racing another actor that may
		// have published it after our last observation.
		resultErr = errors.Join(resultErr, fmt.Errorf(
			"unpublished GitHub release draft %d was retained for administrator review",
			release.ID,
		))
	}()
	if createErr != nil {
		return createErr
	}

	for _, artifact := range artifacts {
		if _, err := publisher.uploadArtifact(release, artifact); err != nil {
			return err
		}
	}
	if err := publisher.requireDraft(release.ID, refName); err != nil {
		return err
	}
	if err := publisher.requireRemoteArtifactClosure(release.ID, artifacts); err != nil {
		return err
	}
	if err := publisher.requireRemoteTagCommit(refName, commit); err != nil {
		return err
	}
	if err := verifyOpenReleaseArtifacts(artifacts); err != nil {
		return fmt.Errorf("revalidate sealed artifacts before publication: %w", err)
	}
	// Retain the release after the first publication attempt: a transport or
	// response error cannot prove that GitHub did not commit draft=false.
	published = true
	final, err := publisher.publishDraft(release.ID, refName)
	if err != nil {
		return err
	}
	// A successful draft=false response is the public commit boundary. Never
	// delete or mutate the release after this point, even if a later check fails.
	if final.PublishedAt == nil || *final.PublishedAt == "" {
		return errors.New("published GitHub release lacks published_at")
	}
	if err := publisher.requirePublished(release.ID, refName); err != nil {
		return err
	}
	if err := publisher.requireRemoteArtifactClosure(release.ID, artifacts); err != nil {
		return err
	}
	return publisher.requireRemoteTagCommit(refName, commit)
}

func (publisher *githubReleasePublisher) requireImmutableReleases() error {
	endpoint := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/immutable-releases", nil,
	)
	var settings githubImmutableReleaseSettings
	if err := publisher.jsonRequest(http.MethodGet, endpoint, nil, http.StatusOK, &settings); err != nil {
		return fmt.Errorf("require immutable GitHub releases before publication: %w", err)
	}
	if !settings.Enabled || !settings.EnforcedByOwner {
		return errors.New("immutable GitHub releases are not enabled and enforced by the repository owner")
	}
	return nil
}

func validateOpenReleaseArtifactClosure(artifacts []openReleaseArtifact) error {
	if len(artifacts) == 0 {
		return errors.New("release artifact closure is empty")
	}
	previous := ""
	seen := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.name == "" || artifact.name != filepathBase(artifact.name) ||
			strings.ContainsAny(artifact.name, "\x00\r\n") {
			return fmt.Errorf("unsafe release artifact name %q", artifact.name)
		}
		if artifact.file == nil || artifact.info == nil {
			return fmt.Errorf("release artifact %s lacks a sealed descriptor identity", artifact.name)
		}
		if _, duplicate := seen[artifact.name]; duplicate {
			return fmt.Errorf("duplicate release artifact name %s", artifact.name)
		}
		if previous != "" && artifact.name <= previous {
			return errors.New("release artifacts are not in canonical name order")
		}
		seen[artifact.name] = struct{}{}
		previous = artifact.name
	}
	return verifyOpenReleaseArtifacts(artifacts)
}

func filepathBase(name string) string {
	if separator := strings.LastIndexAny(name, "/\\"); separator >= 0 {
		return name[separator+1:]
	}
	return name
}

func (publisher *githubReleasePublisher) rejectExistingRelease(refName string) error {
	_, found, err := publisher.findReleaseByTag(refName)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("GitHub release or draft already exists for %s", refName)
	}
	return nil
}

func (publisher *githubReleasePublisher) findReleaseByTag(
	refName string,
) (githubRelease, bool, error) {
	for page := 1; page <= maximumReleasePages; page++ {
		endpoint := publisher.apiEndpoint("/repos/"+releaseRepository+"/releases", url.Values{
			"page": []string{strconv.Itoa(page)}, "per_page": []string{"100"},
		})
		var releases []githubRelease
		if err := publisher.jsonRequest(http.MethodGet, endpoint, nil, http.StatusOK, &releases); err != nil {
			return githubRelease{}, false, fmt.Errorf("list GitHub releases page %d: %w", page, err)
		}
		for _, release := range releases {
			if release.TagName == refName {
				return release, true, nil
			}
		}
		if len(releases) < 100 {
			return githubRelease{}, false, nil
		}
	}
	return githubRelease{}, false, errors.New("GitHub release listing exceeded its page bound")
}

func (publisher *githubReleasePublisher) requireRemoteTagCommit(refName, commit string) error {
	endpoint := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/git/ref/tags/"+url.PathEscape(refName), nil,
	)
	var reference githubReference
	if err := publisher.jsonRequest(http.MethodGet, endpoint, nil, http.StatusOK, &reference); err != nil {
		return fmt.Errorf("resolve remote release tag %s: %w", refName, err)
	}
	if reference.Ref != "refs/tags/"+refName || !validObjectID(reference.Object.SHA) {
		return errors.New("remote release tag response has an invalid identity")
	}
	object := reference.Object
	seen := make(map[string]struct{})
	for range maximumTagDepth {
		switch object.Type {
		case "commit":
			if object.SHA != commit {
				return fmt.Errorf("remote release tag resolves to %s, want %s", object.SHA, commit)
			}
			return nil
		case "tag":
			if _, duplicate := seen[object.SHA]; duplicate {
				return errors.New("remote annotated release tag contains a cycle")
			}
			seen[object.SHA] = struct{}{}
			var tag githubTag
			endpoint := publisher.apiEndpoint(
				"/repos/"+releaseRepository+"/git/tags/"+object.SHA, nil,
			)
			if err := publisher.jsonRequest(http.MethodGet, endpoint, nil, http.StatusOK, &tag); err != nil {
				return fmt.Errorf("peel remote annotated tag %s: %w", object.SHA, err)
			}
			if !validObjectID(tag.Object.SHA) {
				return errors.New("remote annotated tag target has an invalid object ID")
			}
			object = tag.Object
		default:
			return fmt.Errorf("remote release tag targets unsupported object type %q", object.Type)
		}
	}
	return errors.New("remote annotated release tag exceeded its depth bound")
}

func (publisher *githubReleasePublisher) createDraft(refName, commit string) (githubRelease, bool, error) {
	payload := map[string]any{
		"draft":                  true,
		"generate_release_notes": true,
		"name":                   "ScopeSifter " + refName,
		"prerelease":             false,
		"tag_name":               refName,
		"target_commitish":       commit,
	}
	var release githubRelease
	endpoint := publisher.apiEndpoint("/repos/"+releaseRepository+"/releases", nil)
	if err := publisher.jsonRequest(http.MethodPost, endpoint, payload, http.StatusCreated, &release); err != nil {
		createErr := fmt.Errorf("create GitHub release draft: %w", err)
		reconciled, found, reconcileErr := publisher.findReleaseByTag(refName)
		if reconcileErr != nil {
			return githubRelease{}, false, errors.Join(createErr, fmt.Errorf(
				"reconcile ambiguous GitHub release creation: %w", reconcileErr,
			))
		}
		if !found {
			return githubRelease{}, false, errors.Join(
				createErr,
				errors.New("ambiguous GitHub release creation did not expose a retained draft"),
			)
		}
		created := reconciled.ID > 0 && reconciled.TagName == refName && reconciled.Draft
		validationErr := publisher.validateRelease(reconciled, refName, true)
		return reconciled, created, errors.Join(createErr, validationErr)
	}
	created := release.ID > 0 && release.TagName == refName && release.Draft
	if err := publisher.validateRelease(release, refName, true); err != nil {
		return release, created, err
	}
	return release, true, nil
}

func (publisher *githubReleasePublisher) validateRelease(
	release githubRelease, refName string, draft bool,
) error {
	if release.ID <= 0 || release.TagName != refName || release.Name != "ScopeSifter "+refName ||
		release.Draft != draft || release.Prerelease || release.Immutable == draft ||
		draft && release.PublishedAt != nil || !draft && release.PublishedAt == nil {
		return errors.New("GitHub release response does not match the requested identity")
	}
	wantURL := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/releases/"+strconv.FormatInt(release.ID, 10), nil,
	).String()
	wantAssets := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/releases/"+strconv.FormatInt(release.ID, 10)+"/assets", nil,
	).String()
	if release.URL != wantURL || release.AssetsURL != wantAssets {
		return errors.New("GitHub release response contains unexpected API URLs")
	}
	uploadTemplate := strings.SplitN(release.UploadURL, "{", 2)[0]
	wantUpload := publisher.uploadEndpoint(
		"/repos/"+releaseRepository+"/releases/"+strconv.FormatInt(release.ID, 10)+"/assets", nil,
	).String()
	if uploadTemplate != wantUpload {
		return errors.New("GitHub release response contains an unexpected upload URL")
	}
	return nil
}

func (publisher *githubReleasePublisher) uploadArtifact(
	release githubRelease, artifact openReleaseArtifact,
) (githubReleaseAsset, error) {
	endpoint := publisher.uploadEndpoint(
		"/repos/"+releaseRepository+"/releases/"+strconv.FormatInt(release.ID, 10)+"/assets",
		url.Values{"name": []string{artifact.name}},
	)
	reader := io.NewSectionReader(artifact.file, 0, artifact.info.Size())
	request, err := publisher.newRequest(http.MethodPost, endpoint, reader, artifact.info.Size(), true)
	if err != nil {
		return githubReleaseAsset{}, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := publisher.client.Do(request)
	if err != nil {
		return githubReleaseAsset{}, fmt.Errorf("upload release artifact %s: %w", artifact.name, err)
	}
	var uploaded githubReleaseAsset
	decodeErr := decodeJSONResponse(response, http.StatusCreated, &uploaded)
	closeErr := response.Body.Close()
	if err := errors.Join(
		decodeErr,
		wrapReleaseError("close GitHub API response body", closeErr),
	); err != nil {
		return githubReleaseAsset{}, fmt.Errorf("upload release artifact %s: %w", artifact.name, err)
	}
	if err := publisher.validateRemoteAsset(uploaded, artifact); err != nil {
		return githubReleaseAsset{}, err
	}
	return uploaded, nil
}

func (publisher *githubReleasePublisher) validateRemoteAsset(
	remote githubReleaseAsset, local openReleaseArtifact,
) error {
	wantDigest := "sha256:" + fmt.Sprintf("%x", local.digest)
	if remote.ID <= 0 || remote.Name != local.name || remote.State != "uploaded" ||
		remote.Size != local.info.Size() || remote.Digest != wantDigest {
		return fmt.Errorf("remote release artifact %s does not match sealed bytes", local.name)
	}
	wantURL := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/releases/assets/"+strconv.FormatInt(remote.ID, 10), nil,
	).String()
	if remote.URL != wantURL {
		return fmt.Errorf("remote release artifact %s has an unexpected API URL", local.name)
	}
	return nil
}

func (publisher *githubReleasePublisher) requireDraft(id int64, refName string) error {
	release, err := publisher.getRelease(id)
	if err != nil {
		return err
	}
	return publisher.validateRelease(release, refName, true)
}

func (publisher *githubReleasePublisher) requirePublished(id int64, refName string) error {
	release, err := publisher.getRelease(id)
	if err != nil {
		return err
	}
	if err := publisher.validateRelease(release, refName, false); err != nil {
		return err
	}
	if release.PublishedAt == nil || *release.PublishedAt == "" {
		return errors.New("published GitHub release lacks published_at")
	}
	return nil
}

func (publisher *githubReleasePublisher) getRelease(id int64) (githubRelease, error) {
	endpoint := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/releases/"+strconv.FormatInt(id, 10), nil,
	)
	var release githubRelease
	if err := publisher.jsonRequest(http.MethodGet, endpoint, nil, http.StatusOK, &release); err != nil {
		return githubRelease{}, fmt.Errorf("inspect GitHub release %d: %w", id, err)
	}
	return release, nil
}

func (publisher *githubReleasePublisher) requireRemoteArtifactClosure(
	id int64, artifacts []openReleaseArtifact,
) error {
	endpoint := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/releases/"+strconv.FormatInt(id, 10)+"/assets",
		url.Values{"page": []string{"1"}, "per_page": []string{"100"}},
	)
	var remote []githubReleaseAsset
	if err := publisher.jsonRequest(http.MethodGet, endpoint, nil, http.StatusOK, &remote); err != nil {
		return fmt.Errorf("list GitHub release assets: %w", err)
	}
	second := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/releases/"+strconv.FormatInt(id, 10)+"/assets",
		url.Values{"page": []string{"2"}, "per_page": []string{"100"}},
	)
	var overflow []githubReleaseAsset
	if err := publisher.jsonRequest(http.MethodGet, second, nil, http.StatusOK, &overflow); err != nil {
		return fmt.Errorf("check GitHub release asset overflow: %w", err)
	}
	if len(remote) != len(artifacts) || len(overflow) != 0 {
		return fmt.Errorf("remote release has %d+%d assets, want %d", len(remote), len(overflow), len(artifacts))
	}
	localByName := make(map[string]openReleaseArtifact, len(artifacts))
	for _, artifact := range artifacts {
		localByName[artifact.name] = artifact
	}
	seenNames := make(map[string]struct{}, len(remote))
	seenIDs := make(map[int64]struct{}, len(remote))
	for _, asset := range remote {
		local, found := localByName[asset.Name]
		if !found {
			return fmt.Errorf("unexpected remote release artifact %s", asset.Name)
		}
		if _, duplicate := seenNames[asset.Name]; duplicate {
			return fmt.Errorf("duplicate remote release artifact %s", asset.Name)
		}
		if _, duplicate := seenIDs[asset.ID]; duplicate || asset.ID <= 0 {
			return errors.New("duplicate or invalid remote release asset ID")
		}
		seenNames[asset.Name] = struct{}{}
		seenIDs[asset.ID] = struct{}{}
		if err := publisher.validateRemoteAsset(asset, local); err != nil {
			return err
		}
		if err := publisher.requireDownloadedAsset(asset.ID, local); err != nil {
			return err
		}
	}
	return nil
}

func (publisher *githubReleasePublisher) requireDownloadedAsset(
	id int64, artifact openReleaseArtifact,
) error {
	endpoint := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/releases/assets/"+strconv.FormatInt(id, 10), nil,
	)
	request, err := publisher.newRequest(http.MethodGet, endpoint, nil, 0, true)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := publisher.client.Do(request)
	if err != nil {
		return fmt.Errorf("download remote release artifact %s: %w", artifact.name, err)
	}
	if response.StatusCode == http.StatusFound || response.StatusCode == http.StatusTemporaryRedirect ||
		response.StatusCode == http.StatusPermanentRedirect {
		location := response.Header.Get("Location")
		_ = response.Body.Close()
		redirect, parseErr := url.Parse(location)
		if parseErr != nil || !publisher.allowedUnauthenticatedDownload(redirect) {
			return fmt.Errorf("remote release artifact %s has an unsafe redirect", artifact.name)
		}
		request, err = http.NewRequest(http.MethodGet, redirect.String(), nil)
		if err != nil {
			return err
		}
		response, err = publisher.client.Do(request)
		if err != nil {
			return fmt.Errorf("follow remote release artifact redirect %s: %w", artifact.name, err)
		}
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return fmt.Errorf("download remote release artifact %s: GitHub status %d", artifact.name, response.StatusCode)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(hasher, io.LimitReader(response.Body, artifact.info.Size()+1))
	closeErr := response.Body.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	if written != artifact.info.Size() {
		return fmt.Errorf("downloaded release artifact %s size = %d, want %d", artifact.name, written, artifact.info.Size())
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	if digest != artifact.digest {
		return fmt.Errorf("downloaded release artifact %s has the wrong digest", artifact.name)
	}
	return nil
}

func (publisher *githubReleasePublisher) publishDraft(id int64, refName string) (githubRelease, error) {
	endpoint := publisher.apiEndpoint(
		"/repos/"+releaseRepository+"/releases/"+strconv.FormatInt(id, 10), nil,
	)
	var release githubRelease
	if err := publisher.jsonRequest(
		http.MethodPatch, endpoint, map[string]bool{"draft": false}, http.StatusOK, &release,
	); err != nil {
		return githubRelease{}, fmt.Errorf("publish GitHub release draft: %w", err)
	}
	if err := publisher.validateRelease(release, refName, false); err != nil {
		return githubRelease{}, err
	}
	return release, nil
}

func (publisher *githubReleasePublisher) jsonRequest(
	method string, endpoint *url.URL, payload any, expectedStatus int, target any,
) error {
	var body io.Reader
	var length int64
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
		length = int64(len(encoded))
	}
	request, err := publisher.newRequest(method, endpoint, body, length, true)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := publisher.client.Do(request)
	if err != nil {
		return err
	}
	decodeErr := decodeJSONResponse(response, expectedStatus, target)
	closeErr := response.Body.Close()
	return errors.Join(
		decodeErr,
		wrapReleaseError("close GitHub API response body", closeErr),
	)
}

func (publisher *githubReleasePublisher) newRequest(
	method string, endpoint *url.URL, body io.Reader, length int64, authenticated bool,
) (*http.Request, error) {
	if endpoint == nil || !publisher.allowedAuthorizedEndpoint(endpoint) {
		return nil, errors.New("GitHub API endpoint is outside the trusted hosts")
	}
	request, err := http.NewRequest(method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	request.ContentLength = length
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-Github-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", "scopesifter-release-publisher")
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+publisher.token)
	}
	return request, nil
}

func (publisher *githubReleasePublisher) allowedAuthorizedEndpoint(endpoint *url.URL) bool {
	if endpoint == nil || endpoint.User != nil || endpoint.Fragment != "" {
		return false
	}
	if publisher.allowTestHTTP {
		return (endpoint.Scheme == "http" || endpoint.Scheme == "https") &&
			(endpoint.Host == publisher.apiBase.Host || endpoint.Host == publisher.uploadBase.Host)
	}
	return endpoint.Scheme == "https" &&
		(endpoint.Host == publisher.apiBase.Host || endpoint.Host == publisher.uploadBase.Host)
}

func (publisher *githubReleasePublisher) allowedUnauthenticatedDownload(endpoint *url.URL) bool {
	if endpoint == nil || endpoint.User != nil || endpoint.Fragment != "" {
		return false
	}
	if publisher.allowTestHTTP {
		return (endpoint.Scheme == "http" || endpoint.Scheme == "https") &&
			endpoint.Host == publisher.apiBase.Host
	}
	host := strings.ToLower(endpoint.Hostname())
	return endpoint.Scheme == "https" &&
		(host == "release-assets.githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com"))
}

func (publisher *githubReleasePublisher) apiEndpoint(path string, query url.Values) *url.URL {
	return releaseEndpoint(publisher.apiBase, path, query)
}

func (publisher *githubReleasePublisher) uploadEndpoint(path string, query url.Values) *url.URL {
	return releaseEndpoint(publisher.uploadBase, path, query)
}

func releaseEndpoint(base *url.URL, path string, query url.Values) *url.URL {
	result := *base
	result.Path = path
	result.RawPath = ""
	result.RawQuery = query.Encode()
	result.Fragment = ""
	result.User = nil
	return &result
}

func decodeJSONResponse(response *http.Response, expectedStatus int, target any) error {
	if response == nil {
		return errors.New("GitHub API response is missing")
	}
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("GitHub API status = %d, want %d", response.StatusCode, expectedStatus)
	}
	limited := io.LimitReader(response.Body, maximumGitHubJSONBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(data) > maximumGitHubJSONBytes {
		return errors.New("GitHub API JSON response exceeded its size bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("GitHub API response contains multiple JSON values")
		}
		return err
	}
	return nil
}
