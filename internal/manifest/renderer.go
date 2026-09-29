// Package manifest renders and validates native Cloud Run v2 request bodies.
package manifest

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	imagename "github.com/google/go-containerregistry/pkg/name"
	"github.com/justinswe/std/errors"
	"gopkg.in/yaml.v3"
)

// ImagePlaceholder marks the container image a deployment must supply with an image override.
const ImagePlaceholder = "rules-cloudrun.invalid/override-required:latest"

// RenderOptions identifies the inputs and output of a hermetic render action.
type RenderOptions struct {
	ConfigPath      string
	BaseConfigPath  string
	ResourceType    string
	ServiceName     string
	Image           string
	ImageRepo       string
	ImageDigestPath string
	ImageConfigPath string
	ImageContainer  string
	OutputPath      string
}

// ParseYAML reads one document, retaining explicit false, zero, and empty values.
func ParseYAML(data []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil && err != io.EOF {
		return nil, errors.Wrap(err, "parse YAML")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("configuration must contain exactly one YAML document")
	}
	if err := validateYAMLNodes(&document); err != nil {
		return nil, err
	}
	var object map[string]any
	if err := document.Decode(&object); err != nil {
		return nil, errors.Wrap(err, "decode YAML object")
	}
	if object == nil {
		object = map[string]any{}
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, errors.Wrap(err, "convert YAML to JSON")
	}
	var normalized map[string]any
	_ = DecodeJSON(encoded, &normalized) // The bytes were just encoded by encoding/json.
	return normalized, nil
}

// Merge recursively merges objects, replaces arrays, and removes null keys.
func Merge(base, overlay map[string]any) map[string]any {
	result := map[string]any{}
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if v == nil {
			delete(result, k)
			continue
		}
		child, ok := v.(map[string]any)
		if ok {
			existing, _ := result[k].(map[string]any)
			result[k] = Merge(existing, child)
			continue
		}
		result[k] = v
	}
	return result
}

// Render produces a validated, deterministic native v2 request body.
func Render(base, overlay []byte, options RenderOptions) ([]byte, error) {
	if err := ValidateName(options.ServiceName); err != nil {
		return nil, err
	}
	root, ok := roots[options.ResourceType]
	if !ok {
		return nil, errors.Errorf("unknown resource type %q", options.ResourceType)
	}
	b, err := ParseYAML(base)
	if err != nil {
		return nil, errors.Wrap(err, "base config")
	}
	o, err := ParseYAML(overlay)
	if err != nil {
		return nil, errors.Wrap(err, "overlay config")
	}
	if err := rejectNulls(b, "base"); err != nil {
		return nil, err
	}
	if err := validateMessage(b, root, "base", false); err != nil {
		return nil, err
	}
	if err := validateMessage(o, root, "overlay", false); err != nil {
		return nil, err
	}
	if err := checkArrayShadow(b, o, options.ResourceType); err != nil {
		return nil, err
	}
	if err := checkNullNoops(b, o, "overlay"); err != nil {
		return nil, err
	}
	body := Merge(b, o)
	if options.Image != "" {
		if err := SetImage(body, options.ResourceType, options.ImageContainer, options.Image); err != nil {
			return nil, err
		}
	}
	if err := Validate(options.ResourceType, body); err != nil {
		return nil, err
	}
	if err := rejectStrayPlaceholders(body, options.ResourceType, options.ImageContainer); err != nil {
		return nil, err
	}
	if name, ok := body["name"].(string); ok && !strings.HasSuffix(name, "/"+options.ServiceName) {
		return nil, errors.New("configured name conflicts with resource_name")
	}
	if err := ValidateResourceIdentity(options.ResourceType, body, options.ServiceName); err != nil {
		return nil, err
	}
	if err := checkSelectedImage(body, options); err != nil {
		return nil, err
	}
	result, _ := json.MarshalIndent(body, "", "  ") // Validated JSON values cannot fail to encode.
	return append(result, '\n'), nil
}

