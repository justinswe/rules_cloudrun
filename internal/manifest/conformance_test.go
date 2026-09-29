package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2/google"
)

var (
	conformanceProject = flag.String("conformance-project", "", "Project for Cloud Run validateOnly conformance checks; empty skips them")
	conformanceRegion  = flag.String("conformance-region", "us-central1", "Region for conformance checks")
	conformanceImage   = flag.String("conformance-image", "us-docker.pkg.dev/cloudrun/container/hello", "Image the API can resolve, replacing fixture images")
)

// policyRules are intentionally stricter than Cloud Run's validateOnly check, so the server may accept them.
var policyRules = map[string]bool{
	// Repository policy: explicit billing mode and a reviewed GPU allowlist.
	"compute.cpu-idle-explicit": true,
	"gpu.accelerator":           true,
	// Mistakes validateOnly lets through that would misbehave or be ignored at runtime.
	"schema.output-only":           true,
	"duration.format":              true,
	"env.duplicate":                true,
	"env.name":                     true,
	"env.nul":                      true,
	"env.secret-required":          true,
	"env.reserved-google":          true,
	"mount.duplicate-path":         true,
	"container.service-only":       true,
	"container.working-dir":        true,
	"container.nul":                true,
	"container.empty-command":      true,
	"probe.timeout-period":         true,
	"probe.startup-budget":         true,
	"probe.readiness-service-only": true,
	"volume.name":                  true,
	"volume.source":                true,
	"volume.gen1-source":           true,
	"volume.nfs":                   true,
	"volume.cloudsql-empty":        true,
	"source.required":              true,
	"fractional-cpu.gen1":          true,
	// Deployer-owned or ambiguous values: the deployer takes etag from the live resource, and
	// null and *_UNSPECIFIED only restate a default.
	"resource.etag":           true,
	"schema.null":             true,
	"schema.enum-unspecified": true,
	// Cloud Run accepts '..' path segments in secret items; the build keeps mounts inside the volume.
	"secret-item.path": true,
}

// localOnlyRules cannot be exercised by a regional validateOnly create request.
var localOnlyRules = map[string]string{
	"resource.name":             "create requests carry the resource ID in the query and reject a body name",
	"resource.id":               "create requests carry the resource ID in the query and reject a body name",
	"regions.count":             "Cloud Run applies multi-region requests even when validateOnly is set",
	"regions.unique":            "Cloud Run applies multi-region requests even when validateOnly is set",
	"encryption.shutdown":       "Cloud Run needs a Cloud KMS key the project can use",
	"gpu.accelerator-container": "Cloud Run checks GPU quota first",
}

