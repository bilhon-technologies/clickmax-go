package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

const kNamed shapeKind = -1

type goType struct {
	kind     shapeKind // a scalar kind, kAny, kArray, kMap or kNamed
	elem     *goType
	decl     *decl
	cyclic   bool // refers to a type still being built, so a value field would be infinite
	nullable bool // set on array items and map values only; fields carry their own flag
}

type declKind int

const (
	declStruct declKind = iota
	declEnum
	declDefined
	declParams
)

type decl struct {
	name     string
	kind     declKind
	doc      []string
	fields   []*field
	values   []string // declEnum
	consts   []string // declEnum, Go names of values
	under    *goType  // declDefined
	variants [][]string
	refs     []*decl
	owners   map[string]bool // resource slugs; "" is the root
	bodyOf   []string        // doc links of the methods taking it as body
	outOf    []string        // doc links of the methods returning it
	paramsOf string          // doc link of the method taking it as query parameters

	placeholder string // spec name of a component referenced while it is still being built
}

type field struct {
	name     string
	jsonName string
	typ      *goType
	required bool
	nullable bool
	request  bool
	doc      string
}

type service struct {
	slug    string
	name    string // Client field
	methods []*method
}

type method struct {
	name     string
	httpVerb string
	path     string
	summary  string
	desc     string
	deprec   bool
	params   *decl
	body     *decl
	out      *decl
	resource string // slug, "" for root
	link     string // doc link, e.g. [TagsService.List]
}

type model struct {
	services []*service
	root     []*method
	decls    map[string]*decl
}

type builder struct {
	sh       *shaper
	shapes   map[string]*shape
	request  map[string]bool
	compDecl map[string]*decl
	building map[string]bool
	decls    map[string]*decl
	claimed  map[string]string
	reqCtx   bool
	cycles   bool
}

func (b *builder) claim(name, origin string) error {
	if prev, ok := b.claimed[name]; ok {
		return fmt.Errorf("name collision: %s is used by both %s and %s", name, prev, origin)
	}
	if !exported(name) {
		return fmt.Errorf("%s: %q is not an exported Go identifier", origin, name)
	}
	b.claimed[name] = origin
	return nil
}