// checkSelectedImage resolves the image container, when one is named or needed, and checks it against the OCI config.
func checkSelectedImage(body map[string]any, options RenderOptions) error {
	if options.ImageContainer == "" && options.ImageConfigPath == "" {
		return nil
	}
	container, err := SelectContainer(body, options.ResourceType, options.ImageContainer)
	if err != nil || options.ImageConfigPath == "" {
		return err
	}
	config, err := os.ReadFile(options.ImageConfigPath)
	if err != nil {
		return errors.Wrap(err, "read OCI image config")
	}
	return ValidateImageConfig(container, config)
}

// RenderFile reads declared action inputs and writes a manifest only after validation.
func RenderFile(options RenderOptions) error {
	if options.ConfigPath == "" || options.OutputPath == "" {
		return errors.New("config and output paths are required")
	}
	overlay, err := os.ReadFile(options.ConfigPath)
	if err != nil {
		return errors.Wrap(err, "read config")
	}
	var base []byte
	if options.BaseConfigPath != "" {
		base, err = os.ReadFile(options.BaseConfigPath)
		if err != nil {
			return errors.Wrap(err, "read base config")
		}
	}
	if options.ImageDigestPath != "" || options.ImageRepo != "" {
		if options.Image != "" || options.ImageDigestPath == "" || options.ImageRepo == "" {
			return errors.New("image_repo and image_digest are required together and conflict with image")
		}
		digest, err := os.ReadFile(options.ImageDigestPath)
		if err != nil {
			return errors.Wrap(err, "read image digest")
		}
		options.Image, err = PinnedImage(options.ImageRepo, string(digest))
		if err != nil {
			return err
		}
	}
	result, err := Render(base, overlay, options)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.OutputPath), 0o755); err != nil {
		return errors.Wrap(err, "create output directory")
	}
	if err := os.WriteFile(options.OutputPath, result, 0o644); err != nil {
		return errors.Wrap(err, "write manifest")
	}
	return nil
}

var (
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	namePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$|^[a-z]$`)
)

// ValidateName checks the common Cloud Run resource ID contract.
func ValidateName(name string) error {
	if len(name) == 0 || len(name) > 49 || !namePattern.MatchString(name) {
		return ruleErr("resource.id", "invalid Cloud Run resource name %q (1-49 lowercase letters, digits, or hyphens; start with a letter)", name)
	}
	return nil
}

// PinnedImage constructs an immutable image reference from a repository and digest.
func PinnedImage(repo, digest string) (string, error) {
	digest = strings.TrimSpace(digest)
	if !digestPattern.MatchString(digest) {
		return "", errors.New("image digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	if err := ValidateImage(repo); err != nil {
		return "", err
	}
	if strings.Contains(repo, "@") || strings.Contains(repo[strings.LastIndex(repo, "/")+1:], ":") || strings.HasSuffix(repo, "/") {
		return "", errors.New("image_repo must be a repository without a tag, digest, or trailing slash")
	}
	return repo + "@" + digest, nil
}

// ValidateImage rejects malformed image references without invoking a shell.
func ValidateImage(image string) error {
	if image == "" || strings.Contains(image, "://") || strings.Contains(image, "//") || strings.HasSuffix(image, "/") || strings.HasSuffix(image, ":") || strings.ContainsAny(image, " \t\r\n\"'`$;\\") {
		return errors.New("invalid container image reference")
	}
	if i := strings.Index(image, "@"); i >= 0 && !digestPattern.MatchString(image[i+1:]) {
		return errors.New("invalid container image digest")
	}
	if _, err := imagename.ParseReference(image); err != nil {
		return errors.Wrap(err, "invalid container image reference")
	}
	return nil
}

// ContainerList locates the container list for each Cloud Run resource type.
func ContainerList(body map[string]any, kind string) ([]any, error) {
	template, ok := body["template"].(map[string]any)
	if ok && kind == "job" {
		template, ok = template["template"].(map[string]any)
	}
	if !ok {
		return nil, ruleErr("template.required", "a %s requires its container template", kind)
	}
	containers, ok := template["containers"].([]any)
	if !ok || len(containers) == 0 {
		return nil, ruleErr("template.containers", "template containers must be a nonempty array")
	}
	return containers, nil
}

// SetImage updates exactly one selected container, preserving sidecars.
func SetImage(body map[string]any, kind, selected, image string) error {
	if err := ValidateImage(image); err != nil {
		return err
	}
	container, err := SelectContainer(body, kind, selected)
	if err != nil {
		return err
	}
	container["image"] = image
	return nil
}

