// Package scopesiftermcp exposes ScopeSifter navigation through a fixed Model
// Context Protocol tool surface. Repository access is always read-only;
// optional adaptive state is stored only in the OS user-cache directory.
package scopesiftermcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yapless/scopesifter/navigator"
)

const (
	// ImplementationName is the stable MCP implementation name.
	ImplementationName = "scopesifter"
	// ImplementationVersion changes when the public MCP tool contract changes.
	ImplementationVersion = "scopesifter-mcp/v4"

	changedDescription = "Branch/commit/PR first: base-to-HEAD patch and source context."
	findDescription    = "Exact identifier or path fragment; auto tries identifier then path."
	inspectDescription = "Scope/imports/related identifiers at known PATH:LINE."
	outlineDescription = "Source-ordered definitions for a known repository file."
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
	AdaptiveOutputCache bool
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

	learner, err := newAdaptiveLearner(config.Root, config.AdaptiveOutputCache, slog.Default())
	if err != nil {
		return nil, err
	}
	service := &service{view: view, learner: learner, base: config.Base}
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
			IdempotentHint:  false,
			OpenWorldHint:   &closedWorld,
			ReadOnlyHint:    !config.AdaptiveOutputCache,
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
	Response       string   `json:"response"`
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
	Response       string `json:"response"`
	Path           string `json:"path"`
	Return         string `json:"return"`
	Limit          int    `json:"limit"`
	MaxCodeLines   int    `json:"max_code_lines"`
	DropComments   bool   `json:"drop_comments"`
	DropDocstrings bool   `json:"drop_docstrings"`
}

type service struct {
	view    *navigator.View
	learner *adaptiveLearner
	base    string
}

func (service *service) changed(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input changedInput,
) (*mcp.CallToolResult, any, error) {
	options := input.options(navigator.IncludeAll, navigator.ReturnContext, service.base)
	options.MaxPatchLines = defaultInt(input.MaxPatchLines, defaultMaxPatchLines)
	response, err := service.view.WithContext(ctx).Changed(options)
	if err != nil {
		return nil, nil, err
	}
	return service.prepareResponse("changed", input.Response, input, response)
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
	response, err := service.view.WithContext(ctx).Find(input.Query, options)
	if err != nil {
		return nil, nil, err
	}
	return service.prepareResponse("find", input.Response, input, response)
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
	return service.prepareResponse("inspect", input.Response, input, response)
}

func (service *service) outline(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input outlineInput,
) (*mcp.CallToolResult, any, error) {
	options := navigator.Options{
		Base:           service.base,
		Include:        navigator.IncludeDefs,
		Return:         navigator.Return(defaultString(input.Return, string(navigator.ReturnLine))),
		Limit:          defaultInt(input.Limit, defaultLimit),
		MaxCodeLines:   defaultInt(input.MaxCodeLines, defaultMaxCodeLines),
		MaxPatchLines:  defaultMaxPatchLines,
		DropComments:   input.DropComments,
		DropDocstrings: input.DropDocstrings,
	}
	response, err := service.view.WithContext(ctx).Outline(input.Path, options)
	if err != nil {
		return nil, nil, err
	}
	return service.prepareResponse("outline", input.Response, input, response)
}

func (service *service) prepareResponse(
	tool, mode string,
	input, response any,
) (*mcp.CallToolResult, any, error) {
	request, err := newAdaptiveRequest(tool, input)
	if err != nil {
		return nil, nil, err
	}
	budget := service.learner.budget(request)
	result, output, sizing, err := prepareToolResponse(tool, mode, response, budget)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case defaultString(mode, responseAuto) == responseFull:
		service.learner.recordFull(request)
	case sizing.Compacted:
		service.learner.recordCompacted(request)
	default:
		service.learner.recordUncompacted(request)
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

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}
