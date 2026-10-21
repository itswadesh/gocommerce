package aigateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// The JSON Schema a task's answer is held to, in the part of the standard
// both providers enforce and this file checks again: types, closed objects,
// required properties, arrays, enums and the length and range bounds. Rule 1
// rules out a schema library, and a full validator would enforce keywords no
// provider honours; a keyword outside this set is refused when the task is
// registered rather than silently not checked.

type schemaNode struct {
	types      []string // "object", "array", "string", "integer", "number", "boolean", "null"
	properties map[string]*schemaNode
	required   []string
	items      *schemaNode
	enum       []any
	minLength  *int
	maxLength  *int
	minItems   *int
	maxItems   *int
	minimum    *float64
	maximum    *float64
}

// Annotations are allowed and ignored: they describe, they do not constrain.
var annotations = map[string]bool{"description": true, "title": true, "$schema": true, "examples": true}

func parseSchema(raw json.RawMessage) (*schemaNode, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("missing")
	}
	var v map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	return parseNode(v, "$")
}

func parseNode(v map[string]any, at string) (*schemaNode, error) {
	n := &schemaNode{}
	for key := range v {
		switch key {
		case "type", "properties", "required", "additionalProperties", "items", "enum",
			"minLength", "maxLength", "minItems", "maxItems", "minimum", "maximum":
		default:
			if !annotations[key] {
				return nil, fmt.Errorf("%s: %q is not enforced by this gateway; leave it out or check it in the task's Check", at, key)
			}
		}
	}
	switch t := v["type"].(type) {
	case string:
		n.types = []string{t}
	case []any:
		for _, x := range t {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("%s: type must name types", at)
			}
			n.types = append(n.types, s)
		}
	default:
		return nil, fmt.Errorf("%s: every node needs a type", at)
	}
	for _, t := range n.types {
		switch t {
		case "object", "array", "string", "integer", "number", "boolean", "null":
		default:
			return nil, fmt.Errorf("%s: unknown type %q", at, t)
		}
	}
	if slices.Contains(n.types, "object") {
		props, _ := v["properties"].(map[string]any)
		if ap, ok := v["additionalProperties"].(bool); !ok || ap {
			return nil, fmt.Errorf("%s: an object must set additionalProperties to false, so an answer with fields nobody asked for is refused", at)
		}
		n.properties = map[string]*schemaNode{}
		for name, raw := range props {
			child, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s.%s: not a schema", at, name)
			}
			node, err := parseNode(child, at+"."+name)
			if err != nil {
				return nil, err
			}
			n.properties[name] = node
		}
		req, _ := v["required"].([]any)
		for _, r := range req {
			s, ok := r.(string)
			if !ok || n.properties[s] == nil {
				return nil, fmt.Errorf("%s: required names %v, which is not a property", at, r)
			}
			n.required = append(n.required, s)
		}
		if len(n.required) != len(n.properties) {
			return nil, fmt.Errorf("%s: every property must be required (give an optional one \"null\" beside its type)", at)
		}
	}
	if slices.Contains(n.types, "array") {
		items, ok := v["items"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: an array needs items", at)
		}
		node, err := parseNode(items, at+"[]")
		if err != nil {
			return nil, err
		}
		n.items = node
	}
	if e, ok := v["enum"].([]any); ok {
		n.enum = e
	}
	ints := map[string]**int{"minLength": &n.minLength, "maxLength": &n.maxLength, "minItems": &n.minItems, "maxItems": &n.maxItems}
	for key, dst := range ints {
		if raw, ok := v[key]; ok {
			num, ok := raw.(json.Number)
			i, err := num.Int64()
			if !ok || err != nil || i < 0 {
				return nil, fmt.Errorf("%s: %s must be a whole number", at, key)
			}
			x := int(i)
			*dst = &x
		}
	}
	floats := map[string]**float64{"minimum": &n.minimum, "maximum": &n.maximum}
	for key, dst := range floats {
		if raw, ok := v[key]; ok {
			num, ok := raw.(json.Number)
			f, err := num.Float64()
			if !ok || err != nil {
				return nil, fmt.Errorf("%s: %s must be a number", at, key)
			}
			*dst = &f
		}
	}
	return n, nil
}

// validate checks an answer against the schema and says the first thing
// wrong with it, in words that go on the record.
func (n *schemaNode) validate(raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("the answer is not JSON: %v", err)
	}
	if dec.More() {
		return errors.New("the answer is more than one JSON value")
	}
	return n.check(v, "the answer")
}

func (n *schemaNode) check(v any, at string) error {
	kind := jsonKind(v)
	if !slices.Contains(n.types, kind) && !(kind == "integer" && slices.Contains(n.types, "number")) {
		return fmt.Errorf("%s is %s, want %s", at, kind, strings.Join(n.types, " or "))
	}
	if len(n.enum) > 0 && !inEnum(v, n.enum) {
		return fmt.Errorf("%s is not one of the allowed values", at)
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if n.properties[k] == nil {
				return fmt.Errorf("%s has a field nobody asked for: %q", at, k)
			}
		}
		for _, k := range n.required {
			val, ok := x[k]
			if !ok {
				return fmt.Errorf("%s is missing %q", at, k)
			}
			if err := n.properties[k].check(val, at+"."+k); err != nil {
				return err
			}
		}
	case []any:
		if n.minItems != nil && len(x) < *n.minItems {
			return fmt.Errorf("%s has %d items, want at least %d", at, len(x), *n.minItems)
		}
		if n.maxItems != nil && len(x) > *n.maxItems {
			return fmt.Errorf("%s has %d items, want at most %d", at, len(x), *n.maxItems)
		}
		for i, item := range x {
			if err := n.items.check(item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	case string:
		length := utf8.RuneCountInString(x)
		if n.minLength != nil && length < *n.minLength {
			return fmt.Errorf("%s is %d characters, want at least %d", at, length, *n.minLength)
		}
		if n.maxLength != nil && length > *n.maxLength {
			return fmt.Errorf("%s is %d characters, want at most %d", at, length, *n.maxLength)
		}
	case json.Number:
		f, _ := x.Float64()
		if n.minimum != nil && f < *n.minimum {
			return fmt.Errorf("%s is %s, below %v", at, x, *n.minimum)
		}
		if n.maximum != nil && f > *n.maximum {
			return fmt.Errorf("%s is %s, above %v", at, x, *n.maximum)
		}
	}
	return nil
}

func jsonKind(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return "integer"
		}
		return "number"
	}
	return "unknown"
}

func inEnum(v any, enum []any) bool {
	got, _ := json.Marshal(v)
	for _, e := range enum {
		want, _ := json.Marshal(e)
		if bytes.Equal(got, want) {
			return true
		}
	}
	return false
}