// build turns the spec into the model to render. reserved maps the identifiers declared by the
// hand-written runtime to where they come from; generated names must not take them.
func build(doc *document, reserved map[string]string) (*model, error) {
	b := &builder{
		sh:       &shaper{components: doc.Components.Schemas},
		shapes:   map[string]*shape{},
		request:  map[string]bool{},
		compDecl: map[string]*decl{},
		building: map[string]bool{},
		decls:    map[string]*decl{},
		claimed:  maps.Clone(reserved),
	}
	b.claimed["Client"] = "the generated Client"

	type rawOp struct {
		path, verb string
		op         *operation
		body, out  string
	}
	var ops []rawOp
	for _, path := range slices.Sorted(maps.Keys(doc.Paths)) {
		item := doc.Paths[path]
		for _, verb := range slices.Sorted(maps.Keys(item)) {
			op := item[verb]
			where := strings.ToUpper(verb) + " " + path
			if !slices.Contains([]string{"get", "post", "put", "patch", "delete"}, verb) {
				return nil, fmt.Errorf("%s: unsupported path item key %q", path, verb)
			}
			r := rawOp{path: path, verb: verb, op: op}
			var err error
			if r.out, err = responseRef(op); err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			if op.RequestBody != nil {
				if r.body, err = bodyRef(op); err != nil {
					return nil, fmt.Errorf("%s: %w", where, err)
				}
				if len(op.Parameters) > 0 {
					return nil, fmt.Errorf("%s: has both a request body and query parameters", where)
				}
				if err := b.markRequest(r.body); err != nil {
					return nil, err
				}
			}
			ops = append(ops, r)
		}
	}

	// Only components reachable from a request body or a 200 response become types. The rest,
	// such as ApiError behind the 4XX/5XX responses, are covered by the runtime's Error or unused.
	var roots []string
	for _, r := range ops {
		roots = append(roots, r.out)
		if r.body != "" {
			roots = append(roots, r.body)
		}
	}
	slices.Sort(roots)
	for _, name := range slices.Compact(roots) {
		if _, err := b.component(name); err != nil {
			return nil, err
		}
	}
	if b.cycles {
		b.fixCycles()
	}

	m := &model{decls: b.decls}
	services := map[string]*service{}
	rootNames := map[string]string{}
	for _, r := range ops {
		where := strings.ToUpper(r.verb) + " " + r.path
		segs := strings.Split(strings.Trim(r.path, "/"), "/")
		if len(segs) > 2 || segs[0] == "" {
			return nil, fmt.Errorf("%s: %d path segments; expected /<resource>/<action> or /<name>", where, len(segs))
		}
		mt := &method{
			name:     pascal(segs[len(segs)-1]),
			httpVerb: strings.ToUpper(r.verb),
			path:     r.path,
			summary:  r.op.Summary,
			desc:     r.op.Description,
			deprec:   r.op.Deprecated,
			out:      b.compDecl[r.out],
		}
		if r.body != "" {
			mt.body = b.compDecl[r.body]
		}
		if !exported(mt.name) {
			return nil, fmt.Errorf("%s: segment %q is not a valid name", where, segs[len(segs)-1])
		}
		var roots []*decl
		if len(segs) == 1 {
			if prev, ok := rootNames[mt.name]; ok {
				return nil, fmt.Errorf("%s and %s both map to the root method %s", prev, where, mt.name)
			}
			rootNames[mt.name] = where
			mt.link = "[Client." + mt.name + "]"
			m.root = append(m.root, mt)
		} else {
			mt.resource = segs[0]
			svc := services[segs[0]]
			if svc == nil {
				svc = &service{slug: segs[0], name: pascal(segs[0])}
				if err := b.claim(svc.name+"Service", "the service of /"+segs[0]); err != nil {
					return nil, err
				}
				services[segs[0]] = svc
			}
			for _, other := range svc.methods {
				if other.name == mt.name {
					return nil, fmt.Errorf("%s and %s both map to %sService.%s", other.httpVerb+" "+other.path, where, svc.name, mt.name)
				}
			}
			mt.link = "[" + svc.name + "Service." + mt.name + "]"
			svc.methods = append(svc.methods, mt)
		}
		if len(r.op.Parameters) > 0 {
			p, err := b.params(r.op, where)
			if err != nil {
				return nil, err
			}
			p.paramsOf = mt.link
			mt.params = p
			roots = append(roots, p)
		}
		roots = append(roots, mt.out)
		mt.out.outOf = append(mt.out.outOf, mt.link)
		if mt.body != nil {
			roots = append(roots, mt.body)
			mt.body.bodyOf = append(mt.body.bodyOf, mt.link)
		}
		for _, d := range roots {
			own(d, mt.resource)
		}
	}
	if err := b.constants(); err != nil {
		return nil, err
	}
	for _, slug := range slices.Sorted(maps.Keys(services)) {
		svc := services[slug]
		if _, ok := rootNames[svc.name]; ok {
			return nil, fmt.Errorf("%q is both a resource and a root method", svc.name)
		}
		m.services = append(m.services, svc)
	}
	for name, d := range b.decls {
		if len(d.owners) == 0 {
			return nil, fmt.Errorf("%s is not used by any operation", name)
		}
	}
	return m, nil
}

// constants names enum values once every type name is taken. A constant that would take the name
// of a type (enum FlowsListInputOrderBy has the value "mode" and FlowsListInput has the field
// orderByMode) gets a Value suffix, so type names never depend on enum values.
func (b *builder) constants() error {
	for _, name := range slices.Sorted(maps.Keys(b.decls)) {
		d := b.decls[name]
		if d.kind != declEnum {
			continue
		}
		for _, v := range d.values {
			c := d.name + pascal(v)
			if v == "" {
				c = d.name + "Empty"
			} else if pascal(v) == "" {
				return fmt.Errorf("%s: enum value %q has no letters or digits", d.name, v)
			}
			if other := b.decls[c]; other != nil {
				c += "Value"
			}
			if err := b.claim(c, fmt.Sprintf("the %q value of %s", v, d.name)); err != nil {
				return err
			}
			d.consts = append(d.consts, c)
		}
	}
	return nil
}

func own(d *decl, resource string) {
	if d.owners[resource] {
		return
	}
	if d.owners == nil {
		d.owners = map[string]bool{}
	}
	d.owners[resource] = true
	for _, r := range d.refs {
		own(r, resource)
	}
}

func refName(s *schema) (string, error) {
	if s == nil || s.Ref == "" {
		return "", fmt.Errorf("schema must be a $ref to components.schemas")
	}
	name, ok := strings.CutPrefix(s.Ref, "#/components/schemas/")
	if !ok {
		return "", fmt.Errorf("unresolvable $ref %q", s.Ref)
	}
	return name, nil
}

func bodyRef(op *operation) (string, error) {
	if !op.RequestBody.Required {
		return "", fmt.Errorf("optional request bodies are not supported")
	}
	if len(op.RequestBody.Content) != 1 || op.RequestBody.Content["application/json"] == nil {
		return "", fmt.Errorf("request body must be application/json only")
	}
	return refName(op.RequestBody.Content["application/json"].Schema)
}

