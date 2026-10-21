package aigateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// The JSON Schema a task's answer is held to, in the part of the standard
// both providers enforce and this file checks again: types, closed objects,
// required properties, arrays and enums. That is OpenAI's strict mode's own
// set, less what it added late: a schema is sent to whichever provider the
// store picks, so a length or range bound would make a task fail every
// OpenAI call. Bounds belong in the task's Check. Rule 1 rules out a schema
// library; a keyword outside this set is refused when the task is
// registered rather than silently not checked.

type schemaNode struct {
	types      []string // "object", "array", "string", "integer", "number", "boolean", "null"
	properties map[string]*schemaNode
	required   []string
	items      *schemaNode
	enum       []any
}

// Annotations are allowed and ignored: they describe, they do not constrain.
var annotations = map[string]bool{"description": true, "title": true}

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
		case "type", "properties", "required", "additionalProperties", "items", "enum":
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
		for i, item := range x {
			if err := n.items.check(item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
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
