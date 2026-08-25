// Package scopesiftermcp exposes ScopeSifter navigation through a fixed,
// read-only Model Context Protocol tool surface.
package scopesiftermcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yapless/scopesifter/navigator"
)

const (
	// ImplementationName is the stable MCP implementation name.
	ImplementationName = "scopesifter"
	// ImplementationVersion changes when the public MCP tool contract changes.
	ImplementationVersion = "scopesifter-mcp/v9"

	structuredOutputBudget = 1024

	changedDescription = "Start bug/branch/PR with bounded base-to-HEAD changes."
	findDescription    = "Find exact symbol/path; return ranked locations."
	inspectDescription = "Read complete scope or exact fallback at PATH:LINE."
	outlineDescription = "Index known file; inspect PATH:LINE for source."
)

// ToolSpecification is the code-owned provider-visible part of one scopesifter
// MCP tool declaration. InputSchema is freshly allocated on every call to
// ToolSpecifications so callers cannot mutate the server's contract.
type ToolSpecification struct {
	InputSchema map[string]any
	Name        string
	Description string
}

// ToolSpecifications returns the exact four tool descriptions and input
// schemas registered by New, in canonical name order.
func ToolSpecifications() []ToolSpecification {
	return []ToolSpecification{
		{Name: "changed", Description: changedDescription, InputSchema: changedInputSchema()},
		{Name: "find", Description: findDescription, InputSchema: findInputSchema()},
		{Name: "inspect", Description: inspectDescription, InputSchema: inspectInputSchema()},
		{Name: "outline", Description: outlineDescription, InputSchema: outlineInputSchema()},
	}
}

var fullObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Config binds all repository authority at process startup. Tool callers
// cannot replace the root, base commit, or Git executable.
type Config struct {
	Root                string
	Base                string
	GitExecutable       string
	GitExecutableSHA256 string
}

// New creates a server with exactly the changed, find, inspect, and outline
// tools. It exposes no prompts, resources, logging, sampling, instructions, or
// custom methods.
func New(config Config) (*mcp.Server, error) {
	if !filepath.IsAbs(config.Root) || filepath.Clean(config.Root) != config.Root {
		return nil, errors.New("MCP root must be an absolute canonical path")
	}
	resolvedRoot, err := filepath.EvalSymlinks(config.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP root: %w", err)
	}
	if resolvedRoot != config.Root {
		return nil, errors.New("MCP root must not traverse a symlink")
	}
	if !fullObjectID.MatchString(config.Base) {
		return nil, errors.New("MCP base must be a canonical full Git object ID")
	}
	if !validSHA256(config.GitExecutableSHA256) {
		return nil, errors.New("MCP Git executable SHA-256 is invalid")
	}
	view, err := navigator.NewWithGit(
		config.Root,
		config.GitExecutable,
		config.GitExecutableSHA256,
	)
	if err != nil {
		return nil, fmt.Errorf("construct MCP repository view: %w", err)
	}
	if err := view.VerifyBaseCommit(config.Base); err != nil {
		return nil, fmt.Errorf("verify MCP base commit: %w", err)
	}

	service := &service{view: view, base: config.Base}
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    ImplementationName,
			Version: ImplementationVersion,
		},
		&mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{},
			},
			Logger: slog.New(slog.DiscardHandler),
		},
	)
	annotations := func() *mcp.ToolAnnotations {
		closedWorld := false
		nondestructive := false
		return &mcp.ToolAnnotations{
			DestructiveHint: &nondestructive,
			IdempotentHint:  true,
			OpenWorldHint:   &closedWorld,
			ReadOnlyHint:    true,
		}
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "changed",
		Description: changedDescription,
		Annotations: annotations(),
		InputSchema: changedInputSchema(),
	}, service.changed)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "find",
		Description: findDescription,
		Annotations: annotations(),
		InputSchema: findInputSchema(),
	}, service.find)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "inspect",
		Description: inspectDescription,
		Annotations: annotations(),
		InputSchema: inspectInputSchema(),
	}, service.inspect)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "outline",
		Description: outlineDescription,
		Annotations: annotations(),
		InputSchema: outlineInputSchema(),
	}, service.outline)
	return server, nil
}

// Run constructs a server and serves one MCP transport until it closes.
func Run(ctx context.Context, config Config, transport mcp.Transport) error {
	if transport == nil {
		return errors.New("MCP transport is required")
	}
	server, err := New(config)
	if err != nil {
		return err
	}
	return server.Run(ctx, transport)
}

type commonInput struct {
	Return         string   `json:"return"`
	PathGlobs      []string `json:"path_globs"`
	ExcludeGlobs   []string `json:"exclude_globs"`
	Context        int      `json:"context"`
	Limit          int      `json:"limit"`
	MaxCodeLines   int      `json:"max_code_lines"`
	DropComments   bool     `json:"drop_comments"`
	DropDocstrings bool     `json:"drop_docstrings"`
}