// SelectContainer resolves a unique image override without changing the manifest.
func SelectContainer(body map[string]any, kind, selected string) (map[string]any, error) {
	containers, err := ContainerList(body, kind)
	if err != nil {
		return nil, err
	}
	if selected == "" && len(containers) != 1 {
		return nil, errors.New("image_container is required for multiple containers")
	}
	var match map[string]any
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("container must be an object")
		}
		if selected == "" || container["name"] == selected {
			if match != nil {
				return nil, errors.New("image_container matches multiple containers")
			}
			match = container
		}
	}
	if match == nil {
		return nil, errors.Errorf("image_container %q must select exactly one container", selected)
	}
	return match, nil
}

// octalPattern matches YAML 1.1 octal integers such as 0644, which yaml.v3 decodes as octal.
var octalPattern = regexp.MustCompile(`^[-+]?0[0-9_]+$`)

// validateYAMLNodes requires string mapping keys and rejects merge keys, explicit tags, and YAML 1.1 octal integers.
func validateYAMLNodes(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Tag == "!!merge" {
				return ruleErr("config.merge-key", "line %d: YAML merge keys (<<) are not supported; repeat the values or move shared values into the base config", node.Content[i].Line)
			}
			if node.Content[i].Tag != "!!str" {
				return ruleErr("config.key-type", "line %d: YAML mapping keys must be strings", node.Content[i].Line)
			}
		}
	}
	if node.Kind == yaml.ScalarNode {
		if node.Style&yaml.TaggedStyle != 0 && node.Tag != "!!str" {
			return ruleErr("config.tag", "line %d: YAML tag %s is not supported; write the plain value", node.Line, node.Tag)
		}
		if node.Tag == "!!int" && octalPattern.MatchString(node.Value) {
			return ruleErr("config.octal", "line %d: %s is read as an octal number; write it in decimal, or as 0o%s if octal is intended", node.Line, node.Value, strings.TrimLeft(node.Value, "+-0"))
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNodes(child); err != nil {
			return err
		}
	}
	return nil
}

// rejectNulls reports a null in a base configuration, where it has nothing to delete.
func rejectNulls(value any, path string) error {
	switch v := value.(type) {
	case nil:
		return ruleErr("config.base-null", "%s: null is only valid in overlays, where it deletes an inherited field", path)
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			if err := rejectNulls(v[key], path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range v {
			if err := rejectNulls(item, formatIndex(path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkNullNoops rejects overlay nulls that have no inherited field to delete.
func checkNullNoops(base, overlay map[string]any, path string) error {
	for _, key := range slices.Sorted(maps.Keys(overlay)) {
		p := path + "." + key
		inherited, exists := base[key]
		switch value := overlay[key].(type) {
		case nil:
			if !exists {
				return ruleErr("config.null-noop", "%s: null deletes an inherited field, but the base config does not set one", p)
			}
		case map[string]any:
			child, _ := inherited.(map[string]any)
			if err := checkNullNoops(child, value, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// rejectStrayPlaceholders allows the image placeholder only on the container a deployment overrides.
func rejectStrayPlaceholders(body map[string]any, kind, selected string) error {
	containers, _ := ContainerList(body, kind) // Validate already required the container list.
	for _, raw := range containers {
		c := raw.(map[string]any)
		if c["image"] != ImagePlaceholder || (selected == "" && len(containers) == 1) || (selected != "" && c["name"] == selected) {
			continue
		}
		return ruleErr("image.placeholder", "container %q uses the image placeholder, but deployments only replace the image_container's image", stringAt(c, "name"))
	}
	return nil
}

// checkArrayShadow rejects overlay arrays that would silently replace an array the base also sets.
func checkArrayShadow(base, overlay map[string]any, path string) error {
	for _, key := range slices.Sorted(maps.Keys(overlay)) {
		p := path + "." + key
		switch value := overlay[key].(type) {
		case []any:
			if _, ok := base[key].([]any); ok {
				return ruleErr("config.array-shadow", "%s is set in both the base and the overlay; arrays replace rather than merge, so set it in only one of them", p)
			}
		case map[string]any:
			if child, ok := base[key].(map[string]any); ok {
				if err := checkArrayShadow(child, value, p); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
