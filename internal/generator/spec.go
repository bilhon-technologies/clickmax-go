package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type document struct {
	Paths      map[string]map[string]*operation `yaml:"paths"`
	Components struct {
		Schemas map[string]*schema `yaml:"schemas"`
	} `yaml:"components"`
}

type operation struct {
	OperationID string       `yaml:"operationId"`
	Summary     string       `yaml:"summary"`
	Description string       `yaml:"description"`
	Deprecated  bool         `yaml:"deprecated"`
	Parameters  []*parameter `yaml:"parameters"`
	RequestBody *struct {
		Required bool                  `yaml:"required"`
		Content  map[string]*mediaType `yaml:"content"`
	} `yaml:"requestBody"`
	Responses map[string]*struct {
		Content map[string]*mediaType `yaml:"content"`
	} `yaml:"responses"`
}

type mediaType struct {
	Schema *schema `yaml:"schema"`
}

type parameter struct {
	Name        string  `yaml:"name"`
	In          string  `yaml:"in"`
	Required    bool    `yaml:"required"`
	Description string  `yaml:"description"`
	Schema      *schema `yaml:"schema"`
}

// schema keeps only the keywords that decide the Go type. Every other keyword must be listed in
// ignoredKeywords, so a construct the generator does not understand fails generation instead of
// being dropped.
type schema struct {
	Ref                  string       `yaml:"$ref"`
	Type                 typeList     `yaml:"type"`
	Format               any          `yaml:"format"`
	Description          string       `yaml:"description"`
	Enum                 []*yaml.Node `yaml:"-"`
	Const                *yaml.Node   `yaml:"-"`
	Properties           properties   `yaml:"properties"`
	Required             []string     `yaml:"required"`
	AdditionalProperties *additional  `yaml:"additionalProperties"`
	PropertyNames        *schema      `yaml:"propertyNames"`
	Items                *schema      `yaml:"items"`
	AnyOf                []*schema    `yaml:"anyOf"`
	OneOf                []*schema    `yaml:"oneOf"`
	AllOf                []*schema    `yaml:"allOf"`

	line int
}

var ignoredKeywords = map[string]bool{
	"pattern": true, "minLength": true, "maxLength": true, "minimum": true, "maximum": true,
	"exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true, "minItems": true,
	"maxItems": true, "uniqueItems": true, "minProperties": true, "maxProperties": true,
	"default": true, "example": true, "examples": true, "title": true, "readOnly": true,
	"writeOnly": true, "deprecated": true, "$schema": true, "$id": true, "$comment": true,
	"contentEncoding": true, "contentMediaType": true, "discriminator": true,
}

func (s *schema) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: schema is not an object", n.Line)
	}
	type plain schema
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if ignoredKeywords[key] || strings.HasPrefix(key, "x-") {
			continue
		}
		if _, ok := schemaFields[key]; !ok {
			return fmt.Errorf("line %d: unsupported schema keyword %q", n.Content[i].Line, key)
		}
		// Kept as nodes: the tag tells a string from a number or bool, and a null const from an
		// absent one.
		switch key {
		case "const":
			s.Const = n.Content[i+1]
		case "enum":
			if n.Content[i+1].Kind != yaml.SequenceNode {
				return fmt.Errorf("line %d: enum is not a list", n.Content[i].Line)
			}
			s.Enum = n.Content[i+1].Content
		}
	}
	if err := n.Decode((*plain)(s)); err != nil {
		return err
	}
	s.line = n.Line
	return nil
}

var schemaFields = map[string]struct{}{
	"$ref": {}, "type": {}, "format": {}, "description": {}, "enum": {}, "const": {},
	"properties": {}, "required": {}, "additionalProperties": {}, "propertyNames": {},
	"items": {}, "anyOf": {}, "oneOf": {}, "allOf": {},
}

// typeList accepts both `type: x` and `type: [x, y]`.
type typeList []string

func (t *typeList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*t = typeList{n.Value}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*t = list
	return nil
}

type property struct {
	name   string
	schema *schema
}

// properties keeps the spec order so struct fields follow it.
type properties []property

func (p *properties) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: properties is not an object", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		s := new(schema)
		if err := n.Content[i+1].Decode(s); err != nil {
			return err
		}
		*p = append(*p, property{name: n.Content[i].Value, schema: s})
	}
	return nil
}

type additional struct {
	allowed bool
	schema  *schema
}

func (a *additional) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&a.allowed)
	}
	a.allowed = true
	a.schema = new(schema)
	return n.Decode(a.schema)
}

func loadSpec(path string) (*document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc document
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &doc, nil
}
