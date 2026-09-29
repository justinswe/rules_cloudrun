package manifest

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func validBody(kind string) map[string]any {
	template := map[string]any{"containers": []any{map[string]any{"name": "app", "image": "example.com/app:latest"}}}
	if kind == "job" {
		template = map[string]any{"template": template}
	}
	return map[string]any{"template": template}
}

func firstContainer(body map[string]any, kind string) map[string]any {
	return arrayAt(TaskTemplate(body, kind), "containers")[0].(map[string]any)
}

func TestRenderPreservesValuesAndSecrets(t *testing.T) {
	for _, kind := range []string{"service", "job", "worker"} {
		t.Run(kind, func(t *testing.T) {
			body := validBody(kind)
			c := firstContainer(body, kind)
			c["env"] = []any{
				map[string]any{"name": "EMPTY", "value": ""},
				map[string]any{"name": "FLAG", "value": "false"},
				map[string]any{"name": "COUNT", "value": "0"},
				map[string]any{"name": "SECRET", "valueSource": map[string]any{"secretKeyRef": map[string]any{"secret": "projects/other-project/secrets/password", "version": "production"}}},
			}
			c["resources"] = map[string]any{"cpuIdle": false, "startupCpuBoost": false}
			input, err := json.Marshal(body)
			require.NoError(t, err)
			output, err := Render(nil, input, RenderOptions{ServiceName: "app", ResourceType: kind})
			require.NoError(t, err)
			require.JSONEq(t, string(input), string(output))
		})
	}
}

func TestMergeSemantics(t *testing.T) {
	base := []byte("labels: {team: infra, remove: yes}\ntraffic: [{type: TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST, percent: 100}]\ntemplate:\n  timeout: 300s\n  containers: [{name: app, image: 'example.com/new:latest', env: [{name: EMPTY, value: ''}]}]\n")
	overlay := []byte("labels: {remove: null, env: dev}\ntraffic: null\n")
	out, err := Render(base, overlay, RenderOptions{ResourceType: "service", ServiceName: "app"})
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, DecodeJSON(out, &result))
	require.Equal(t, map[string]any{"team": "infra", "env": "dev"}, result["labels"])
	require.Empty(t, result["traffic"])
	require.Equal(t, "300s", objectAt(result, "template")["timeout"])
	require.Equal(t, []any{map[string]any{"name": "EMPTY", "value": ""}}, firstContainer(result, "service")["env"])
	again, err := Render(out, []byte("{}"), RenderOptions{ResourceType: "service", ServiceName: "app"})
	require.NoError(t, err)
	require.Equal(t, out, again)
}

func TestInputFailures(t *testing.T) {
	for _, input := range []string{"a: [", "a: 1\na: 2", "{}\n---\n{}", "- scalar", "1: value"} {
		t.Run(input, func(t *testing.T) { _, err := ParseYAML([]byte(input)); require.Error(t, err) })
	}
	for _, input := range []string{"", "# empty", "{}", "null"} {
		_, err := ParseYAML([]byte(input))
		require.NoError(t, err)
	}
	body := validBody("service")
	firstContainer(body, "service")["env"] = []any{map[string]any{"name": "FLAG", "value": false}}
	require.ErrorContains(t, Validate("service", body), "must be a string")
	require.Error(t, Validate("unknown", body))
	_, err := Render(nil, []byte("{}"), RenderOptions{ServiceName: "app", ResourceType: "unknown"})
	require.Error(t, err)
	_, err = Render(nil, []byte("{}"), RenderOptions{ServiceName: "invalid/name", ResourceType: "service"})
	require.Error(t, err)
}

