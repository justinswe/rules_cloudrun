// Package deploy binds a rendered Cloud Run v2 manifest to its destination and applies it.
package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/justinswe/rules_cloudrun/internal/manifest"
	"github.com/justinswe/rules_cloudrun/internal/runapi"
	"github.com/justinswe/std/errors"
)

var (
	projectPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9:.-]*$`)
	regionPattern  = regexp.MustCompile(`^[a-z]+(?:-[a-z]+)+[0-9]+$`)
)

// Options describe one deployment of a rendered manifest.
type Options struct {
	// Manifest is the rendered JSON request body.
	Manifest map[string]any
	// Kind is service, job, or worker.
	Kind string
	// Name is the Cloud Run resource ID.
	Name    string
	Project string
	// Regions holds one region, or two or more for a multi-region service.
	Regions []string
	// Image optionally overrides ImageContainer's image; it must be pinned by digest.
	Image          string
	ImageContainer string
	ValidateOnly   bool
}

// ReadManifest reads a rendered manifest file.
func ReadManifest(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrap(err, "read manifest")
	}
	var body map[string]any
	if err := manifest.DecodeJSON(data, &body); err != nil {
		return nil, errors.Wrap(err, "parse manifest")
	}
	return body, nil
}

// Kind normalizes a resource kind, accepting workerpool for worker.
func Kind(kind string) string {
	if kind == "workerpool" {
		return "worker"
	}
	return kind
}

// Run binds the manifest, applies each bound body, and reports the result of each.
func Run(ctx context.Context, api *runapi.API, options Options, out io.Writer) error {
	bodies, err := Bind(options)
	if err != nil {
		return err
	}
	for _, body := range bodies {
		name, _ := body["name"].(string)
		if options.ValidateOnly && strings.Contains(name, "/locations/global/") {
			return errors.New("--validate-only is refused for multi-region services: Cloud Run applies requests to locations/global even when validateOnly is set")
		}
	}
	for _, body := range bodies {
		if err := apply(ctx, api, options, body, out); err != nil {
			return err
		}
	}
	return nil
}

// apply applies one bound body and prints its name, generation, and ready revision or execution.
func apply(ctx context.Context, api *runapi.API, options Options, body map[string]any, out io.Writer) error {
	name, _ := body["name"].(string)
	if err := api.Apply(ctx, options.Kind, body, options.ValidateOnly); err != nil {
		return err
	}
	if options.ValidateOnly {
		fmt.Fprintf(out, "%s: valid\n", name)
		return nil
	}
	current, err := api.Get(ctx, name)
	if err != nil {
		return err
	}
	generation, _ := current["generation"].(string)
	if options.Kind == "job" {
		execution, _ := current["latestCreatedExecution"].(map[string]any)
		fmt.Fprintf(out, "%s: generation %s, latest execution %v\n", name, generation, orNone(execution["name"]))
		return nil
	}
	fmt.Fprintf(out, "%s: generation %s, ready revision %v\n", name, generation, orNone(current["latestReadyRevision"]))
	return nil
}

// orNone substitutes "none" for an absent value.
func orNone(value any) any {
	if value == nil || value == "" {
		return "none"
	}
	return value
}

// Bind resolves the resource name, regions, and image override without changing the manifest.
func Bind(options Options) ([]map[string]any, error) {
	collection, err := manifest.ResourcePath(options.Kind)
	if err != nil {
		return nil, err
	}
	if err := validateDestination(options); err != nil {
		return nil, err
	}
	desired, err := withImage(options)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(options.Kind, desired); err != nil {
		return nil, err
	}
	if err := manifest.ValidateResourceIdentity(options.Kind, desired, options.Name); err != nil {
		return nil, err
	}
	if err := validateRegionalReferences(manifest.TaskTemplate(desired, options.Kind), options.Regions); err != nil {
		return nil, err
	}
	locations := options.Regions
	if options.Kind == "service" && (len(locations) > 1 || desired["multiRegionSettings"] != nil) {
		locations = []string{"global"}
	}
	var bodies []map[string]any
	for _, location := range locations {
		body, err := bindLocation(desired, options, collection, location)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, body)
	}
	return bodies, nil
}

// validateDestination checks the resource ID, project, and regions.
func validateDestination(options Options) error {
	if err := manifest.ValidateName(options.Name); err != nil {
		return err
	}
	if !projectPattern.MatchString(options.Project) {
		return errors.New("invalid deployment project")
	}
	if len(options.Regions) == 0 {
		return errors.New("at least one deployment region is required")
	}
	seen := map[string]bool{}
	for _, region := range options.Regions {
		if !regionPattern.MatchString(region) || seen[region] {
			return errors.New("deployment regions must be valid and unique")
		}
		seen[region] = true
	}
	return nil
}

// withImage copies the manifest and applies a digest-pinned image override.
func withImage(options Options) (map[string]any, error) {
	body := clone(options.Manifest)
	if options.Image == "" {
		return body, nil
	}
	if !strings.Contains(options.Image, "@sha256:") {
		return nil, errors.New("--image must be pinned to a sha256 digest")
	}
	if err := manifest.SetImage(body, options.Kind, options.ImageContainer, options.Image); err != nil {
		return nil, err
	}
	return body, nil
}

// bindLocation names one body for its location and sets multi-region settings at locations/global.
func bindLocation(desired map[string]any, options Options, collection, location string) (map[string]any, error) {
	body := clone(desired)
	name := fmt.Sprintf("projects/%s/locations/%s/%s/%s", options.Project, location, collection, options.Name)
	if configured, ok := body["name"]; ok && configured != name {
		return nil, errors.New("configured name conflicts with deployment destination")
	}
	body["name"] = name
	if location == "global" {
		if err := setMultiRegion(body, options.Regions); err != nil {
			return nil, err
		}
	} else if body["multiRegionSettings"] != nil {
		return nil, errors.New("multiRegionSettings requires a multi-region service")
	}
	if err := manifest.Validate(options.Kind, body); err != nil {
		return nil, err
	}
	containers, _ := manifest.ContainerList(body, options.Kind)
	for _, raw := range containers {
		if raw.(map[string]any)["image"] == manifest.ImagePlaceholder {
			return nil, errors.New("unresolved image placeholder; pass --image")
		}
	}
	return body, nil
}

// setMultiRegion sets multiRegionSettings.regions, rejecting a conflicting configured list.
func setMultiRegion(body map[string]any, regions []string) error {
	if existing, ok := body["multiRegionSettings"].(map[string]any); ok {
		want, _ := json.Marshal(regions)
		have, _ := json.Marshal(existing["regions"])
		if !bytes.Equal(want, have) {
			return errors.New("multiRegionSettings conflicts with deployment regions")
		}
		return nil
	}
	list := []any{}
	for _, region := range regions {
		list = append(list, region)
	}
	body["multiRegionSettings"] = map[string]any{"regions": list}
	return nil
}

// validateRegionalReferences prevents binding regional references to another region.
func validateRegionalReferences(template map[string]any, regions []string) error {
	references := []string{}
	if key, ok := template["encryptionKey"].(string); ok && key != "" {
		references = append(references, key)
	}
	if vpc, ok := template["vpcAccess"].(map[string]any); ok {
		if connector, ok := vpc["connector"].(string); ok && connector != "" {
			references = append(references, connector)
		}
		interfaces, _ := vpc["networkInterfaces"].([]any)
		for _, raw := range interfaces {
			iface := raw.(map[string]any)
			if sub, ok := iface["subnetwork"].(string); ok && strings.HasPrefix(sub, "projects/") {
				references = append(references, sub)
			}
		}
	}
	for _, reference := range references {
		parts := strings.Split(reference, "/")
		if len(parts) < 6 || parts[0] != "projects" || (parts[2] != "locations" && parts[2] != "regions") {
			return errors.New("invalid regional resource reference")
		}
		for _, region := range regions {
			if parts[3] != region {
				return errors.Errorf("regional resource reference belongs to %s, but deployment includes %s", parts[3], region)
			}
		}
	}
	return nil
}

// clone deep-copies a validated JSON object.
func clone(body map[string]any) map[string]any {
	encoded, _ := json.Marshal(body) // Manifests hold only JSON values.
	var copied map[string]any
	_ = manifest.DecodeJSON(encoded, &copied)
	return copied
}
