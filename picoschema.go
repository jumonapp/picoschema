// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package picoschema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/invopop/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

// ToJSONSchema turns picoschema input into a JSONSchema.
// The val parameter is the result of parsing YAML into an value of type any.
// picoschema is loosely documented at docs/dotprompt.md.
func ToJSONSchema(val any) (*jsonschema.Schema, error) {
	if val == nil {
		return nil, nil
	}

	if m, ok := val.(map[string]any); ok {
		// If we decoded to something that looks like it might
		// be a JSON schema, treat it as a JSON schema.
		switch m["type"] {
		case "string", "boolean", "null", "number", "integer", "object", "array":
			return mapToJSONSchema(m)
		}

		if p, ok := m["properties"]; ok {
			if _, ok := p.(map[string]any); ok {
				s, err := mapToJSONSchema(m)
				if err != nil {
					return nil, err
				}
				s.Type = "object"
				return s, nil
			}
		}
	}

	return parsePico(val)
}

// parsePico parses picoschema from the result of the YAML parser.
func parsePico(val any) (*jsonschema.Schema, error) {
	switch val := val.(type) {
	default:
		return nil, fmt.Errorf("picoschema: value %v of type %[1]T is not an object, slice or string", val)

	case string:
		typ, desc, found := strings.Cut(val, ",")
		switch typ {
		case "string", "boolean", "null", "number", "integer", "any":
		default:
			return nil, fmt.Errorf("picoschema: unsupported scalar type %q", typ)
		}
		if typ == "any" {
			typ = ""
		}
		ret := &jsonschema.Schema{
			Type: typ,
		}
		if found {
			ret.Description = strings.TrimSpace(desc)
		}
		return ret, nil

	case []any: // assume enum
		return &jsonschema.Schema{Enum: val}, nil

	case map[string]any:
		ret := &jsonschema.Schema{
			Type:                 "object",
			Properties:           orderedmap.New[string, *jsonschema.Schema](),
			AdditionalProperties: jsonschema.FalseSchema,
		}
		for k, v := range val {
			name, typ, found := strings.Cut(k, "(")
			propertyName, isOptional := strings.CutSuffix(name, "?")
			if name != "" && !isOptional {
				ret.Required = append(ret.Required, propertyName)
			}

			property, err := parsePico(v)
			if err != nil {
				return nil, err
			}

			if !found {
				ret.Properties.Set(propertyName, property)
				continue
			}

			typ = strings.TrimSuffix(typ, ")")
			typ, desc, found := strings.Cut(strings.TrimSuffix(typ, ")"), ",")
			switch typ {
			case "array":
				property = &jsonschema.Schema{
					Type:  "array",
					Items: property,
				}
			case "object":
				// Use property unchanged.
			case "enum":
				if property.Enum == nil {
					return nil, fmt.Errorf("picoschema: enum value %v is not an array", property)
				}
				if isOptional {
					property.Enum = append(property.Enum, nil)
				}

			case "*":
				ret.AdditionalProperties = property
				continue
			default:
				return nil, fmt.Errorf("picoschema: parenthetical type %q is none of %q", typ,
					[]string{"object", "array", "enum", "*"})

			}

			if found {
				property.Description = strings.TrimSpace(desc)
			}

			ret.Properties.Set(propertyName, property)
		}
		return ret, nil
	}
}