func TestImages(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	image, err := PinnedImage("example.com/app", digest+"\n")
	require.NoError(t, err)
	require.Equal(t, "example.com/app@"+digest, image)
	for _, repo := range []string{"example.com/app:tag", "example.com/app/", "example.com/app@" + digest, "$(touch bad)"} {
		_, err := PinnedImage(repo, digest)
		require.Error(t, err)
	}
	for _, digest := range []string{"", "sha256:x", "sha512:" + strings.Repeat("a", 64)} {
		_, err := PinnedImage("example.com/app", digest)
		require.Error(t, err)
	}
	for _, image := range []string{"", "example.com/app@sha256:bad", "example.com/$(id)", "example.com/app\nextra"} {
		require.Error(t, ValidateImage(image))
	}
	body := validBody("service")
	tmpl := objectAt(body, "template")
	tmpl["containers"] = append(arrayAt(tmpl, "containers"), map[string]any{"name": "sidecar", "image": "example.com/sidecar:old"})
	require.Error(t, SetImage(body, "service", "", "example.com/app:new"))
	require.Error(t, SetImage(body, "service", "missing", "example.com/app:new"))
	require.NoError(t, SetImage(body, "service", "app", "example.com/app:new"))
	require.Equal(t, "example.com/sidecar:old", arrayAt(tmpl, "containers")[1].(map[string]any)["image"])
	require.Error(t, SetImage(map[string]any{}, "service", "", "image"))
	require.Error(t, SetImage(map[string]any{"template": map[string]any{}}, "job", "", "image"))
	tmpl["containers"] = []any{"wrong"}
	require.Error(t, SetImage(body, "service", "", "image"))
	for _, name := range []string{"", "a/evil", "Bad", "0app", "app-", strings.Repeat("a", 50)} {
		require.Error(t, ValidateName(name))
	}
	require.NoError(t, ValidateName("a"))
	require.NoError(t, ValidateName("valid-app-1"))
}

func TestRenderFile(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	base := filepath.Join(dir, "base.yaml")
	digest := filepath.Join(dir, "digest")
	require.NoError(t, os.WriteFile(config, []byte("template:\n  containers: [{name: app}]\n"), 0o600))
	require.NoError(t, os.WriteFile(base, []byte("description: test"), 0o600))
	require.NoError(t, os.WriteFile(digest, []byte("sha256:"+strings.Repeat("a", 64)), 0o600))
	options := RenderOptions{ConfigPath: config, BaseConfigPath: base, OutputPath: filepath.Join(dir, "output", "body.json"), ServiceName: "app", ResourceType: "service", ImageDigestPath: digest, ImageRepo: "example.com/app"}
	require.NoError(t, RenderFile(options))
	body, err := os.ReadFile(options.OutputPath)
	require.NoError(t, err)
	require.Contains(t, string(body), "example.com/app@sha256:")
	tests := []func(*RenderOptions){
		func(o *RenderOptions) { o.ConfigPath = "" }, func(o *RenderOptions) { o.OutputPath = "" },
		func(o *RenderOptions) { o.ConfigPath = dir }, func(o *RenderOptions) { o.BaseConfigPath = dir },
		func(o *RenderOptions) { o.Image = "image" }, func(o *RenderOptions) { o.ImageRepo = "" },
		func(o *RenderOptions) { o.ImageDigestPath = "" }, func(o *RenderOptions) { o.ImageDigestPath = dir },
		func(o *RenderOptions) { o.ImageRepo = "bad:tag" },
		func(o *RenderOptions) { o.OutputPath = filepath.Join(config, "child") }, func(o *RenderOptions) { o.OutputPath = dir },
	}
	for _, change := range tests {
		bad := options
		change(&bad)
		require.Error(t, RenderFile(bad))
	}
}

func TestSchemaFieldInventory(t *testing.T) {
	actual := strings.Join(fieldInventory(), "\n") + "\n"
	want, err := os.ReadFile("testdata/field_inventory.txt")
	require.NoError(t, err)
	if string(want) != actual {
		t.Logf("actual inventory:\n%s", actual)
	}
	require.Equal(t, string(want), actual, "runpb changed: copy the logged inventory into testdata/field_inventory.txt and add rules for new fields")
	require.True(t, strings.HasPrefix(SchemaRevision(), "runpb-"))
	require.Error(t, validateMessage(map[string]any{"typo": true}, roots["service"], "service", false))
	require.Error(t, validateInt32(json.Number("1.5"), "n"))
	require.Error(t, validateInt32(json.Number("2147483648"), "n"))
	require.Error(t, validateInt32("1", "n"))
	require.Error(t, validateInt64(json.Number("1"), "n"))
	require.Error(t, validateInt64("bad", "n"))
}

