package releaseartifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	testReleaseRef    = "v1.2.3"
	testReleaseToken  = "test-release-token"
	testReleaseID     = int64(42)
	testReleaseCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testReleaseTag    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type releasePublishScenario struct {
	t                     *testing.T
	server                *httptest.Server
	artifacts             []openReleaseArtifact
	artifactBytes         map[string][]byte
	assetIDs              map[string]int64
	uploaded              map[string][]byte
	calls                 []string
	created               bool
	published             bool
	deleted               int
	preexisting           bool
	immutableDisabled     bool
	immutableNotEnforced  bool
	wrongTagCommit        bool
	corruptDownload       bool
	invalidPublishedState bool
}

func newReleasePublishScenario(t *testing.T, content map[string][]byte) *releasePublishScenario {
	t.Helper()
	artifacts, err := openReleaseArtifacts(content)
	if err != nil {
		t.Fatal(err)
	}
	scenario := &releasePublishScenario{
		t: t, artifacts: artifacts, artifactBytes: content,
		assetIDs: make(map[string]int64, len(content)),
		uploaded: make(map[string][]byte, len(content)),
	}
	scenario.server = httptest.NewServer(http.HandlerFunc(scenario.serveHTTP))
	t.Cleanup(func() {
		scenario.server.Close()
		for _, artifact := range scenario.artifacts {
			if err := artifact.file.Close(); err != nil {
				t.Errorf("close test artifact %s: %v", artifact.name, err)
			}
		}
	})
	return scenario
}

func (scenario *releasePublishScenario) publisher() *githubReleasePublisher {
	scenario.t.Helper()
	base, err := url.Parse(scenario.server.URL)
	if err != nil {
		scenario.t.Fatal(err)
	}
	client := scenario.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &githubReleasePublisher{
		apiBase: base, uploadBase: base, client: client,
		token: testReleaseToken, allowTestHTTP: true,
	}
}