type changedInput struct {
	commonInput
	MaxPatchLines int `json:"max_patch_lines"`
}

type findInput struct {
	Query   string `json:"query"`
	Match   string `json:"match"`
	Include string `json:"include"`
	commonInput
	ChangedOnly     bool `json:"changed_only"`
	IncludeComments bool `json:"include_comments"`
	IncludeStrings  bool `json:"include_strings"`
}

type inspectInput struct {
	Location string `json:"location"`
	Include  string `json:"include"`
	commonInput
	ChangedOnly     bool `json:"changed_only"`
	IncludeComments bool `json:"include_comments"`
	IncludeStrings  bool `json:"include_strings"`
}

type outlineInput struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
}

type service struct {
	view *navigator.View
	base string
}

func (service *service) changed(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input changedInput,
) (*mcp.CallToolResult, any, error) {
	options := input.options(navigator.IncludeAll, navigator.ReturnLocations, service.base)
	options.MaxPatchLines = defaultInt(input.MaxPatchLines, defaultMaxPatchLines)
	response, err := service.view.WithContext(ctx).Changed(options)
	if err != nil {
		return nil, nil, err
	}
	return service.prepareResponse("changed", response)
}

func (service *service) find(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input findInput,
) (*mcp.CallToolResult, any, error) {
	options := input.options(
		navigator.Include(defaultString(input.Include, string(navigator.IncludeBoth))),
		navigator.ReturnLocations,
		service.base,
	)
	options.Match = navigator.FindMatch(defaultString(input.Match, string(navigator.FindMatchAuto)))
	options.ChangedOnly = input.ChangedOnly
	options.NoComments = !input.IncludeComments
	options.NoStrings = !input.IncludeStrings
	response, err := findWithLiteralSource(
		service.view.WithContext(ctx), input.Query, input.PathGlobs, options,
	)
	if err != nil {
		return nil, nil, err
	}
	response = service.enrichFileFindWithChanges(ctx, input, response)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	response, err = service.ensureUniqueFindDefinition(ctx, input, response)
	if err != nil {
		return nil, nil, err
	}
	return service.prepareResponse("find", response)
}

func (service *service) ensureUniqueFindDefinition(
	ctx context.Context,
	input findInput,
	response navigator.FindResponse,
) (navigator.FindResponse, error) {
	if response.MatchedAs != navigator.FindOutcomeSymbol || !response.ResultsTruncated ||
		navigator.Include(defaultString(input.Include, string(navigator.IncludeBoth))) == navigator.IncludeRefs {
		return response, nil
	}
	options := input.options(navigator.IncludeDefs, navigator.ReturnLocations, service.base)
	options.Match = navigator.FindMatchSymbol
	options.ChangedOnly = input.ChangedOnly
	options.NoComments = !input.IncludeComments
	options.NoStrings = !input.IncludeStrings
	options.Limit = 2
	definitions, err := findWithLiteralSource(
		service.view.WithContext(ctx), response.Query, input.PathGlobs, options,
	)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return response, err
		}
		return response, nil
	}
	if definitions.MatchedAs != navigator.FindOutcomeSymbol || definitions.ResultsTruncated {
		return response, nil
	}
	verified := uniqueActionableDefinition(definitions.Results)
	if verified < 0 {
		return response, nil
	}
	want := definitions.Results[verified]
	wantLine := actionableResultLine(&want)
	for index := range response.Results {
		result := response.Results[index]
		if result.Kind == "def" && result.Path == want.Path &&
			actionableResultLine(&result) == wantLine {
			return response, nil
		}
	}
	limit := defaultInt(input.Limit, defaultLimit)
	response.Results = append([]navigator.Result{want}, response.Results...)
	if len(response.Results) > limit {
		response.Results = response.Results[:limit]
		response.ResultsTruncated = true
	}
	return response, nil
}

func uniqueActionableDefinition(results []navigator.Result) int {
	primary := -1
	for index := range results {
		if results[index].Kind != "def" || results[index].Path == "" ||
			actionableResultLine(&results[index]) < 1 {
			continue
		}
		if primary >= 0 {
			return -1
		}
		primary = index
	}
	return primary
}

func (service *service) enrichFileFindWithChanges(
	ctx context.Context,
	input findInput,
	response navigator.FindResponse,
) navigator.FindResponse {
	if response.MatchedAs != navigator.FindOutcomeFile || len(response.Results) == 0 {
		return response
	}
	paths := make([]string, 0, len(response.Results))
	wantedPaths := make(map[string]struct{}, len(response.Results))
	for index := range response.Results {
		path := response.Results[index].Path
		if path == "" {
			continue
		}
		if _, exists := wantedPaths[path]; exists {
			continue
		}
		wantedPaths[path] = struct{}{}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return response
	}
	limit := defaultInt(input.Limit, defaultLimit)
	changed, err := service.view.WithContext(ctx).Changed(navigator.Options{
		Base:          service.base,
		Include:       navigator.IncludeAll,
		Return:        navigator.ReturnLocations,
		PathGlobs:     paths,
		Limit:         limit,
		MaxCodeLines:  defaultMaxCodeLines,
		MaxPatchLines: 1,
	})
	if err != nil {
		return response
	}
	return mergeChangedFileResults(response, changed, wantedPaths, limit)
}

