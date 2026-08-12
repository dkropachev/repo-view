package commandrunner

import (
	"errors"
	"fmt"
	"strings"
)

const (
	maximumCommandBytes = 64 << 10
	maximumPipelineSize = 16
	maximumArguments    = 256
	maximumArgumentSize = 32 << 10
)

type commandProgram struct {
	pipeline [][]string
}

// parseCommandProgram parses the closed command language accepted from Codex.
// It is deliberately not a shell: a program is exactly one pipeline of argv
// commands. Quotes and backslashes construct literal argv bytes and do not
// perform expansion. Every shell control, expansion, redirection, and command
// list operator is rejected.
func parseCommandProgram(input string) (commandProgram, error) {
	if input == "" {
		return commandProgram{}, errors.New("command is empty")
	}
	if len(input) > maximumCommandBytes {
		return commandProgram{}, errors.New("command exceeds the size limit")
	}

	parser := commandParser{input: input}
	program := commandProgram{pipeline: make([][]string, 0, 1)}
	for {
		arguments, err := parser.parseStage()
		if err != nil {
			return commandProgram{}, err
		}
		if err := validateStageGrammar(arguments); err != nil {
			return commandProgram{}, err
		}
		program.pipeline = append(program.pipeline, arguments)
		if len(program.pipeline) > maximumPipelineSize {
			return commandProgram{}, errors.New("pipeline exceeds the stage limit")
		}

		parser.skipHorizontalSpace()
		if parser.offset == len(parser.input) {
			return program, nil
		}
		if parser.input[parser.offset] != '|' {
			return commandProgram{}, parser.forbiddenSyntax(parser.input[parser.offset])
		}
		parser.offset++
		if parser.offset < len(parser.input) && parser.input[parser.offset] == '|' {
			return commandProgram{}, errors.New("conditional operator || is forbidden")
		}
		parser.skipHorizontalSpace()
		if parser.offset == len(parser.input) {
			return commandProgram{}, errors.New("pipeline has an empty final stage")
		}
	}
}

type commandParser struct {
	input  string
	offset int
}

func (parser *commandParser) parseStage() ([]string, error) {
	arguments := make([]string, 0, 4)
	for {
		parser.skipHorizontalSpace()
		if parser.offset == len(parser.input) || parser.input[parser.offset] == '|' {
			if len(arguments) == 0 {
				return nil, errors.New("pipeline has an empty stage")
			}
			return arguments, nil
		}
		argument, err := parser.parseWord()
		if err != nil {
			return nil, err
		}
		arguments = append(arguments, argument)
		if len(arguments) > maximumArguments {
			return nil, errors.New("command exceeds the argument limit")
		}
	}
}

func (parser *commandParser) parseWord() (string, error) {
	var word strings.Builder
	started := false
	for parser.offset < len(parser.input) {
		character := parser.input[parser.offset]
		switch character {
		case ' ', '\t':
			if started {
				return word.String(), nil
			}
			return "", errors.New("internal parser error: word begins with whitespace")
		case '|':
			if started {
				return word.String(), nil
			}
			return "", errors.New("pipeline has an empty stage")
		case '\'', '"':
			started = true
			if err := parser.appendQuoted(&word, character); err != nil {
				return "", err
			}
		case '\\':
			started = true
			parser.offset++
			if parser.offset == len(parser.input) || parser.input[parser.offset] == '\n' ||
				parser.input[parser.offset] == '\r' || parser.input[parser.offset] == 0 {
				return "", errors.New("invalid trailing or multiline escape")
			}
			word.WriteByte(parser.input[parser.offset])
			parser.offset++
		case '$', '`':
			return "", errors.New("variable or command expansion is forbidden")
		case '*', '?', '[', ']':
			return "", errors.New("unquoted path or pattern expansion is forbidden")
		case '~':
			if !started {
				return "", errors.New("home-directory expansion is forbidden")
			}
			started = true
			word.WriteByte(character)
			parser.offset++
		case '#':
			if !started {
				return "", errors.New("comments are forbidden")
			}
			started = true
			word.WriteByte(character)
			parser.offset++
		case ';', '&', '<', '>', '(', ')', '{', '}', '\n', '\r', 0:
			return "", parser.forbiddenSyntax(character)
		default:
			started = true
			word.WriteByte(character)
			parser.offset++
		}
		if word.Len() > maximumArgumentSize {
			return "", errors.New("command argument exceeds the size limit")
		}
	}
	if !started {
		return "", errors.New("command word is empty")
	}
	return word.String(), nil
}