func (scenario *releasePublishScenario) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	scenario.t.Helper()
	scenario.requireRequestHeaders(request)
	scenario.calls = append(scenario.calls, request.Method+" "+request.URL.RequestURI())

	repositoryPath := "/repos/" + releaseRepository
	switch {
	case request.Method == http.MethodGet && request.URL.Path == repositoryPath+"/immutable-releases":
		scenario.requireNoQuery(request)
		scenario.requireEmptyBody(request)
		scenario.writeJSON(writer, http.StatusOK, githubImmutableReleaseSettings{
			Enabled:         !scenario.immutableDisabled,
			EnforcedByOwner: !scenario.immutableNotEnforced,
		})
	case request.Method == http.MethodGet && request.URL.Path == repositoryPath+"/releases":
		scenario.requireQuery(request, url.Values{"page": {"1"}, "per_page": {"100"}})
		scenario.requireEmptyBody(request)
		if scenario.created {
			scenario.writeJSON(writer, http.StatusOK, []githubRelease{scenario.release(true)})
			return
		}
		if scenario.preexisting {
			scenario.writeJSON(writer, http.StatusOK, []githubRelease{{TagName: testReleaseRef, Draft: true}})
			return
		}
		scenario.writeJSON(writer, http.StatusOK, []githubRelease{})
	case request.Method == http.MethodGet && request.URL.Path == repositoryPath+"/git/ref/tags/"+testReleaseRef:
		scenario.requireNoQuery(request)
		scenario.requireEmptyBody(request)
		scenario.writeJSON(writer, http.StatusOK, githubReference{
			Ref:    "refs/tags/" + testReleaseRef,
			Object: githubObject{SHA: testReleaseTag, Type: "tag"},
		})
	case request.Method == http.MethodGet && request.URL.Path == repositoryPath+"/git/tags/"+testReleaseTag:
		scenario.requireNoQuery(request)
		scenario.requireEmptyBody(request)
		commit := testReleaseCommit
		if scenario.wrongTagCommit {
			commit = strings.Repeat("c", 40)
		}
		scenario.writeJSON(writer, http.StatusOK, githubTag{
			Object: githubObject{SHA: commit, Type: "commit"},
		})
	case request.Method == http.MethodPost && request.URL.Path == repositoryPath+"/releases":
		scenario.requireNoQuery(request)
		scenario.requireContentType(request, "application/json")
		scenario.requireExactBody(request, fmt.Sprintf(
			`{"draft":true,"generate_release_notes":true,"name":"ScopeSifter %s","prerelease":false,"tag_name":"%s","target_commitish":"%s"}`,
			testReleaseRef, testReleaseRef, testReleaseCommit,
		))
		scenario.created = true
		scenario.writeJSON(writer, http.StatusCreated, scenario.release(true))
	case request.Method == http.MethodPost && request.URL.Path == repositoryPath+"/releases/42/assets":
		scenario.requireContentType(request, "application/octet-stream")
		name := request.URL.Query().Get("name")
		if len(request.URL.Query()) != 1 || name == "" {
			scenario.t.Errorf("upload query = %q, want one canonical name", request.URL.RawQuery)
		}
		want, found := scenario.artifactBytes[name]
		if !found {
			scenario.t.Errorf("upload name = %q, want a known artifact", name)
		}
		got := scenario.readBody(request)
		if !bytes.Equal(got, want) {
			scenario.t.Errorf("uploaded %s bytes = %q, want %q", name, got, want)
		}
		if request.ContentLength != int64(len(want)) {
			scenario.t.Errorf("uploaded %s Content-Length = %d, want %d", name, request.ContentLength, len(want))
		}
		id := int64(101 + len(scenario.uploaded))
		scenario.assetIDs[name] = id
		scenario.uploaded[name] = bytes.Clone(got)
		scenario.writeJSON(writer, http.StatusCreated, scenario.asset(name, id))
	case request.Method == http.MethodGet && request.URL.Path == repositoryPath+"/releases/42":
		scenario.requireNoQuery(request)
		scenario.requireEmptyBody(request)
		release := scenario.release(!scenario.published)
		if scenario.published && scenario.invalidPublishedState {
			release.PublishedAt = nil
		}
		scenario.writeJSON(writer, http.StatusOK, release)
	case request.Method == http.MethodGet && request.URL.Path == repositoryPath+"/releases/42/assets":
		scenario.requireEmptyBody(request)
		page := request.URL.Query().Get("page")
		scenario.requireQuery(request, url.Values{"page": {page}, "per_page": {"100"}})
		if page == "2" {
			scenario.writeJSON(writer, http.StatusOK, []githubReleaseAsset{})
			return
		}
		if page != "1" {
			scenario.t.Errorf("asset page = %q, want 1 or 2", page)
		}
		// Return the opposite of upload order to prove closure checks bind by
		// canonical asset identity, not incidental API ordering.
		names := scenario.uploadedNames()
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		assets := make([]githubReleaseAsset, 0, len(names))
		for _, name := range names {
			assets = append(assets, scenario.asset(name, scenario.assetIDs[name]))
		}
		scenario.writeJSON(writer, http.StatusOK, assets)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, repositoryPath+"/releases/assets/"):
		scenario.requireNoQuery(request)
		scenario.requireEmptyBody(request)
		if got := request.Header.Get("Accept"); got != "application/octet-stream" {
			scenario.t.Errorf("asset download Accept = %q, want application/octet-stream", got)
		}
		idText := strings.TrimPrefix(request.URL.Path, repositoryPath+"/releases/assets/")
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			scenario.t.Errorf("parse asset id %q: %v", idText, err)
		}
		name, found := scenario.nameForAssetID(id)
		if !found {
			scenario.t.Errorf("downloaded unknown asset id %d", id)
		}
		content := scenario.uploaded[name]
		if scenario.corruptDownload {
			content = append(bytes.Clone(content), 'x')
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(content)
	case request.Method == http.MethodPatch && request.URL.Path == repositoryPath+"/releases/42":
		scenario.requireNoQuery(request)
		scenario.requireContentType(request, "application/json")
		scenario.requireExactBody(request, `{"draft":false}`)
		scenario.published = true
		scenario.writeJSON(writer, http.StatusOK, scenario.release(false))
	case request.Method == http.MethodDelete && request.URL.Path == repositoryPath+"/releases/42":
		scenario.requireNoQuery(request)
		scenario.requireEmptyBody(request)
		if scenario.published {
			scenario.t.Error("publisher attempted to delete a release after PATCH")
		}
		scenario.deleted++
		writer.WriteHeader(http.StatusNoContent)
	default:
		scenario.t.Errorf("unexpected GitHub request: %s %s", request.Method, request.URL.RequestURI())
		http.Error(writer, "unexpected request", http.StatusNotFound)
	}
}

