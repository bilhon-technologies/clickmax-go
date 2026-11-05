package main

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type shapeKind int

const (
	kAny shapeKind = iota
	kNull
	kString
	kTime
	kInt
	kFloat
	kBool
	kEnum
	kObject
	kMap
	kArray
	kRef
)

// shape is a schema reduced to what decides its Go type: unions are resolved, `null` becomes a
// flag and object unions are already flattened.
type shape struct {
	kind     shapeKind
	nullable bool
	desc     string
	values   []string // kEnum
	props    []prop   // kObject
	variants [][]string
	elem     *shape // kArray, kMap
	ref      string // kRef
}

type prop struct {
	name     string
	shape    *shape
	required bool
}

type shaper struct {
	components map[string]*schema
}

func (sh *shaper) shape(s *schema) (*shape, error) {
	out, err := sh.shapeOf(s)
	if err != nil {
		return nil, err
	}
	if s.Description != "" {
		out.desc = s.Description
	}
	return out, nil
}

func (sh *shaper) shapeOf(s *schema) (*shape, error) {
	fail := func(format string, args ...any) (*shape, error) {
		return nil, fmt.Errorf("line %d: %s", s.line, fmt.Sprintf(format, args...))
	}
	if s.Ref != "" {
		name, ok := strings.CutPrefix(s.Ref, "#/components/schemas/")
		if !ok || sh.components[name] == nil {
			return fail("unresolvable $ref %q", s.Ref)
		}
		return &shape{kind: kRef, ref: name}, nil
	}
	variants := append(slices.Clone(s.AnyOf), s.OneOf...)
	if len(variants) > 0 {
		if len(s.Type) > 0 || len(s.Properties) > 0 || len(s.AllOf) > 0 {
			return fail("anyOf/oneOf next to type, properties or allOf")
		}
		shapes := make([]*shape, 0, len(variants))
		for _, v := range variants {
			vs, err := sh.shape(v)
			if err != nil {
				return nil, err
			}
			shapes = append(shapes, vs)
		}
		return union(shapes), nil
	}
	if len(s.AllOf) > 0 {
		return sh.allOf(s)
	}
	if s.Const != nil {
		return sh.constant(s)
	}

	types := slices.Clone([]string(s.Type))
	nullable := false
	if i := slices.Index(types, "null"); i >= 0 && len(types) > 1 {
		types = slices.Delete(types, i, i+1)
		nullable = true
	}
	switch len(types) {
	case 0:
		if len(s.Properties) > 0 || len(s.Enum) > 0 || s.Items != nil {
			return fail("schema without type")
		}
		return &shape{kind: kAny}, nil
	case 1:
	default:
		return &shape{kind: kAny}, nil
	}

	var out *shape
	switch types[0] {
	case "null":
		out = &shape{kind: kNull}
	case "string":
		out = &shape{kind: kString}
		if f, _ := s.Format.(string); f == "date-time" {
			out.kind = kTime
		}
		if len(s.Enum) > 0 {
			out = &shape{kind: kEnum}
			for _, n := range s.Enum {
				if n.Tag == "!!null" {
					nullable = true
					continue
				}
				if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
					return fail("non-string enum value %q", n.Value)
				}
				out.values = appendUnique(out.values, n.Value)
			}
		}
	case "integer":
		out = &shape{kind: kInt}
	case "number":
		out = &shape{kind: kFloat}
	case "boolean":
		out = &shape{kind: kBool}
	case "array":
		if s.Items == nil {
			return fail("array without items")
		}
		elem, err := sh.shape(s.Items)
		if err != nil {
			return nil, err
		}
		out = &shape{kind: kArray, elem: elem}
	case "object":
		var err error
		if out, err = sh.object(s); err != nil {
			return nil, err
		}
	default:
		return fail("unsupported type %q", types[0])
	}
	if len(s.Enum) > 0 && out.kind != kEnum {
		return fail("enum on a %s schema", types[0])
	}
	out.nullable = out.nullable || nullable
	return out, nil
}