// serverReasons maps each rule Cloud Run also enforces to a substring of its rejection message.
var serverReasons = map[string]string{
	"schema.type":                   "Invalid value at 'service.labels'",
	"schema.unknown-field":          "Cannot find field",
	"schema.required":               "has no task template",
	"schema.oneof":                  "oneof field",
	"schema.enum":                   "google.cloud.run.v2.ExecutionEnvironment",
	"schema.int32":                  "TYPE_INT32",
	"schema.int64":                  "TYPE_INT64",
	"schema.format":                 "Illegal duration format",
	"volume.duplicate":              "Duplicate volume name",
	"container.duplicate":           "duplicate container names",
	"mount.unknown-volume":          "which were not found in volumes list",
	"mount.relative-path":           "must be a valid unix absolute path",
	"mount.secret-subpath":          "cannot have sub_path set",
	"container.serving-ports":       "exactly one container with an exposed port",
	"metadata.reserved":             "system annotations are not supported",
	"scaling.range":                 "must be greater than or equal to zero",
	"scaling.order":                 "must be greater than or equal to min_instance_count",
	"scaling.manual-mode":           "scaling mode must be set to MANUAL",
	"launch-stage":                  "only ALPHA, BETA, GA, or unspecified are supported",
	"template.concurrency":          "max_instance_request_concurrency: Must be a number between 0 and 1000",
	"template.max-retries":          "max_retries: must be a number between 0 and 10",
	"template.timeout":              "maximum allowed time is one hour",
	"template.containers":           "must contain at least one container",
	"execution.task-count":          "task_count: must be a number between 0 and 10000",
	"execution.parallelism":         "parallelism: must not be negative",
	"container.name":                "must conform to RFC 1123",
	"container.image":               "must be a container image path",
	"container.image-required":      "must provide an image name to deploy",
	"container.ports":               "ports should contain 0 or 1 port",
	"container.ingress":             "exactly one container with an exposed port",
	"container.count":               "at most 10 containers",
	"container.args-count":          "limit on args across all containers",
	"env.count":                     "limit on EnvVars across all containers",
	"env.reserved-service":          "reserved env names were provided: K_SERVICE",
	"env.reserved-port":             "reserved env names were provided: PORT",
	"env.reserved-job":              "reserved env names were provided: CLOUD_RUN_JOB",
	"env.value-size":                "length cannot exceed 32768",
	"secret.reference":              "secret_key_ref.secret: may only be",
	"secret.version":                "secret_key_ref.version: should have only alphanumeric characters",
	"secret.mode":                   "default_mode: if present, must be a supported unix chmod bitmask",
	"secret-item.version":           "items[0].version: should have only alphanumeric characters",
	"secret-item.mode":              "items[0].mode: if present, must be a supported unix chmod bitmask",
	"probe.type":                    "Exactly one probe action must be set",
	"probe.tcp-startup-only":        "does not support TCP socket in readiness probe",
	"probe.interval":                "_seconds must be a number between 0 and",
	"probe.initial-delay":           "initial_delay_seconds must be a number between",
	"probe.readiness-delay":         "Initial delay is not supported for readiness probe",
	"probe.failure-threshold":       "Must be a positive number if set",
	"probe.port":                    "if specified, must be between 1 and 65535",
	"probe.header":                  "a valid HTTP header must consist of alphanumeric characters",
	"probe.job-liveness":            "does not support liveness probe",
	"port.name":                     "must be one of empty, 'http1' or 'h2c'",
	"port.range":                    "container_port: must be 0",
	"vpc.mode":                      "one and only one of connector and network_interfaces must be set",
	"vpc.interfaces":                "only 1 network interface is allowed",
	"vpc.network":                   "at least one of network or subnetwork should not be empty",
	"vpc.connector":                 "connector: must be in form projects/",
	"volume.size-quantity":          "size_limit: Could not parse Quantity",
	"volume.gcs-bucket":             "Bucket name must be specified",
	"volume.cloudsql-instance":      "must be in the format project:location:instance",
	"volume.cloudsql-name":          "Cloud SQL volume must be named 'cloudsql'",
	"mount.colon":                   "must be a valid unix absolute path",
	"mount.reserved-path":           "are in one of disallowed mount points",
	"split.percent":                 "percent: must be a number between 0 and 100",
	"split.revision":                "must be specified if and only if traffic type is",
	"split.latest":                  "must be specified if and only if traffic type is",
	"split.tag":                     "may not contain a duplicate tag",
	"split.total":                   "should be 100",
	"compute.limit":                 "limits are supported",
	"compute.cpu-quantity":          "limits.cpu: Could not parse Quantity",
	"compute.rtx-cpu":               "Must be equal to one of 20.0, 22.0",
	"compute.cpu":                   "Must be equal to one of [.08-1]",
	"compute.memory-quantity":       "limits.memory: Could not parse Quantity",
	"compute.rtx-memory":            "memory must be between 80Gi",
	"compute.memory":                "Invalid value specified for container memory",
	"compute.cpu-idle-service-only": "CPU idle must be set to false",
	"fractional-cpu.kind":           "Total cpu",
	"fractional-cpu.concurrency":    "not supported with concurrency",
	"fractional-cpu.cpu-idle":       "not supported with cpu always allocated",
	"gpu.limit":                     "GPU can only be set to 1",
	"gpu.l4-minimum":                "CPU must be at least 4; memory must be at least 16Gi",
	"gpu.gen1":                      "Execution environment must be set to gen2 in order to set GPU",
	"gpu.cpu-idle":                  "must be disabled in order to set GPU requirements",
	"gpu.job-zonal":                 "unable to offer GPU enabled instances with zonal redundancy",
	"gpu.job-timeout":               "when using GPU",
	"gpu.count":                     "on one container",
	"gpu.zonal-without-gpu":         "must be set when GPU redundancy is set",
	"dependency.unknown":            "does not exist",
	"dependency.cycle":              "dependency cycle detected",
	"runtime.gen1":                  "is not supported on resources of kind Execution",
	"identity.service-account":      "Unsupported service account",
	"encryption.key":                "encryption_key: must conform to",
	"encryption.requires-key":       "encryption_key must be set if this field is set",
}