func (scenario *releasePublishScenario) release(draft bool) githubRelease {
	base := scenario.server.URL + "/repos/" + releaseRepository
	release := githubRelease{
		AssetsURL: base + "/releases/42/assets",
		Draft:     draft,
		ID:        testReleaseID,
		Immutable: !draft,
		Name:      "ScopeSifter " + testReleaseRef,
		TagName:   testReleaseRef,
		UploadURL: scenario.server.URL + "/repos/" + releaseRepository + "/releases/42/assets{?name,label}",
		URL:       base + "/releases/42",
	}
	if !draft {
		publishedAt := "2026-08-12T12:00:00Z"
		release.PublishedAt = &publishedAt
	}
	return release
}

func (scenario *releasePublishScenario) asset(name string, id int64) githubReleaseAsset {
	digest := sha256.Sum256(scenario.artifactBytes[name])
	return githubReleaseAsset{
		Digest: fmt.Sprintf("sha256:%x", digest), ID: id, Name: name,
		Size: int64(len(scenario.artifactBytes[name])), State: "uploaded",
		URL: scenario.server.URL + "/repos/" + releaseRepository + "/releases/assets/" + strconv.FormatInt(id, 10),
	}
}

func (scenario *releasePublishScenario) uploadedNames() []string {
	names := make([]string, 0, len(scenario.uploaded))
	for name := range scenario.uploaded {
		names = append(names, name)
	}
	return names
}

func (scenario *releasePublishScenario) nameForAssetID(id int64) (string, bool) {
	for name, candidate := range scenario.assetIDs {
		if candidate == id {
			return name, true
		}
	}
	return "", false
}

func (scenario *releasePublishScenario) writeJSON(writer http.ResponseWriter, status int, value any) {
	scenario.t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		scenario.t.Errorf("encode fake GitHub response: %v", err)
	}
}

func (scenario *releasePublishScenario) requireRequestHeaders(request *http.Request) {
	scenario.t.Helper()
	for name, want := range map[string]string{
		"Authorization":        "Bearer " + testReleaseToken,
		"X-GitHub-Api-Version": githubAPIVersion,
		"User-Agent":           "scopesifter-release-publisher",
	} {
		if got := request.Header.Get(name); got != want {
			scenario.t.Errorf("%s %s header %s = %q, want %q", request.Method, request.URL.Path, name, got, want)
		}
	}
	if got := request.Header.Get("Accept"); request.Method != http.MethodGet ||
		!strings.Contains(request.URL.Path, "/releases/assets/") {
		if got != "application/vnd.github+json" {
			scenario.t.Errorf("%s %s Accept = %q, want application/vnd.github+json", request.Method, request.URL.Path, got)
		}
	}
}

func (scenario *releasePublishScenario) requireContentType(request *http.Request, want string) {
	scenario.t.Helper()
	if got := request.Header.Get("Content-Type"); got != want {
		scenario.t.Errorf("%s %s Content-Type = %q, want %q", request.Method, request.URL.Path, got, want)
	}
}

