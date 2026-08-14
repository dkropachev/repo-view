package scopesiftermcp

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
	properties := commonInputProperties("locations")
	for _, name := range []string{
		"response", "return", "context", "max_code_lines", "drop_comments", "drop_docstrings",
	} {
		delete(properties, name)
	}
	properties["query"] = stringSchema(maximumSelectorLength)
	properties["match"] = enumSchema(
		"auto",
		"auto", "symbol", "path",
	)
	properties["include"] = enumSchema(
		"both",
		"defs", "refs", "both",
	)
	properties["changed_only"] = booleanSchema(false)
	return objectSchema(properties, "query")
}

func inspectInputSchema() map[string]any {
	properties := commonInputProperties("scope")
	for _, name := range []string{
		"response", "return", "context", "max_code_lines", "drop_comments", "drop_docstrings",
	} {
		delete(properties, name)
	}
	properties["location"] = stringSchema(maximumSelectorLength)
	properties["include"] = enumSchema(
		"scope",
		"symbol", "scope", "defs", "refs", "both", "imports", "all",
	)
	properties["changed_only"] = booleanSchema(false)
	return objectSchema(properties, "location")
}

func outlineInputSchema() map[string]any {
	properties := map[string]any{
		"path": stringSchema(maximumSelectorLength),
		"limit": boundedIntegerSchema(
			defaultLimit,
			maximumLimit,
		),
	}
	return objectSchema(properties, "path")
}

func changedInputSchema() map[string]any {
	properties := commonInputProperties("context")
	for _, name := range []string{
		"response", "return", "context", "max_code_lines", "drop_comments", "drop_docstrings",
	} {
		delete(properties, name)
	}
	return objectSchema(properties)
}

func commonInputProperties(defaultReturn string) map[string]any {
	return map[string]any{
		"response": enumSchema(
			"auto",
			"auto", "full",
		),
		"return": enumSchema(
			defaultReturn,
			"locations", "line", "context", "scope",
		),
		"context": boundedIntegerSchema(
			defaultContext,
			maximumContext,
		),
		"limit": boundedIntegerSchema(
			defaultLimit,
			maximumLimit,
		),
		"path_globs":    filterSchema(),
		"exclude_globs": filterSchema(),
		"max_code_lines": boundedIntegerSchema(
			defaultMaxCodeLines,
			maximumMaxCodeLines,
		),
		"drop_comments":   booleanSchema(false),
		"drop_docstrings": booleanSchema(false),
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

func stringSchema(maximum int) map[string]any {
	return map[string]any{
		"type":      "string",
		"minLength": 1,
		"maxLength": maximum,
	}
}

func enumSchema(defaultValue string, values ...string) map[string]any {
	return map[string]any{
		"type":    "string",
		"default": defaultValue,
		"enum":    values,
	}
}

func boundedIntegerSchema(
	defaultValue, maximum int,
) map[string]any {
	return map[string]any{
		"type":    "integer",
		"default": defaultValue,
		"minimum": 1,
		"maximum": maximum,
	}
}

func booleanSchema(defaultValue bool) map[string]any {
	return map[string]any{
		"type":    "boolean",
		"default": defaultValue,
	}
}

func filterSchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": maximumFilterCount,
		"items":    stringSchema(maximumFilterLength),
	}
}