// TestConformanceTablesCoverRules keeps the conformance tables in step with the rule table without calling Cloud Run.
func TestConformanceTablesCoverRules(t *testing.T) {
	ids := map[string]bool{}
	for _, tc := range ruleCases {
		ids[tc.id] = true
		if tc.change == nil || tc.check != nil {
			continue
		}
		_, reason := serverReasons[tc.id]
		_, local := localOnlyRules[tc.id]
		classes := 0
		for _, in := range []bool{reason, local, policyRules[tc.id]} {
			if in {
				classes++
			}
		}
		require.Equal(t, 1, classes, "rule %s needs exactly one of a server reason, a policy tag, or a local-only reason", tc.id)
	}
	for _, id := range slices.Concat(slices.Collect(maps.Keys(serverReasons)), slices.Collect(maps.Keys(localOnlyRules)), slices.Collect(maps.Keys(policyRules))) {
		require.True(t, ids[id], "conformance table names unknown rule %s", id)
	}
}

// TestConformance compares local verdicts with Cloud Run's validateOnly verdicts.
//
// bazel test //internal/manifest:manifest_test --test_filter=TestConformance \
//
//	--test_arg=-conformance-project=PROJECT --test_env=HOME --spawn_strategy=local --test_output=all
func TestConformance(t *testing.T) {
	if *conformanceProject == "" {
		t.Skip("set -conformance-project to compare local rules with Cloud Run")
	}
	client, err := google.DefaultClient(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
	require.NoError(t, err)
	accepted := acceptedBodies()
	for _, kind := range []string{"service", "job", "worker"} {
		accepted["base "+kind] = kindBody{kind, validBody(kind)}
	}
	for _, name := range slices.Sorted(maps.Keys(accepted)) {
		t.Run("accept/"+name, func(t *testing.T) {
			status, text := validateRemote(t, client, accepted[name].kind, accepted[name].body)
			require.Equal(t, http.StatusOK, status, "Cloud Run rejected a body the build accepts (over-strict server or bad fixture): %s", text)
		})
	}
	for _, tc := range ruleCases {
		if _, local := localOnlyRules[tc.id]; local || tc.change == nil || tc.check != nil {
			continue
		}
		t.Run("reject/"+tc.id, func(t *testing.T) {
			body := validBody(tc.kind)
			tc.change(body)
			status, text := validateRemote(t, client, tc.kind, body)
			if policyRules[tc.id] {
				t.Logf("policy rule; Cloud Run returned HTTP %d", status)
				return
			}
			reason, ok := serverReasons[tc.id]
			require.True(t, ok, "rule %s has no expected server reason; Cloud Run returned HTTP %d: %s", tc.id, status, firstLine(text))
			require.Equal(t, http.StatusBadRequest, status, "Cloud Run must reject rule %s as invalid: %s", tc.id, firstLine(text))
			require.Contains(t, text, reason, "Cloud Run rejected rule %s for a different reason", tc.id)
		})
	}
}

// validateRemote sends a validateOnly create request and returns the HTTP status and response body.
func validateRemote(t *testing.T, client *http.Client, kind string, body map[string]any) (int, string) {
	collection, err := ResourcePath(kind)
	require.NoError(t, err)
	useResolvableImages(body, kind)
	data, err := json.Marshal(body)
	require.NoError(t, err)
	if body["multiRegionSettings"] != nil {
		// Cloud Run creates or updates multi-region services even when validateOnly is set.
		t.Skip("multi-region bodies are never sent: Cloud Run ignores validateOnly at locations/global")
	}
	id := strings.TrimSuffix(collection, "s") + "Id"
	endpoint := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/%s?%s=conformance-check&validateOnly=true", *conformanceProject, *conformanceRegion, collection, id)
	response, err := client.Post(endpoint, "application/json", bytes.NewReader(data))
	require.NoError(t, err)
	defer response.Body.Close()
	text, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(text)
}

// useResolvableImages swaps fixture images for one the API can resolve, leaving deliberately invalid images alone.
func useResolvableImages(body map[string]any, kind string) {
	containers, _ := ContainerList(body, kind)
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if ok && strings.HasPrefix(stringAt(container, "image"), "example.com/") {
			container["image"] = *conformanceImage
		}
	}
}

// firstLine compacts a response body for test logs.
func firstLine(text string) string {
	joined := strings.Join(strings.Fields(text), " ")
	if len(joined) > 300 {
		return joined[:300]
	}
	return joined
}