func (scenario *releasePublishScenario) requireNoQuery(request *http.Request) {
	scenario.t.Helper()
	if request.URL.RawQuery != "" {
		scenario.t.Errorf("%s %s query = %q, want empty", request.Method, request.URL.Path, request.URL.RawQuery)
	}
}

func (scenario *releasePublishScenario) requireQuery(request *http.Request, want url.Values) {
	scenario.t.Helper()
	if got := request.URL.Query(); !reflect.DeepEqual(got, want) {
		scenario.t.Errorf("%s %s query = %#v, want %#v", request.Method, request.URL.Path, got, want)
	}
}

func (scenario *releasePublishScenario) readBody(request *http.Request) []byte {
	scenario.t.Helper()
	data, err := io.ReadAll(request.Body)
	if err != nil {
		scenario.t.Errorf("read %s %s body: %v", request.Method, request.URL.Path, err)
	}
	if err := request.Body.Close(); err != nil {
		scenario.t.Errorf("close %s %s body: %v", request.Method, request.URL.Path, err)
	}
	return data
}

func (scenario *releasePublishScenario) requireEmptyBody(request *http.Request) {
	scenario.t.Helper()
	if body := scenario.readBody(request); len(body) != 0 {
		scenario.t.Errorf("%s %s body = %q, want empty", request.Method, request.URL.Path, body)
	}
}

func (scenario *releasePublishScenario) requireExactBody(request *http.Request, want string) {
	scenario.t.Helper()
	if got := string(scenario.readBody(request)); got != want {
		scenario.t.Errorf("%s %s body = %q, want %q", request.Method, request.URL.Path, got, want)
	}
}

func TestGitHubReleasePublisherSuccessUsesExactDraftTransaction(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{
		"a-binary": []byte("first sealed release artifact"),
		"z.zip":    []byte("second sealed release artifact"),
	})
	if err := scenario.publisher().publish(testReleaseRef, testReleaseCommit, scenario.artifacts); err != nil {
		t.Fatal(err)
	}
	if scenario.deleted != 0 {
		t.Fatalf("successful publication deleted %d drafts", scenario.deleted)
	}
	if !scenario.created || !scenario.published {
		t.Fatalf("publication state = created %t, published %t", scenario.created, scenario.published)
	}
	for name, want := range scenario.artifactBytes {
		if got := scenario.uploaded[name]; !bytes.Equal(got, want) {
			t.Errorf("remote upload %s = %q, want %q", name, got, want)
		}
	}

	repositoryPath := "/repos/" + releaseRepository
	want := []string{
		"GET " + repositoryPath + "/immutable-releases",
		"GET " + repositoryPath + "/releases?page=1&per_page=100",
		"GET " + repositoryPath + "/git/ref/tags/" + testReleaseRef,
		"GET " + repositoryPath + "/git/tags/" + testReleaseTag,
		"POST " + repositoryPath + "/releases",
		"POST " + repositoryPath + "/releases/42/assets?name=a-binary",
		"POST " + repositoryPath + "/releases/42/assets?name=z.zip",
		"GET " + repositoryPath + "/releases/42",
		"GET " + repositoryPath + "/releases/42/assets?page=1&per_page=100",
		"GET " + repositoryPath + "/releases/42/assets?page=2&per_page=100",
		"GET " + repositoryPath + "/releases/assets/102",
		"GET " + repositoryPath + "/releases/assets/101",
		"GET " + repositoryPath + "/git/ref/tags/" + testReleaseRef,
		"GET " + repositoryPath + "/git/tags/" + testReleaseTag,
		"PATCH " + repositoryPath + "/releases/42",
		"GET " + repositoryPath + "/releases/42",
		"GET " + repositoryPath + "/releases/42/assets?page=1&per_page=100",
		"GET " + repositoryPath + "/releases/42/assets?page=2&per_page=100",
		"GET " + repositoryPath + "/releases/assets/102",
		"GET " + repositoryPath + "/releases/assets/101",
		"GET " + repositoryPath + "/git/ref/tags/" + testReleaseRef,
		"GET " + repositoryPath + "/git/tags/" + testReleaseTag,
	}
	if !reflect.DeepEqual(scenario.calls, want) {
		t.Fatalf("GitHub request sequence =\n%s\nwant:\n%s", strings.Join(scenario.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestGitHubReleasePublisherRejectsPreexistingDraftBeforeMutation(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{"artifact": []byte("content")})
	scenario.preexisting = true
	err := scenario.publisher().publish(testReleaseRef, testReleaseCommit, scenario.artifacts)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("preexisting draft result = %v, want rejection", err)
	}
	want := []string{
		"GET /repos/" + releaseRepository + "/immutable-releases",
		"GET /repos/" + releaseRepository + "/releases?page=1&per_page=100",
	}
	if !reflect.DeepEqual(scenario.calls, want) || scenario.created || scenario.deleted != 0 {
		t.Fatalf("preexisting-draft side effects: calls=%q created=%t deleted=%d", scenario.calls, scenario.created, scenario.deleted)
	}
}

func TestGitHubReleasePublisherRequiresImmutableSettingBeforeMutation(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{"artifact": []byte("content")})
	scenario.immutableDisabled = true
	err := scenario.publisher().publish(testReleaseRef, testReleaseCommit, scenario.artifacts)
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("disabled immutable releases result = %v, want rejection", err)
	}
	want := []string{"GET /repos/" + releaseRepository + "/immutable-releases"}
	if !reflect.DeepEqual(scenario.calls, want) || scenario.created || scenario.deleted != 0 {
		t.Fatalf("immutable preflight side effects: calls=%q created=%t deleted=%d", scenario.calls, scenario.created, scenario.deleted)
	}
}