// TestEveryFieldAcceptsItsProtoJSONShape exercises every reachable field with a sample value.
func TestEveryFieldAcceptsItsProtoJSONShape(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var visit func(protoreflect.MessageDescriptor)
	visit = func(message protoreflect.MessageDescriptor) {
		if seen[message.FullName()] || message.ParentFile().Package() == "google.protobuf" {
			return
		}
		seen[message.FullName()] = true
		for i := 0; i < message.Fields().Len(); i++ {
			field := message.Fields().Get(i)
			id := string(message.FullName()) + "." + field.JSONName()
			err := validateMessage(map[string]any{field.JSONName(): sampleValue(field)}, message, id, false)
			if classify(field) == "output-only" {
				require.ErrorContains(t, err, "output-only", id)
				continue
			}
			require.NoError(t, err, id)
			require.Error(t, validateField(nil, field, id, true), id)
			if field.Enum() != nil {
				require.Error(t, validateEnum("NOT_AN_ENUM", field.Enum(), id), id)
			}
			if field.Message() != nil && !field.IsMap() {
				visit(field.Message())
			}
		}
	}
	for _, kind := range slices.Sorted(maps.Keys(roots)) {
		visit(roots[kind])
	}
}

// sampleValue returns a proto-JSON value of the field's shape.
func sampleValue(field protoreflect.FieldDescriptor) any {
	switch {
	case field.IsMap():
		return map[string]any{"key": sampleSingular(field.MapValue())}
	case field.IsList():
		return []any{sampleSingular(field)}
	}
	return sampleSingular(field)
}

func sampleSingular(field protoreflect.FieldDescriptor) any {
	switch field.Kind() {
	case protoreflect.MessageKind:
		if field.Message().ParentFile().Package() == "google.protobuf" {
			return "1s"
		}
		return map[string]any{}
	case protoreflect.EnumKind:
		values := field.Enum().Values()
		for i := 0; i < values.Len(); i++ {
			if name := string(values.Get(i).Name()); !strings.HasSuffix(name, "_UNSPECIFIED") {
				return name
			}
		}
	case protoreflect.BoolKind:
		return false
	case protoreflect.Int32Kind:
		return json.Number("1")
	case protoreflect.Int64Kind:
		return "1"
	}
	return "example"
}

func TestRenderRejectsInvalidInputBeforeWriting(t *testing.T) {
	options := RenderOptions{ServiceName: "app", ResourceType: "service"}
	for _, pair := range [][2]string{{"a: [", "{}"}, {"{}", "a: ["}, {"{}", "uid: forbidden"}, {"{}", "template: {containers: [{image: 'example.com/app:latest'}]}\nname: projects/p/locations/us-west1/services/other"}} {
		_, err := Render([]byte(pair[0]), []byte(pair[1]), options)
		require.Error(t, err)
	}
	_, err := ParseYAML([]byte("value: .nan"))
	require.Error(t, err)
	options.Image = "image"
	_, err = Render(nil, []byte("{}"), options)
	require.Error(t, err)
	require.Error(t, SetImage(validBody("service"), "service", "", "bad image"))
	dir := t.TempDir()
	input := filepath.Join(dir, "bad.yaml")
	output := filepath.Join(dir, "out.json")
	require.NoError(t, os.WriteFile(input, []byte("uid: forbidden"), 0o600))
	options.ConfigPath = input
	options.OutputPath = output
	options.Image = ""
	require.Error(t, RenderFile(options))
	_, err = os.Stat(output)
	require.True(t, os.IsNotExist(err))
	for _, value := range []any{int32(1), int64(1), float64(1)} {
		n, ok := number(value)
		require.True(t, ok)
		require.Equal(t, float64(1), n)
	}
}

func TestJSONRejectsTrailingValues(t *testing.T) {
	for _, data := range []string{"{} {}", "{} garbage"} {
		var dest map[string]any
		require.Error(t, DecodeJSON([]byte(data), &dest))
	}
}
