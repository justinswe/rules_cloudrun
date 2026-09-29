package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/justinswe/std/errors"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// roots maps each resource type to its Cloud Run v2 API message.
var roots = map[string]protoreflect.MessageDescriptor{
	"service": (&runpb.Service{}).ProtoReflect().Descriptor(),
	"job":     (&runpb.Job{}).ProtoReflect().Descriptor(),
	"worker":  (&runpb.WorkerPool{}).ProtoReflect().Descriptor(),
}

var schemaRevision = inventoryRevision()

// SchemaRevision identifies the runpb descriptor snapshot used by offline validation.
func SchemaRevision() string { return schemaRevision }

// DecodeJSON decodes JSON without losing integer precision.
func DecodeJSON(data []byte, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

// Validate checks a complete writable Cloud Run v2 resource body.
func Validate(kind string, body map[string]any) error {
	root, ok := roots[kind]
	if !ok {
		return errors.Errorf("unknown resource type %q", kind)
	}
	if err := validateMessage(body, root, kind, true); err != nil {
		return err
	}
	return validateResource(kind, body)
}

// validateMessage checks an object against a message; required also enforces REQUIRED fields and rejects nulls.
func validateMessage(value any, message protoreflect.MessageDescriptor, path string, required bool) error {
	object, ok := value.(map[string]any)
	if !ok {
		return ruleErr("schema.type", "%s must be an object", path)
	}
	oneofs := map[protoreflect.FullName]bool{}
	for _, key := range slices.Sorted(maps.Keys(object)) {
		field := message.Fields().ByJSONName(key)
		if field == nil {
			return ruleErr("schema.unknown-field", "%s.%s: unknown field%s", path, key, jsonNameHint(message, key))
		}
		if hasBehavior(field, annotations.FieldBehavior_OUTPUT_ONLY) {
			return ruleErr("schema.output-only", "%s.%s is output-only", path, key)
		}
		if !required && object[key] == nil {
			continue
		}
		if err := claimOneof(field, oneofs, path); err != nil {
			return err
		}
		if err := validateField(object[key], field, path+"."+key, required); err != nil {
			return err
		}
	}
	if !required {
		return nil
	}
	return checkRequired(object, message, path)
}

// jsonNameHint suggests the JSON name when a key uses the proto field name.
func jsonNameHint(message protoreflect.MessageDescriptor, key string) string {
	if field := message.Fields().ByName(protoreflect.Name(key)); field != nil {
		return fmt.Sprintf("; did you mean %q?", field.JSONName())
	}
	return ""
}

// claimOneof rejects a second member of the same API union.
func claimOneof(field protoreflect.FieldDescriptor, claimed map[protoreflect.FullName]bool, path string) error {
	oneof := field.ContainingOneof()
	if oneof == nil || oneof.IsSynthetic() {
		return nil
	}
	if claimed[oneof.FullName()] {
		return ruleErr("schema.oneof", "%s: exactly one of %s may be set; to replace a member set in the base config, set it to null in the overlay", path, strings.Join(oneofNames(oneof), ", "))
	}
	claimed[oneof.FullName()] = true
	return nil
}

// oneofNames lists the JSON names of a union's members.
func oneofNames(oneof protoreflect.OneofDescriptor) []string {
	names := []string{}
	for i := 0; i < oneof.Fields().Len(); i++ {
		names = append(names, oneof.Fields().Get(i).JSONName())
	}
	return names
}

// checkRequired reports the first missing REQUIRED field.
func checkRequired(object map[string]any, message protoreflect.MessageDescriptor, path string) error {
	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if !hasBehavior(field, annotations.FieldBehavior_REQUIRED) {
			continue
		}
		if message.FullName() == "google.cloud.run.v2.Container" && field.JSONName() == "image" && object["sourceCode"] != nil {
			continue
		}
		if _, present := object[field.JSONName()]; !present {
			return ruleErr("schema.required", "%s.%s is required", path, field.JSONName())
		}
	}
	return nil
}

// validateField checks one field value, including map and list containers.
func validateField(value any, field protoreflect.FieldDescriptor, path string, required bool) error {
	switch {
	case field.IsMap():
		object, ok := value.(map[string]any)
		if !ok {
			return ruleErr("schema.type", "%s must be an object", path)
		}
		for _, key := range slices.Sorted(maps.Keys(object)) {
			if !required && object[key] == nil {
				continue
			}
			if err := validateSingular(object[key], field.MapValue(), path+"."+key, required); err != nil {
				return err
			}
		}
		return nil
	case field.IsList():
		values, ok := value.([]any)
		if !ok {
			return ruleErr("schema.type", "%s must be an array", path)
		}
		for i, item := range values {
			if err := validateSingular(item, field, formatIndex(path, i), required); err != nil {
				return err
			}
		}
		return nil
	}
	return validateSingular(value, field, path, required)
}

// validateSingular checks a scalar or message value against its field kind.
func validateSingular(value any, field protoreflect.FieldDescriptor, path string, required bool) error {
	if value == nil {
		return ruleErr("schema.null", "%s is null; null only deletes an inherited field of an overlay map", path)
	}
	switch field.Kind() {
	case protoreflect.MessageKind:
		return validateMessageValue(value, field.Message(), path, required)
	case protoreflect.EnumKind:
		return validateEnum(value, field.Enum(), path)
	case protoreflect.StringKind:
		return requireString(value, path)
	case protoreflect.BoolKind:
		if _, ok := value.(bool); !ok {
			return ruleErr("schema.type", "%s must be a boolean", path)
		}
		return nil
	case protoreflect.Int32Kind:
		return validateInt32(value, path)
	case protoreflect.Int64Kind:
		return validateInt64(value, path)
	}
	return ruleErr("schema.kind", "%s: unsupported field kind %s", path, field.Kind())
}