func TestGitHubReleasePublisherRequiresOwnerEnforcedImmutabilityBeforeMutation(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{"artifact": []byte("content")})
	scenario.immutableNotEnforced = true
	err := scenario.publisher().publish(testReleaseRef, testReleaseCommit, scenario.artifacts)
	if err == nil || !strings.Contains(err.Error(), "enforced by the repository owner") {
		t.Fatalf("non-owner-enforced immutable releases result = %v, want rejection", err)
	}
	want := []string{"GET /repos/" + releaseRepository + "/immutable-releases"}
	if !reflect.DeepEqual(scenario.calls, want) || scenario.created || scenario.deleted != 0 {
		t.Fatalf("immutability enforcement side effects: calls=%q created=%t deleted=%d", scenario.calls, scenario.created, scenario.deleted)
	}
}

func TestGitHubReleasePublisherRequiresExactPeeledTagBeforeMutation(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{"artifact": []byte("content")})
	scenario.wrongTagCommit = true
	err := scenario.publisher().publish(testReleaseRef, testReleaseCommit, scenario.artifacts)
	if err == nil || !strings.Contains(err.Error(), "want "+testReleaseCommit) {
		t.Fatalf("wrong peeled tag result = %v, want exact-commit rejection", err)
	}
	if scenario.created || scenario.deleted != 0 {
		t.Fatalf("wrong tag mutated releases: created=%t deleted=%d", scenario.created, scenario.deleted)
	}
}

func TestGitHubReleasePublisherDownloadMismatchRetainsDraftBeforePublish(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{"artifact": []byte("content")})
	scenario.corruptDownload = true
	err := scenario.publisher().publish(testReleaseRef, testReleaseCommit, scenario.artifacts)
	if err == nil || !strings.Contains(err.Error(), "size") ||
		!strings.Contains(err.Error(), "draft 42 was retained") {
		t.Fatalf("corrupt remote bytes result = %v, want closure rejection", err)
	}
	if scenario.published || scenario.deleted != 0 {
		t.Fatalf("corrupt remote bytes state = published %t, deleted %d", scenario.published, scenario.deleted)
	}
	for _, call := range scenario.calls {
		if strings.HasPrefix(call, "PATCH ") {
			t.Fatalf("corrupt remote closure reached publication PATCH: %q", scenario.calls)
		}
	}
	if got := scenario.calls[len(scenario.calls)-1]; strings.HasPrefix(got, "DELETE ") {
		t.Fatalf("failure attempted unsafe draft cleanup: %q", scenario.calls)
	}
}