func (sh *shaper) object(s *schema) (*shape, error) {
	if s.PropertyNames != nil && !slices.Equal(s.PropertyNames.Type, []string{"string"}) {
		return nil, fmt.Errorf("line %d: propertyNames must be a string schema", s.line)
	}
	var extra *schema
	if s.AdditionalProperties != nil {
		extra = s.AdditionalProperties.schema
	}
	if len(s.Properties) == 0 {
		if extra == nil {
			return &shape{kind: kMap, elem: &shape{kind: kAny}}, nil
		}
		elem, err := sh.shape(extra)
		if err != nil {
			return nil, err
		}
		return &shape{kind: kMap, elem: elem}, nil
	}
	if extra != nil {
		return nil, fmt.Errorf("line %d: object with both properties and additionalProperties", s.line)
	}
	out := &shape{kind: kObject}
	names := make([]string, 0, len(s.Properties))
	for _, p := range s.Properties {
		ps, err := sh.shape(p.schema)
		if err != nil {
			return nil, err
		}
		out.props = append(out.props, prop{name: p.name, shape: ps, required: slices.Contains(s.Required, p.name)})
		names = append(names, p.name)
	}
	for _, r := range s.Required {
		if !slices.Contains(names, r) {
			return nil, fmt.Errorf("line %d: required property %q is not declared", s.line, r)
		}
	}
	return out, nil
}

func (sh *shaper) constant(s *schema) (*shape, error) {
	n := s.Const
	switch n.Tag {
	case "!!null":
		return &shape{kind: kNull}, nil
	case "!!str":
		return &shape{kind: kEnum, values: []string{n.Value}}, nil
	case "!!bool":
		return &shape{kind: kBool}, nil
	case "!!int":
		if slices.Contains(s.Type, "number") {
			return &shape{kind: kFloat}, nil
		}
		return &shape{kind: kInt}, nil
	case "!!float":
		return &shape{kind: kFloat}, nil
	}
	return nil, fmt.Errorf("line %d: unsupported const %q", s.line, n.Value)
}

// allOf only supports parts that agree: unconstrained parts are dropped and the rest must all be
// objects, whose properties are merged.
func (sh *shaper) allOf(s *schema) (*shape, error) {
	if len(s.Type) > 0 || len(s.Properties) > 0 {
		return nil, fmt.Errorf("line %d: allOf next to type or properties", s.line)
	}
	var parts []*shape
	for _, p := range s.AllOf {
		ps, err := sh.shape(p)
		if err != nil {
			return nil, err
		}
		if ps.kind != kAny {
			parts = append(parts, ps)
		}
	}
	if len(parts) == 0 {
		return &shape{kind: kAny}, nil
	}
	out := &shape{kind: kObject, nullable: true}
	for _, p := range parts {
		if p.kind != kObject {
			return nil, fmt.Errorf("line %d: allOf of non-object schemas", s.line)
		}
		out.nullable = out.nullable && p.nullable
		for _, pp := range p.props {
			i := slices.IndexFunc(out.props, func(q prop) bool { return q.name == pp.name })
			if i < 0 {
				out.props = append(out.props, pp)
				continue
			}
			if key(out.props[i].shape) != key(pp.shape) {
				return nil, fmt.Errorf("line %d: allOf parts disagree on property %q", s.line, pp.name)
			}
			out.props[i].required = out.props[i].required || pp.required
		}
	}
	if len(parts) == 1 {
		out.variants = parts[0].variants
	}
	return out, nil
}

