package repoviewmcp

const (
	defaultContext        = 5
	defaultLimit          = 50
	defaultMaxCodeLines   = 80
	defaultMaxPatchLines  = 400
	maximumContext        = 50
	maximumLimit          = 200
	maximumMaxCodeLines   = 400
	maximumMaxPatchLines  = 2000
	maximumFilterCount    = 32
	maximumFilterLength   = 512
	maximumSelectorLength = 4096
)

func findInputSchema() map[string]any {
	properties := commonInputProperties("scope")
	properties["symbol"] = stringSchema(
		"Exact symbol name to find.",
		maximumSelectorLength,
	)
	properties["include"] = enumSchema(
		"Select definitions, references, or both.",
		"both",
		"defs", "refs", "both",
	)
	properties["changed_only"] = map[string]any{
		"type":        "boolean",
		"default":     false,
		"description": "Search only files changed from the configured base commit.",
	}
	properties["include_comments"] = map[string]any{
		"type":        "boolean",
		"default":     false,
		"description": "Include matches that occur only in comments.",
	}
	properties["include_strings"] = map[string]any{
		"type":        "boolean",
		"default":     false,
		"description": "Include matches that occur only in string literals.",
	}
	return objectSchema(properties, "symbol")
}

func inspectInputSchema() map[string]any {
	properties := commonInputProperties("scope")
	properties["location"] = stringSchema(
		"Repository-relative source location in PATH:LINE form.",
		maximumSelectorLength,
	)
	properties["include"] = enumSchema(
		"Select the enclosing scope and optional related results.",
		"scope",
		"symbol", "scope", "defs", "refs", "both", "imports", "all",
	)
	properties["changed_only"] = map[string]any{
		"type":        "boolean",
		"default":     false,
		"description": "Limit related symbol results to files changed from the configured base commit.",
	}
	properties["include_comments"] = map[string]any{
		"type":        "boolean",
		"default":     false,
		"description": "Include related matches that occur only in comments.",
	}
	properties["include_strings"] = map[string]any{
		"type":        "boolean",
		"default":     false,
		"description": "Include related matches that occur only in string literals.",
	}
	return objectSchema(properties, "location")
}

func outlineInputSchema() map[string]any {
	properties := commonInputProperties("line")
	delete(properties, "context")
	delete(properties, "path_globs")
	delete(properties, "exclude_globs")
	properties["path"] = stringSchema(
		"Repository-relative source file path.",
		maximumSelectorLength,
	)
	return objectSchema(properties, "path")
}

func changedInputSchema() map[string]any {
	properties := commonInputProperties("context")
	properties["max_patch_lines"] = boundedIntegerSchema(
		"Maximum number of patch lines to return.",
		defaultMaxPatchLines,
		maximumMaxPatchLines,
	)
	return objectSchema(properties)
}

func commonInputProperties(defaultReturn string) map[string]any {
	return map[string]any{
		"return": enumSchema(
			"Shape of each returned source result.",
			defaultReturn,
			"locations", "line", "context", "scope",
		),
		"context": boundedIntegerSchema(
			"Context lines on each side for context results.",
			defaultContext,
			maximumContext,
		),
		"limit": boundedIntegerSchema(
			"Maximum number of results.",
			defaultLimit,
			maximumLimit,
		),
		"path_globs":    filterSchema("Include path glob or substring filters."),
		"exclude_globs": filterSchema("Exclude path glob or substring filters."),
		"max_code_lines": boundedIntegerSchema(
			"Maximum embedded source lines per result.",
			defaultMaxCodeLines,
			maximumMaxCodeLines,
		),
		"drop_comments": map[string]any{
			"type":        "boolean",
			"default":     false,
			"description": "Remove comments from embedded source snippets.",
		},
		"drop_docstrings": map[string]any{
			"type":        "boolean",
			"default":     false,
			"description": "Remove Python docstrings from embedded source snippets.",
		},
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) != 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string, maximum int) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
		"minLength":   1,
		"maxLength":   maximum,
	}
}

func enumSchema(description, defaultValue string, values ...string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
		"default":     defaultValue,
		"enum":        values,
	}
}

func boundedIntegerSchema(
	description string,
	defaultValue, maximum int,
) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
		"default":     defaultValue,
		"minimum":     1,
		"maximum":     maximum,
	}
}

func filterSchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"maxItems":    maximumFilterCount,
		"items": stringSchema(
			"Repository-relative path filter.",
			maximumFilterLength,
		),
	}
}