func responseRef(op *operation) (string, error) {
	for code := range op.Responses {
		if code != "200" && (code[0] == '1' || code[0] == '2' || code[0] == '3') {
			return "", fmt.Errorf("unsupported response %s", code)
		}
	}
	ok := op.Responses["200"]
	if ok == nil || len(ok.Content) != 1 || ok.Content["application/json"] == nil {
		return "", fmt.Errorf("response 200 must be application/json")
	}
	return refName(ok.Content["application/json"].Schema)
}

// markRequest flags a component, and every component it refers to, as reachable from a request
// body: their optional nullable fields need Nullable to send an explicit null.
func (b *builder) markRequest(name string) error {
	if b.request[name] {
		return nil
	}
	b.request[name] = true
	s, err := b.shapeOf(name)
	if err != nil {
		return err
	}
	var walk func(*shape) error
	walk = func(s *shape) error {
		switch s.kind {
		case kRef:
			return b.markRequest(s.ref)
		case kArray, kMap:
			return walk(s.elem)
		case kObject:
			for _, p := range s.props {
				if err := walk(p.shape); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(s)
}

// shapeOf shapes a component on first use, so components no operation reaches are never read.
func (b *builder) shapeOf(name string) (*shape, error) {
	if s := b.shapes[name]; s != nil {
		return s, nil
	}
	sch := b.sh.components[name]
	if sch == nil {
		return nil, fmt.Errorf("unknown component %q", name)
	}
	s, err := b.sh.shape(sch)
	if err != nil {
		return nil, fmt.Errorf("components.schemas.%s: %w", name, err)
	}
	b.shapes[name] = s
	return s, nil
}

func (b *builder) component(name string) (*goType, error) {
	if d := b.compDecl[name]; d != nil {
		return &goType{kind: kNamed, decl: d}, nil
	}
	if b.building[name] {
		b.cycles = true
		return &goType{kind: kNamed, decl: &decl{placeholder: name}, cyclic: true}, nil
	}
	b.building[name] = true
	defer delete(b.building, name)
	saved := b.reqCtx
	b.reqCtx = b.request[name]
	defer func() { b.reqCtx = saved }()

	// Spec names go through pascal too, so AiAgentsGetOutput reads AIAgentsGetOutput like the
	// AIAgentsGetParams named from the operationId next to it.
	goName := pascal(name)
	s, err := b.shapeOf(name)
	if err != nil {
		return nil, err
	}
	t, err := b.typeOf(s, goName, "")
	if err != nil {
		return nil, err
	}
	if t.kind != kNamed || t.decl.name != goName {
		d := &decl{name: goName, kind: declDefined, under: t, refs: namedIn(t)}
		if err := b.add(d, "the schema component "+name, s.desc); err != nil {
			return nil, err
		}
		t = &goType{kind: kNamed, decl: d}
	}
	b.compDecl[name] = t.decl
	return t, nil
}

func namedIn(t *goType) []*decl {
	for ; t != nil; t = t.elem {
		if t.kind == kNamed {
			return []*decl{t.decl}
		}
	}
	return nil
}

// fixCycles swaps the placeholder decls of recursive references for the finished ones.
func (b *builder) fixCycles() {
	for _, d := range b.decls {
		for i, r := range d.refs {
			if r.placeholder != "" {
				d.refs[i] = b.compDecl[r.placeholder]
			}
		}
		var fix func(t *goType)
		fix = func(t *goType) {
			if t == nil {
				return
			}
			if t.kind == kNamed && t.decl.placeholder != "" {
				t.decl = b.compDecl[t.decl.placeholder]
			}
			fix(t.elem)
		}
		for _, f := range d.fields {
			fix(f.typ)
		}
		fix(d.under)
	}
}

func (b *builder) add(d *decl, origin, desc string) error {
	if err := b.claim(d.name, origin); err != nil {
		return err
	}
	if desc != "" {
		d.doc = append(d.doc, desc)
	}
	b.decls[d.name] = d
	return nil
}

// typeOf names the inline objects and enums of s after hint; where is the doc link of the
// field it belongs to, empty for a component.
func (b *builder) typeOf(s *shape, hint, where string) (*goType, error) {
	switch s.kind {
	case kAny, kNull:
		return &goType{kind: kAny}, nil
	case kString, kTime, kInt, kFloat, kBool:
		return &goType{kind: s.kind}, nil
	case kRef:
		return b.component(s.ref)
	case kArray, kMap:
		suffix, phrase := "Item", "an element of "
		if s.kind == kMap {
			suffix, phrase = "Value", "a value of "
		}
		inner := phrase + where
		if where == "" {
			inner = phrase + "[" + hint + "]"
		}
		elem, err := b.typeOf(s.elem, hint+suffix, inner)
		if err != nil {
			return nil, err
		}
		elem.nullable = s.elem.nullable
		return &goType{kind: s.kind, elem: elem}, nil
	case kEnum:
		d := &decl{name: hint, kind: declEnum, values: s.values}
		if err := b.add(d, origin(hint, where), docFor(hint, where, s.desc)); err != nil {
			return nil, err
		}
		return &goType{kind: kNamed, decl: d}, nil
	case kObject:
		d := &decl{name: hint, kind: declStruct, variants: s.variants}
		if err := b.add(d, origin(hint, where), docFor(hint, where, s.desc)); err != nil {
			return nil, err
		}
		seen := map[string]string{}
		for _, p := range s.props {
			fname := fieldName(p.name)
			if prev, ok := seen[fname]; ok {
				return nil, fmt.Errorf("%s: properties %q and %q both map to field %s", hint, prev, p.name, fname)
			}
			if !exported(fname) {
				return nil, fmt.Errorf("%s: property %q does not map to an exported field name", hint, p.name)
			}
			seen[fname] = p.name
			ft, err := b.typeOf(p.shape, hint+fname, "["+hint+"."+fname+"]")
			if err != nil {
				return nil, err
			}
			d.refs = append(d.refs, namedIn(ft)...)
			d.fields = append(d.fields, &field{
				name:     fname,
				jsonName: p.name,
				typ:      ft,
				required: p.required,
				nullable: p.shape.nullable,
				request:  b.reqCtx,
				doc:      p.shape.desc,
			})
		}
		return &goType{kind: kNamed, decl: d}, nil
	}
	return nil, fmt.Errorf("%s: unhandled shape kind %d", hint, s.kind)
}

// fieldName maps a property name to a field name. Numeric keys such as the `50`..`900` steps of a
// color scale get an N prefix, since an identifier cannot start with a digit.
func fieldName(prop string) string {
	name := pascal(prop)
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "N" + name
	}
	return name
}

func origin(hint, where string) string {
	if where == "" {
		return "the schema component " + hint
	}
	return "the type of " + where
}

func docFor(hint, where, desc string) string {
	if where == "" {
		return desc
	}
	lead := hint + " is the type of " + where + "."
	if !strings.HasPrefix(where, "[") {
		lead = hint + " is " + where + "."
	}
	if desc == "" {
		return lead
	}
	return lead + "\n\n" + desc
}

// params builds the query parameter struct of an operation. Only scalar and string enum
// parameters exist in the API, so anything else fails.
func (b *builder) params(op *operation, where string) (*decl, error) {
	if op.OperationID == "" {
		return nil, fmt.Errorf("%s: query parameters need an operationId to name their struct", where)
	}
	name := pascal(op.OperationID) + "Params"
	d := &decl{name: name, kind: declParams}
	if err := b.add(d, "the query parameters of "+where, ""); err != nil {
		return nil, err
	}
	seen := map[string]string{}
	for _, p := range op.Parameters {
		if p.In != "query" {
			return nil, fmt.Errorf("%s: parameter %q is in %s; only query parameters are supported", where, p.Name, p.In)
		}
		if p.Schema == nil {
			return nil, fmt.Errorf("%s: parameter %q has no schema", where, p.Name)
		}
		s, err := b.sh.shape(p.Schema)
		if err != nil {
			return nil, fmt.Errorf("%s: parameter %q: %w", where, p.Name, err)
		}
		fname := fieldName(p.Name)
		if prev, ok := seen[fname]; ok {
			return nil, fmt.Errorf("%s: parameters %q and %q both map to field %s", where, prev, p.Name, fname)
		}
		if !exported(fname) {
			return nil, fmt.Errorf("%s: parameter %q does not map to an exported field name", where, p.Name)
		}
		seen[fname] = p.Name
		switch s.kind {
		case kString, kTime, kInt, kFloat, kBool, kEnum:
		default:
			return nil, fmt.Errorf("%s: parameter %q is not a scalar or string enum", where, p.Name)
		}
		ft, err := b.typeOf(s, name+fname, "["+name+"."+fname+"]")
		if err != nil {
			return nil, err
		}
		d.refs = append(d.refs, namedIn(ft)...)
		desc := p.Description
		if desc == "" {
			desc = s.desc
		}
		d.fields = append(d.fields, &field{name: fname, jsonName: p.Name, typ: ft, required: p.Required, doc: desc})
	}
	return d, nil
}