// union resolves anyOf/oneOf. Null variants make the result nullable; string enums merge; strings
// of any format collapse to string; objects flatten into one struct whose property types are again
// the union of each variant's property; arrays and maps union their elements; anything else that
// does not agree becomes any.
//
// A variant's description is kept only when it is the sole non-null variant: on a real union it
// describes one alternative, not the whole value.
func union(variants []*shape) *shape {
	var rest []*shape
	nullable := false
	for _, v := range variants {
		if v.kind == kNull {
			nullable = true
			continue
		}
		nullable = nullable || v.nullable
		rest = append(rest, v)
	}
	out := unionOf(rest)
	if out.kind != kAny {
		out.nullable = nullable
	}
	if len(rest) != 1 {
		out.desc = ""
	}
	return out
}

func unionOf(vs []*shape) *shape {
	if len(vs) == 0 {
		return &shape{kind: kAny}
	}
	if len(vs) == 1 {
		c := *vs[0]
		return &c
	}
	kinds := map[shapeKind]bool{}
	for _, v := range vs {
		kinds[v.kind] = true
	}
	only := func(ks ...shapeKind) bool {
		for k := range kinds {
			if !slices.Contains(ks, k) {
				return false
			}
		}
		return true
	}
	switch {
	case kinds[kAny]:
		return &shape{kind: kAny}
	case only(kEnum):
		out := &shape{kind: kEnum}
		for _, v := range vs {
			for _, val := range v.values {
				out.values = appendUnique(out.values, val)
			}
		}
		return out
	case only(kTime):
		return &shape{kind: kTime}
	case only(kString, kEnum, kTime):
		return &shape{kind: kString}
	case only(kInt), only(kFloat), only(kBool):
		return &shape{kind: vs[0].kind}
	case only(kObject):
		return flatten(vs)
	case only(kArray), only(kMap):
		elems := make([]*shape, len(vs))
		for i, v := range vs {
			elems[i] = v.elem
		}
		return &shape{kind: vs[0].kind, elem: union(elems)}
	case only(kRef):
		for _, v := range vs[1:] {
			if v.ref != vs[0].ref {
				return &shape{kind: kAny}
			}
		}
		return &shape{kind: kRef, ref: vs[0].ref}
	}
	return &shape{kind: kAny}
}

func flatten(vs []*shape) *shape {
	out := &shape{kind: kObject}
	var order []string
	byName := map[string][]*shape{}
	inAll := map[string]int{}
	addVariant := func(names []string) {
		for _, seen := range out.variants {
			if slices.Equal(seen, names) {
				return
			}
		}
		out.variants = append(out.variants, names)
	}
	for _, v := range vs {
		if len(v.variants) > 0 {
			for _, names := range v.variants {
				addVariant(names)
			}
		} else {
			names := make([]string, len(v.props))
			for i, p := range v.props {
				names[i] = p.name
			}
			addVariant(names)
		}
		for _, p := range v.props {
			if _, ok := byName[p.name]; !ok {
				order = append(order, p.name)
			}
			byName[p.name] = append(byName[p.name], p.shape)
			if p.required {
				inAll[p.name]++
			}
		}
	}
	for _, name := range order {
		merged := union(byName[name])
		for _, s := range byName[name] {
			if merged.desc == "" {
				merged.desc = s.desc
			}
		}
		out.props = append(out.props, prop{name: name, shape: merged, required: inAll[name] == len(vs)})
	}
	return out
}

// key is a canonical form used to compare shapes.
func key(s *shape) string {
	var b strings.Builder
	var walk func(*shape)
	walk = func(s *shape) {
		fmt.Fprintf(&b, "%d/%t", s.kind, s.nullable)
		switch s.kind {
		case kEnum:
			fmt.Fprintf(&b, "%q", s.values)
		case kRef:
			b.WriteString(s.ref)
		case kArray, kMap:
			b.WriteString("(")
			walk(s.elem)
			b.WriteString(")")
		case kObject:
			b.WriteString("{")
			for _, p := range s.props {
				fmt.Fprintf(&b, "%q:%t:", p.name, p.required)
				walk(p.shape)
				b.WriteString(",")
			}
			b.WriteString("}")
		}
	}
	walk(s)
	return b.String()
}

func appendUnique(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}