func (parser *commandParser) appendQuoted(word *strings.Builder, quote byte) error {
	parser.offset++
	for parser.offset < len(parser.input) {
		character := parser.input[parser.offset]
		if character == quote {
			parser.offset++
			return nil
		}
		if character == '\n' || character == '\r' || character == 0 {
			return errors.New("multiline and NUL-containing words are forbidden")
		}
		if quote == '"' && (character == '$' || character == '`') {
			return errors.New("variable or command expansion is forbidden")
		}
		if quote == '"' && character == '\\' {
			parser.offset++
			if parser.offset == len(parser.input) {
				return errors.New("unterminated double-quoted escape")
			}
			escaped := parser.input[parser.offset]
			if escaped == '"' || escaped == '\\' {
				word.WriteByte(escaped)
			} else {
				word.WriteByte('\\')
				word.WriteByte(escaped)
			}
			parser.offset++
			if word.Len() > maximumArgumentSize {
				return errors.New("command argument exceeds the size limit")
			}
			continue
		}
		word.WriteByte(character)
		parser.offset++
		if word.Len() > maximumArgumentSize {
			return errors.New("command argument exceeds the size limit")
		}
	}
	return fmt.Errorf("unterminated %q quote", quote)
}

func (parser *commandParser) skipHorizontalSpace() {
	for parser.offset < len(parser.input) &&
		(parser.input[parser.offset] == ' ' || parser.input[parser.offset] == '\t') {
		parser.offset++
	}
}

func (parser *commandParser) forbiddenSyntax(character byte) error {
	if character == '&' && parser.offset+1 < len(parser.input) &&
		parser.input[parser.offset+1] == '&' {
		return errors.New("conditional operator && is forbidden")
	}
	return fmt.Errorf("shell syntax %q is forbidden", character)
}

func validateStageGrammar(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("command stage is empty")
	}
	command := arguments[0]
	if _, forbidden := forbiddenCommandWords[command]; forbidden {
		return fmt.Errorf("shell command word %q is forbidden", command)
	}
	if isAssignmentWord(command) {
		return errors.New("variable assignment is forbidden")
	}
	return nil
}

func isAssignmentWord(word string) bool {
	separator := strings.IndexByte(word, '=')
	if separator <= 0 {
		return false
	}
	name := word[:separator]
	for index, character := range name {
		if character == '_' || character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

var forbiddenCommandWords = map[string]struct{}{
	"!": {}, ".": {}, "[[": {}, "alias": {}, "bg": {}, "break": {},
	"builtin": {}, "case": {}, "cd": {}, "command": {}, "continue": {},
	"coproc": {}, "declare": {}, "do": {}, "done": {}, "elif": {},
	"else": {}, "enable": {}, "esac": {}, "eval": {}, "exec": {},
	"exit": {}, "export": {}, "false": {}, "fg": {}, "fi": {},
	"for": {}, "function": {}, "getopts": {}, "hash": {}, "help": {},
	"history": {}, "if": {}, "jobs": {}, "let": {}, "local": {},
	"logout": {}, "mapfile": {}, "popd": {}, "printf": {}, "pushd": {},
	"read": {}, "readarray": {}, "readonly": {}, "return": {},
	"select": {}, "set": {}, "shift": {}, "shopt": {}, "source": {},
	"suspend": {}, "test": {}, "then": {}, "time": {}, "times": {},
	"trap": {}, "true": {}, "type": {}, "typeset": {}, "ulimit": {},
	"umask": {}, "unalias": {}, "unset": {}, "until": {}, "wait": {},
	"while": {},
}