func mergeChangedFileResults(
	response navigator.FindResponse,
	changed navigator.ChangedResponse,
	wantedPaths map[string]struct{},
	limit int,
) navigator.FindResponse {
	changedPaths := make(map[string]struct{})
	results := make([]navigator.Result, 0, len(changed.Results)+len(response.Results))
	for index := range changed.Results {
		result := changed.Results[index]
		if _, wanted := wantedPaths[result.Path]; !wanted || actionableResultLine(&result) < 1 {
			continue
		}
		result.Code = ""
		result.CodeStartLine = 0
		result.CodeEndLine = 0
		result.CodeTruncated = false
		result.Kind = "changed"
		results = append(results, result)
		changedPaths[result.Path] = struct{}{}
	}
	if len(results) == 0 {
		return response
	}
	for index := range response.Results {
		result := response.Results[index]
		if _, changedPath := changedPaths[result.Path]; changedPath {
			continue
		}
		results = append(results, result)
	}
	if len(results) > limit {
		results = results[:limit]
		response.ResultsTruncated = true
	}
	response.Results = results
	response.ResultsTruncated = response.ResultsTruncated || changed.ResultsTruncated
	return response
}

func (service *service) inspect(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input inspectInput,
) (*mcp.CallToolResult, any, error) {
	options := input.options(
		navigator.Include(defaultString(input.Include, string(navigator.IncludeScope))),
		navigator.ReturnScope,
		service.base,
	)
	options.ChangedOnly = input.ChangedOnly
	options.NoComments = !input.IncludeComments
	options.NoStrings = !input.IncludeStrings
	response, err := service.view.WithContext(ctx).Inspect(input.Location, options)
	if err != nil {
		return nil, nil, err
	}
	return service.prepareResponse("inspect", response)
}

func (service *service) outline(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input outlineInput,
) (*mcp.CallToolResult, any, error) {
	options := navigator.Options{
		Base:          service.base,
		Include:       navigator.IncludeDefs,
		Return:        navigator.ReturnLocations,
		Limit:         defaultInt(input.Limit, defaultLimit),
		MaxCodeLines:  defaultMaxCodeLines,
		MaxPatchLines: defaultMaxPatchLines,
	}
	response, err := service.view.WithContext(ctx).Outline(input.Path, options)
	if err != nil {
		return nil, nil, err
	}
	return service.prepareResponse("outline", response)
}

func (service *service) prepareResponse(
	tool string,
	response any,
) (*mcp.CallToolResult, any, error) {
	result, output, _, err := prepareToolResponse(
		tool,
		responseAuto,
		response,
		structuredOutputBudget,
	)
	if err != nil {
		return nil, nil, err
	}
	return result, output, nil
}

func (input commonInput) options(
	include navigator.Include,
	defaultReturn navigator.Return,
	base string,
) navigator.Options {
	return navigator.Options{
		Base:           base,
		Include:        include,
		Return:         navigator.Return(defaultString(input.Return, string(defaultReturn))),
		Context:        defaultInt(input.Context, defaultContext),
		Limit:          defaultInt(input.Limit, defaultLimit),
		PathGlobs:      append([]string(nil), input.PathGlobs...),
		ExcludeGlobs:   append([]string(nil), input.ExcludeGlobs...),
		MaxCodeLines:   defaultInt(input.MaxCodeLines, defaultMaxCodeLines),
		MaxPatchLines:  defaultMaxPatchLines,
		DropComments:   input.DropComments,
		DropDocstrings: input.DropDocstrings,
	}
}

func defaultInt(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func findWithLiteralSource(
	view *navigator.View,
	query string,
	pathGlobs []string,
	options navigator.Options,
) (navigator.FindResponse, error) {
	if options.Match != navigator.FindMatchPath {
		if source, ok := literalSourcePath(pathGlobs); ok {
			return view.FindSourceFirst(query, source, options)
		}
	}
	return view.Find(query, options)
}

func literalSourcePath(patterns []string) (string, bool) {
	if len(patterns) != 1 {
		return "", false
	}
	path := patterns[0]
	if path == "" || strings.ContainsAny(path, `\*?[`) ||
		strings.ContainsRune(path, '\x00') || pathpkg.IsAbs(path) {
		return "", false
	}
	clean := pathpkg.Clean(path)
	if clean != path || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}