// validateMessageValue checks nested messages, parsing well-known types from their proto-JSON strings.
func validateMessageValue(value any, message protoreflect.MessageDescriptor, path string, required bool) error {
	if message.ParentFile().Package() != "google.protobuf" {
		return validateMessage(value, message, path, required)
	}
	if err := requireString(value, path); err != nil {
		return err
	}
	encoded, _ := json.Marshal(value) // A string always encodes.
	if err := protojson.Unmarshal(encoded, dynamicpb.NewMessage(message)); err != nil {
		return ruleErr("schema.format", "%s must be a valid %s (durations are seconds, for example 300s)", path, message.Name())
	}
	return nil
}

// requireString rejects non-string values, which YAML produces for unquoted numbers and booleans.
func requireString(value any, path string) error {
	if _, ok := value.(string); !ok {
		return ruleErr("schema.type", "%s must be a string (quote YAML numbers and booleans)", path)
	}
	return nil
}

// validateEnum requires the symbolic name of an enum value.
func validateEnum(value any, enum protoreflect.EnumDescriptor, path string) error {
	if err := requireString(value, path); err != nil {
		return err
	}
	name := value.(string)
	if enum.Values().ByName(protoreflect.Name(name)) == nil {
		return ruleErr("schema.enum", "%s: invalid enum %q", path, name)
	}
	if strings.HasSuffix(name, "_UNSPECIFIED") {
		return ruleErr("schema.enum-unspecified", "%s: omit the field instead of setting %s", path, name)
	}
	return nil
}

// validateInt32 requires a JSON integer in the int32 range.
func validateInt32(value any, path string) error {
	n, ok := number(value)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
		return ruleErr("schema.type", "%s must be integer", path)
	}
	if n < math.MinInt32 || n > math.MaxInt32 {
		return ruleErr("schema.int32", "%s exceeds int32 range", path)
	}
	return nil
}

// validateInt64 requires a decimal string, the proto-JSON encoding of int64.
func validateInt64(value any, path string) error {
	if err := requireString(value, path); err != nil {
		return err
	}
	if _, err := strconv.ParseInt(value.(string), 10, 64); err != nil {
		return ruleErr("schema.int64", "%s must be a decimal integer string in the int64 range", path)
	}
	return nil
}

// hasBehavior reports whether a field carries a google.api.field_behavior annotation.
func hasBehavior(field protoreflect.FieldDescriptor, wanted annotations.FieldBehavior) bool {
	options, ok := field.Options().(*descriptorpb.FieldOptions)
	if !ok || !proto.HasExtension(options, annotations.E_FieldBehavior) {
		return false
	}
	for _, behavior := range proto.GetExtension(options, annotations.E_FieldBehavior).([]annotations.FieldBehavior) {
		if behavior == wanted {
			return true
		}
	}
	return false
}

// fieldInventory lists every field reachable from the resource roots with its write classification.
func fieldInventory() []string {
	lines := []string{}
	seen := map[protoreflect.FullName]bool{}
	var visit func(protoreflect.MessageDescriptor)
	visit = func(message protoreflect.MessageDescriptor) {
		if seen[message.FullName()] || message.ParentFile().Package() == "google.protobuf" {
			return
		}
		seen[message.FullName()] = true
		for i := 0; i < message.Fields().Len(); i++ {
			field := message.Fields().Get(i)
			lines = append(lines, fmt.Sprintf("%s.%s %s %s%s%s", message.FullName(), field.JSONName(), classify(field), shape(field), oneofName(field), enumValues(field)))
			if field.Message() != nil && !field.IsMap() && classify(field) != "output-only" {
				visit(field.Message())
			}
		}
	}
	for _, kind := range slices.Sorted(maps.Keys(roots)) {
		visit(roots[kind])
	}
	slices.Sort(lines)
	return lines
}

// classify names how a field participates in writable request bodies.
func classify(field protoreflect.FieldDescriptor) string {
	switch {
	case hasBehavior(field, annotations.FieldBehavior_OUTPUT_ONLY):
		return "output-only"
	case hasBehavior(field, annotations.FieldBehavior_REQUIRED):
		return "required"
	}
	return "writable"
}

// shape names a field's cardinality and kind, such as "repeated message" or "map".
func shape(field protoreflect.FieldDescriptor) string {
	if field.IsMap() {
		return "map"
	}
	return field.Cardinality().String() + " " + field.Kind().String()
}

// oneofName names the union a field belongs to, if any.
func oneofName(field protoreflect.FieldDescriptor) string {
	if oneof := field.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
		return " oneof=" + string(oneof.Name())
	}
	return ""
}

// enumValues lists an enum field's accepted names so descriptor bumps surface new values.
func enumValues(field protoreflect.FieldDescriptor) string {
	if field.Enum() == nil {
		return ""
	}
	names := []string{}
	for i := 0; i < field.Enum().Values().Len(); i++ {
		names = append(names, string(field.Enum().Values().Get(i).Name()))
	}
	return " enum=" + strings.Join(names, ",")
}

// inventoryRevision fingerprints the field inventory so deployments can record the schema they were validated against.
func inventoryRevision() string {
	sum := sha256.Sum256([]byte(strings.Join(fieldInventory(), "\n")))
	return "runpb-" + hex.EncodeToString(sum[:6])
}

// number converts a decoded JSON or YAML number to float64.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// formatIndex appends a list index to a field path.
func formatIndex(path string, index int) string { return fmt.Sprintf("%s[%d]", path, index) }