// mapToJSONSchema converts a YAML value to a JSONSchema.
func mapToJSONSchema(m map[string]any) (*jsonschema.Schema, error) {
	var ret jsonschema.Schema

	rval := reflect.ValueOf(&ret)
	rtype := rval.Type().Elem()
	numField := rtype.NumField()
	jsonMap := make(map[string]reflect.Value)
	for i := 0; i < numField; i++ {
		sf := rtype.Field(i)
		spec := sf.Tag.Get("json")
		if spec != "" {
			jsonName, _, _ := strings.Cut(spec, ",")
			jsonMap[jsonName] = rval.Elem().Field(i)
		}
	}

	for k, v := range m {
		rf, ok := jsonMap[k]
		if !ok {
			return nil, fmt.Errorf("picoschema: unrecognized JSON schema field name %q", k)
		}

		switch rf.Type() {
		case reflect.TypeFor[any]():
			rf.Set(reflect.ValueOf(v))

		case reflect.TypeFor[string]():
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("picoschema: found type %T for field %q, want %T", v, k, "")
			}
			rf.SetString(str)

		case reflect.TypeFor[*uint64]():
			rf.Set(reflect.New(reflect.TypeFor[uint64]()))
			switch v.(type) {
			case uint, uint8, uint16, uint32, uint64, uintptr:
				rf.Elem().SetUint(reflect.ValueOf(v).Uint())
			case int, int8, int16, int32, int64:
				rf.Elem().SetUint(uint64(reflect.ValueOf(v).Int()))
			default:
				return nil, fmt.Errorf("picoschema: found type %T for field %q, want an integer type", v, k)
			}

		case reflect.TypeFor[bool]():
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("picoschema: found type %T for field %q, want %T", v, k, true)
			}
			rf.SetBool(b)

		case reflect.TypeFor[[]string]():
			astrs, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("picoschema: found type %T for field %q, want %T", v, k, []any{})
			}
			sstrs := make([]string, 0, len(astrs))
			for i, astr := range astrs {
				s, ok := astr.(string)
				if !ok {
					return nil, fmt.Errorf("picoschema: found type %T for field element %d of %q, want %T", astr, i, k, "")
				}
				sstrs = append(sstrs, s)
			}
			rf.Set(reflect.ValueOf(sstrs))

		case reflect.TypeFor[json.Number]():
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("picoschema: found type %T for field %q, want %T", v, k, "")
			}
			rf.SetString(s)

		case reflect.TypeFor[*jsonschema.Schema]():
			m, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("picoschema: found type %T for field %q, want %T", v, k, make(map[string]any))
			}
			schema, err := mapToJSONSchema(m)
			if err != nil {
				return nil, fmt.Errorf("picoschema: failed to convert field %q: %w", k, err)
			}
			rf.Set(reflect.ValueOf(schema))

		case reflect.TypeFor[[]*jsonschema.Schema]():
			s, ok := v.([]map[string]any)
			if !ok {
				return nil, fmt.Errorf("picoschema: found type %T for field %q, want %T", v, k, []map[string]any{})
			}
			schemas := make([]*jsonschema.Schema, 0, len(s))
			for _, m := range s {
				schema, err := mapToJSONSchema(m)
				if err != nil {
					return nil, fmt.Errorf("picoschema: error in field %q: %w", k, err)
				}
				schemas = append(schemas, schema)
			}
			rf.Set(reflect.ValueOf(schemas))

		case reflect.TypeFor[*orderedmap.OrderedMap[string, *jsonschema.Schema]]():
			m, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("picoschema: found type %T for field %q, want %T", v, k, make(map[string]any))
			}
			om := orderedmap.New[string, *jsonschema.Schema]()
			for mk, mv := range m {
				mvm, ok := mv.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("picoschema: found type %T for field %q key %q, want %T", mv, k, mk, make(map[string]any))
				}
				schema, err := mapToJSONSchema(mvm)
				if err != nil {
					return nil, fmt.Errorf("picoschema: error in field %q key %q: %w", k, mk, err)
				}
				om.Set(mk, schema)
			}
			rf.Set(reflect.ValueOf(om))

		default:
			return nil, fmt.Errorf("picoschema: unsupported JSONSchema field type %s for field %q", rf.Type(), k)
		}
	}

	return &ret, nil
}

// ConvertSchema marshals s to JSON, then unmarshals the result.
func ConvertSchema(s *jsonschema.Schema) (any, error) {
	// JSON sorts maps but not slices.
	// jsonschema slices are not sorted consistently.
	sortSchemaSlices(s)
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var a any
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return a, nil
}

// sortSchemaSlices sorts the slices in a jsonschema to permit
// consistent comparisons. We only bother with the fields we need
// for the tests we have.
func sortSchemaSlices(s *jsonschema.Schema) {
	slices.Sort(s.Required)
	if s.Properties != nil {
		for p := s.Properties.Oldest(); p != nil; p = p.Next() {
			sortSchemaSlices(p.Value)
		}
	}
	if s.Items != nil {
		sortSchemaSlices(s.Items)
	}
}
