package main

import (
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

var (
	rustSyntaxLanguage       = sitter.NewLanguage(tree_sitter_rust.Language())
	typeScriptSyntaxLanguage = sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())
	typeScriptTSXLanguage    = sitter.NewLanguage(tree_sitter_typescript.LanguageTSX())
)

func newRustParser() (*sitter.Parser, error) {
	return newParserForLanguage(rustSyntaxLanguage)
}

func newTypeScriptParser(isTSX bool) (*sitter.Parser, error) {
	if isTSX {
		return newParserForLanguage(typeScriptTSXLanguage)
	}
	return newParserForLanguage(typeScriptSyntaxLanguage)
}

func newParserForLanguage(language *sitter.Language) (*sitter.Parser, error) {
	parser := sitter.NewParser()
	if err := parser.SetLanguage(language); err != nil {
		parser.Close()
		return nil, err
	}
	return parser, nil
}

func isTypeScriptTSXPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".tsx")
}

func nodeText(node *sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	return node.Utf8Text(source)
}

func unquoteStringLiteral(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	unquoted, err := strconv.Unquote(raw)
	if err != nil {
		return raw
	}
	return unquoted
}

func walkTreePreOrder(root *sitter.Node, visit func(*sitter.Node)) {
	if root == nil || visit == nil {
		return
	}

	stack := []*sitter.Node{root}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		visit(node)

		for i := int(node.ChildCount()) - 1; i >= 0; i-- {
			child := node.Child(uint(i))
			if child != nil {
				stack = append(stack, child)
			}
		}
	}
}