type releaseRoundTripFunc func(*http.Request) (*http.Response, error)

func (function releaseRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestGitHubReleasePublisherRetainsDraftAfterAmbiguousCreate(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{"artifact": []byte("content")})
	publisher := scenario.publisher()
	transport := publisher.client.Transport
	publisher.client.Transport = releaseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/repos/"+releaseRepository+"/releases" {
			return transport.RoundTrip(request)
		}
		scenario.requireRequestHeaders(request)
		scenario.calls = append(scenario.calls, request.Method+" "+request.URL.RequestURI())
		scenario.requireContentType(request, "application/json")
		_ = scenario.readBody(request)
		scenario.created = true
		return nil, errors.New("transport lost after sending creation request")
	})
	err := publisher.publish(testReleaseRef, testReleaseCommit, scenario.artifacts)
	if err == nil || !strings.Contains(err.Error(), "transport lost") ||
		!strings.Contains(err.Error(), "draft 42 was retained") {
		t.Fatalf("ambiguous creation result = %v, want retained-draft error", err)
	}
	if scenario.published || scenario.deleted != 0 || len(scenario.uploaded) != 0 {
		t.Fatalf("ambiguous creation state = published %t, deleted %d, uploaded %d", scenario.published, scenario.deleted, len(scenario.uploaded))
	}
	if got := scenario.calls[len(scenario.calls)-1]; got != "GET /repos/"+releaseRepository+"/releases?page=1&per_page=100" {
		t.Fatalf("ambiguous creation reconciliation ended with %q", got)
	}
}

func TestGitHubReleasePublisherDoesNotDeleteAfterAmbiguousPatch(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{"artifact": []byte("content")})
	publisher := scenario.publisher()
	transport := publisher.client.Transport
	publisher.client.Transport = releaseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPatch {
			return transport.RoundTrip(request)
		}
		scenario.requireRequestHeaders(request)
		scenario.calls = append(scenario.calls, request.Method+" "+request.URL.RequestURI())
		scenario.requireContentType(request, "application/json")
		scenario.requireExactBody(request, `{"draft":false}`)
		return nil, errors.New("transport lost after sending publication request")
	})
	err := publisher.publish(testReleaseRef, testReleaseCommit, scenario.artifacts)
	if err == nil || !strings.Contains(err.Error(), "transport lost") {
		t.Fatalf("ambiguous PATCH result = %v, want transport error", err)
	}
	if scenario.deleted != 0 {
		t.Fatalf("ambiguous PATCH deleted %d drafts", scenario.deleted)
	}
	if got := scenario.calls[len(scenario.calls)-1]; !strings.HasPrefix(got, "PATCH ") {
		t.Fatalf("ambiguous PATCH was followed by a mutation: %q", scenario.calls)
	}
}

func TestGitHubReleasePublisherDoesNotDeleteAfterSuccessfulPatch(t *testing.T) {
	scenario := newReleasePublishScenario(t, map[string][]byte{"artifact": []byte("content")})
	scenario.invalidPublishedState = true
	err := scenario.publisher().publish(testReleaseRef, testReleaseCommit, scenario.artifacts)
	if err == nil || !strings.Contains(err.Error(), "requested identity") {
		t.Fatalf("post-publication verification result = %v, want failure", err)
	}
	if !scenario.published || scenario.deleted != 0 {
		t.Fatalf("post-PATCH failure state = published %t, deleted %d", scenario.published, scenario.deleted)
	}
	for _, call := range scenario.calls {
		if strings.HasPrefix(call, "DELETE ") {
			t.Fatalf("post-PATCH verification failure deleted release: %q", scenario.calls)
		}
	}
}
