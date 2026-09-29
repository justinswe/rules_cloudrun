package manifest

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/justinswe/std/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/descriptorpb"
)

// sources holds the package's Go files so the rule-ID scan covers every rule site.
//
//go:embed *.go
var sources embed.FS

// ruleCase is one mutation of a valid body that must fail with exactly its rule ID.
type ruleCase struct {
	id, kind string
	change   func(body map[string]any)
	check    func(kind string, body map[string]any) error
}

// setVolumes installs volumes on the task template.
func setVolumes(b map[string]any, kind string, volumes ...map[string]any) {
	list := []any{}
	for _, v := range volumes {
		list = append(list, v)
	}
	TaskTemplate(b, kind)["volumes"] = list
}

// setMounts installs volume mounts on the first container.
func setMounts(b map[string]any, kind string, mounts ...map[string]any) {
	list := []any{}
	for _, m := range mounts {
		list = append(list, m)
	}
	firstContainer(b, kind)["volumeMounts"] = list
}

// setEnv installs environment variables on the first container.
func setEnv(b map[string]any, kind string, env ...map[string]any) {
	list := []any{}
	for _, e := range env {
		list = append(list, e)
	}
	firstContainer(b, kind)["env"] = list
}

// setLimits installs resource limits (and optional extra resource fields) on the first container.
func setLimits(b map[string]any, kind string, limits map[string]any, extra ...func(map[string]any)) {
	resources := map[string]any{"limits": limits, "cpuIdle": false}
	for _, apply := range extra {
		apply(resources)
	}
	firstContainer(b, kind)["resources"] = resources
}

// l4GPU configures a valid single L4 GPU on the first container.
func l4GPU(b map[string]any, kind string) {
	setLimits(b, kind, map[string]any{"cpu": "4", "memory": "16Gi", "nvidia.com/gpu": "1"})
	TaskTemplate(b, kind)["nodeSelector"] = map[string]any{"accelerator": "nvidia-l4"}
}

// addContainer appends a named sidecar with extra fields.
func addContainer(b map[string]any, kind, name string, fields map[string]any) {
	c := map[string]any{"name": name, "image": "example.com/" + name + ":latest"}
	for k, v := range fields {
		c[k] = v
	}
	TaskTemplate(b, kind)["containers"] = append(arrayAt(TaskTemplate(b, kind), "containers"), c)
}

func memoryVolume() map[string]any {
	return map[string]any{"name": "v", "emptyDir": map[string]any{"medium": "MEMORY"}}
}

func identity(kind string, body map[string]any) error {
	return ValidateResourceIdentity(kind, body, "app")
}

// imageConfig checks the first container against an OCI image configuration.
func imageConfig(config string) func(string, map[string]any) error {
	return func(kind string, body map[string]any) error {
		return ValidateImageConfig(firstContainer(body, kind), []byte(config))
	}
}

// cloudSQLVolume is a valid Cloud SQL volume; Cloud Run requires the name cloudsql.
func cloudSQLVolume(instances ...any) map[string]any {
	return map[string]any{"name": "cloudsql", "cloudSqlInstance": map[string]any{"instances": instances}}
}

// fillContainers gives every container n entries of env or args, with an ingress port on the first.
func fillContainers(b map[string]any, key string, n int) {
	firstContainer(b, "service")["ports"] = []any{map[string]any{"containerPort": 8080}}
	addContainer(b, "service", "sidecar", nil)
	for _, raw := range arrayAt(TaskTemplate(b, "service"), "containers") {
		values := []any{}
		for i := 0; i < n; i++ {
			if key == "env" {
				values = append(values, map[string]any{"name": fmt.Sprintf("V%d", i)})
			} else {
				values = append(values, "a")
			}
		}
		raw.(map[string]any)[key] = values
	}
}

// renderYAML checks a base and overlay pair through the full render path.
func renderYAML(base, overlay string) func(string, map[string]any) error {
	return func(kind string, _ map[string]any) error {
		_, err := Render([]byte(base), []byte(overlay), RenderOptions{ServiceName: "app", ResourceType: kind})
		return err
	}
}

var ruleCases = []ruleCase{
	{id: "schema.type", kind: "service", change: func(b map[string]any) { b["labels"] = "x" }},
	{id: "schema.unknown-field", kind: "service", change: func(b map[string]any) { b["typo"] = true }},
	{id: "schema.output-only", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["buildInfo"] = map[string]any{} }},
	{id: "schema.required", kind: "job", change: func(b map[string]any) { delete(objectAt(b, "template"), "template") }},
	{id: "schema.oneof", kind: "job", change: func(b map[string]any) { b["runExecutionToken"] = "a"; b["startExecutionToken"] = "b" }},
	{id: "schema.enum", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["executionEnvironment"] = "GEN3" }},
	{id: "schema.int32", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["maxInstanceRequestConcurrency"] = json.Number("2147483648")
	}},
	{id: "schema.int64", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["sourceCode"] = map[string]any{"cloudStorageSource": map[string]any{"bucket": "b", "object": "o", "generation": "x"}}
	}},
	{id: "schema.format", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["timeout"] = "2m" }},
	{id: "schema.kind", kind: "service", check: func(string, map[string]any) error {
		field := (&descriptorpb.UninterpretedOption{}).ProtoReflect().Descriptor().Fields().ByName("double_value")
		return validateSingular(1.5, field, "option", true)
	}},
	{id: "duration.format", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["timeout"] = "-5s" }},
	{id: "volume.duplicate", kind: "service", change: func(b map[string]any) { setVolumes(b, "service", memoryVolume(), memoryVolume()) }},
	{id: "container.duplicate", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["containers"] = []any{firstContainer(b, "service"), firstContainer(b, "service")}
	}},
	{id: "env.duplicate", kind: "service", change: func(b map[string]any) { setEnv(b, "service", map[string]any{"name": "X"}, map[string]any{"name": "X"}) }},
	{id: "mount.unknown-volume", kind: "worker", change: func(b map[string]any) { setMounts(b, "worker", map[string]any{"name": "missing", "mountPath": "/tmp"}) }},
	{id: "mount.duplicate-path", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", memoryVolume())
		setMounts(b, "service", map[string]any{"name": "v", "mountPath": "/data"}, map[string]any{"name": "v", "mountPath": "/data/"})
	}},
	{id: "mount.relative-path", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", memoryVolume())
		setMounts(b, "service", map[string]any{"name": "v", "mountPath": "relative"})
	}},
	{id: "mount.secret-subpath", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "v", "secret": map[string]any{"secret": "s"}})
		setMounts(b, "service", map[string]any{"name": "v", "mountPath": "/s", "subPath": "x"})
	}},
	{id: "container.serving-ports", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["ports"] = []any{map[string]any{"containerPort": 8080}}
		addContainer(b, "service", "sidecar", map[string]any{"ports": []any{map[string]any{"containerPort": 9090}}})
	}},
	{id: "metadata.reserved", kind: "job", change: func(b map[string]any) { b["annotations"] = map[string]any{"serving.knative.dev/label": "x"} }},
	{id: "resource.name", kind: "service", change: func(b map[string]any) { b["name"] = "wrong" }},
	{id: "resource.id", kind: "service", change: func(b map[string]any) { b["name"] = "projects/test/locations/us-west1/services/0bad" }},
	{id: "scaling.range", kind: "worker", change: func(b map[string]any) { b["scaling"] = map[string]any{"manualInstanceCount": -1} }},
	{id: "scaling.range", kind: "service", change: func(b map[string]any) { b["scaling"] = map[string]any{"maxInstanceCount": -1} }},
	{id: "scaling.order", kind: "service", change: func(b map[string]any) { b["scaling"] = map[string]any{"minInstanceCount": 10, "maxInstanceCount": 1} }},
	{id: "scaling.manual-mode", kind: "service", change: func(b map[string]any) {
		b["scaling"] = map[string]any{"manualInstanceCount": 1, "scalingMode": "AUTOMATIC"}
	}},
	{id: "scaling.manual-mode", kind: "service", change: func(b map[string]any) { b["scaling"] = map[string]any{"manualInstanceCount": 2} }},
	{id: "launch-stage", kind: "service", change: func(b map[string]any) { b["launchStage"] = "PRELAUNCH" }},
	{id: "schema.enum-unspecified", kind: "service", change: func(b map[string]any) { b["ingress"] = "INGRESS_TRAFFIC_UNSPECIFIED" }},
	{id: "schema.null", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["command"] = []any{nil} }},
	{id: "resource.etag", kind: "service", change: func(b map[string]any) { b["etag"] = "stale" }},
	{id: "regions.count", kind: "service", change: func(b map[string]any) { b["multiRegionSettings"] = map[string]any{"regions": []any{}} }},
	{id: "regions.count", kind: "service", change: func(b map[string]any) { b["multiRegionSettings"] = map[string]any{"regions": []any{"us-west1"}} }},
	{id: "regions.unique", kind: "service", change: func(b map[string]any) {
		b["multiRegionSettings"] = map[string]any{"regions": []any{"us-west1", "us-west1"}}
	}},
	{id: "template.concurrency", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["maxInstanceRequestConcurrency"] = 1001 }},
	{id: "template.max-retries", kind: "job", change: func(b map[string]any) { TaskTemplate(b, "job")["maxRetries"] = 11 }},
	{id: "template.timeout", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["timeout"] = "3601s" }},
	{id: "execution.task-count", kind: "job", change: func(b map[string]any) { objectAt(b, "template")["taskCount"] = 10001 }},
	{id: "execution.parallelism", kind: "job", change: func(b map[string]any) { objectAt(b, "template")["parallelism"] = -1 }},
	{id: "container.name", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["name"] = "invalid_name" }},
	{id: "container.name", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["name"] = "app." }},
	{id: "container.image", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["image"] = "image@sha256:bad" }},
	{id: "container.image-required", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["image"] = "" }},
	{id: "container.ports", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["ports"] = []any{map[string]any{}, map[string]any{}}
	}},
	{id: "container.service-only", kind: "job", change: func(b map[string]any) {
		firstContainer(b, "job")["ports"] = []any{map[string]any{"containerPort": 8080}}
	}},
	{id: "env.name", kind: "service", change: func(b map[string]any) { setEnv(b, "service", map[string]any{"name": "A=B"}) }},
	{id: "env.reserved-service", kind: "service", change: func(b map[string]any) { setEnv(b, "service", map[string]any{"name": "K_SERVICE"}) }},
	{id: "env.reserved-port", kind: "job", change: func(b map[string]any) { setEnv(b, "job", map[string]any{"name": "PORT", "value": "9000"}) }},
	{id: "env.reserved-google", kind: "job", change: func(b map[string]any) { setEnv(b, "job", map[string]any{"name": "X_GOOGLE_TOKEN"}) }},
	{id: "env.value-size", kind: "service", change: func(b map[string]any) {
		setEnv(b, "service", map[string]any{"name": "X", "value": strings.Repeat("x", 32769)})
	}},
	{id: "env.secret-required", kind: "service", change: func(b map[string]any) {
		setEnv(b, "service", map[string]any{"name": "X", "valueSource": map[string]any{}})
	}},
	{id: "secret.reference", kind: "service", change: func(b map[string]any) {
		setEnv(b, "service", map[string]any{"name": "X", "valueSource": map[string]any{"secretKeyRef": map[string]any{"secret": "wrong/path"}}})
	}},
	{id: "secret.version", kind: "service", change: func(b map[string]any) {
		setEnv(b, "service", map[string]any{"name": "X", "valueSource": map[string]any{"secretKeyRef": map[string]any{"secret": "s", "version": "?"}}})
	}},
	{id: "secret.mode", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "v", "secret": map[string]any{"secret": "s", "defaultMode": 512}})
	}},
	{id: "secret-item.path", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "v", "secret": map[string]any{"secret": "s", "items": []any{map[string]any{"path": "../x", "version": "latest"}}}})
	}},
	{id: "secret-item.version", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "v", "secret": map[string]any{"secret": "s", "items": []any{map[string]any{"path": "x", "version": "?"}}}})
	}},
	{id: "secret-item.version", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "v", "secret": map[string]any{"secret": "s", "items": []any{map[string]any{"path": "x"}}}})
	}},
	{id: "secret-item.mode", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "v", "secret": map[string]any{"secret": "s", "items": []any{map[string]any{"path": "x", "version": "latest", "mode": 512}}}})
	}},
	{id: "probe.type", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["startupProbe"] = map[string]any{} }},
	{id: "probe.tcp-startup-only", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["readinessProbe"] = map[string]any{"tcpSocket": map[string]any{}}
	}},
	{id: "probe.interval", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["startupProbe"] = map[string]any{"httpGet": map[string]any{}, "periodSeconds": 241}
	}},
	{id: "probe.interval", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["readinessProbe"] = map[string]any{"httpGet": map[string]any{}, "periodSeconds": 400, "timeoutSeconds": 301}
	}},
	{id: "probe.readiness-delay", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["readinessProbe"] = map[string]any{"httpGet": map[string]any{}, "initialDelaySeconds": 5}
	}},
	{id: "probe.initial-delay", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["livenessProbe"] = map[string]any{"httpGet": map[string]any{}, "initialDelaySeconds": 3601}
	}},
	{id: "probe.failure-threshold", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["startupProbe"] = map[string]any{"httpGet": map[string]any{}, "failureThreshold": -1}
	}},
	{id: "probe.timeout-period", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["startupProbe"] = map[string]any{"httpGet": map[string]any{}, "timeoutSeconds": 11}
	}},
	{id: "probe.port", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["startupProbe"] = map[string]any{"httpGet": map[string]any{"port": 65536}}
	}},
	{id: "probe.header", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["readinessProbe"] = map[string]any{"httpGet": map[string]any{"httpHeaders": []any{map[string]any{"name": "X\nHeader"}}}}
	}},
	{id: "probe.header", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["readinessProbe"] = map[string]any{"httpGet": map[string]any{"httpHeaders": []any{map[string]any{"name": "X Probe"}}}}
	}},
	{id: "probe.startup-budget", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["startupProbe"] = map[string]any{"grpc": map[string]any{}, "periodSeconds": 240, "failureThreshold": 3}
	}},
	{id: "probe.job-liveness", kind: "job", change: func(b map[string]any) {
		firstContainer(b, "job")["livenessProbe"] = map[string]any{"httpGet": map[string]any{}}
	}},
	{id: "probe.readiness-service-only", kind: "worker", change: func(b map[string]any) {
		firstContainer(b, "worker")["readinessProbe"] = map[string]any{"httpGet": map[string]any{}}
	}},
	{id: "port.name", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["ports"] = []any{map[string]any{"name": "http2", "containerPort": 8080}}
	}},
	{id: "port.range", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["ports"] = []any{map[string]any{"containerPort": 0}}
	}},
	{id: "port.range", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["ports"] = []any{map[string]any{"name": "h2c"}} }},
	{id: "vpc.mode", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["vpcAccess"] = map[string]any{"connector": "projects/p/locations/us-central1/connectors/c", "networkInterfaces": []any{map[string]any{"network": "n"}}}
	}},
	{id: "vpc.interfaces", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["vpcAccess"] = map[string]any{"networkInterfaces": []any{map[string]any{"network": "n"}, map[string]any{"network": "n"}}}
	}},
	{id: "vpc.network", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["vpcAccess"] = map[string]any{"networkInterfaces": []any{map[string]any{}}}
	}},
	{id: "vpc.connector", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["vpcAccess"] = map[string]any{"connector": "c"} }},
	{id: "volume.name", kind: "worker", change: func(b map[string]any) {
		setVolumes(b, "worker", map[string]any{"name": "BAD", "emptyDir": map[string]any{}})
	}},
	{id: "volume.source", kind: "service", change: func(b map[string]any) { setVolumes(b, "service", map[string]any{"name": "v"}) }},
	{id: "volume.size-quantity", kind: "worker", change: func(b map[string]any) {
		setVolumes(b, "worker", map[string]any{"name": "v", "emptyDir": map[string]any{"sizeLimit": "wrong"}})
	}},
	{id: "volume.gen1-source", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["executionEnvironment"] = "EXECUTION_ENVIRONMENT_GEN1"
		setVolumes(b, "service", memoryVolume())
	}},
	{id: "volume.gcs-bucket", kind: "service", change: func(b map[string]any) { setVolumes(b, "service", map[string]any{"name": "v", "gcs": map[string]any{}}) }},
	{id: "volume.nfs", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "v", "nfs": map[string]any{"server": "s", "path": "relative"}})
	}},
	{id: "volume.cloudsql-empty", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "cloudsql", "cloudSqlInstance": map[string]any{}})
	}},
	{id: "volume.cloudsql-instance", kind: "service", change: func(b map[string]any) { setVolumes(b, "service", cloudSQLVolume("bad")) }},
	{id: "volume.cloudsql-name", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "db", "cloudSqlInstance": map[string]any{"instances": []any{"p:us-west1:db"}}})
	}},
	{id: "volume.cloudsql-name", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", map[string]any{"name": "cloudsql", "emptyDir": map[string]any{"medium": "MEMORY"}})
	}},
	{id: "mount.colon", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", memoryVolume())
		setMounts(b, "service", map[string]any{"name": "v", "mountPath": "/foo:bar"})
	}},
	{id: "mount.reserved-path", kind: "service", change: func(b map[string]any) {
		setVolumes(b, "service", memoryVolume())
		setMounts(b, "service", map[string]any{"name": "v", "mountPath": "/data/../proc"})
	}},
	{id: "source.required", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["sourceCode"] = map[string]any{} }},
	{id: "split.percent", kind: "service", change: func(b map[string]any) {
		b["traffic"] = []any{map[string]any{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST", "percent": 101}}
	}},
	{id: "split.revision", kind: "service", change: func(b map[string]any) {
		b["traffic"] = []any{map[string]any{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "percent": 100}}
	}},
	{id: "split.latest", kind: "worker", change: func(b map[string]any) {
		b["instanceSplits"] = []any{map[string]any{"type": "INSTANCE_SPLIT_ALLOCATION_TYPE_LATEST", "revision": "v1", "percent": 100}}
	}},
	{id: "split.tag", kind: "service", change: func(b map[string]any) {
		b["traffic"] = []any{
			map[string]any{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST", "percent": 50, "tag": "same"},
			map[string]any{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST", "percent": 50, "tag": "same"},
		}
	}},
	{id: "split.total", kind: "service", change: func(b map[string]any) {
		b["traffic"] = []any{map[string]any{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST", "percent": 90}}
	}},
	{id: "compute.limit", kind: "service", change: func(b map[string]any) { setLimits(b, "service", map[string]any{"disk": "1Gi"}) }},
	{id: "compute.cpu-quantity", kind: "service", change: func(b map[string]any) { setLimits(b, "service", map[string]any{"cpu": "bad"}) }},
	{id: "compute.rtx-cpu", kind: "service", change: func(b map[string]any) {
		setLimits(b, "service", map[string]any{"cpu": "8", "memory": "80Gi", "nvidia.com/gpu": "1"})
		TaskTemplate(b, "service")["nodeSelector"] = map[string]any{"accelerator": "nvidia-rtx-pro-6000"}
	}},
	{id: "compute.rtx-cpu", kind: "service", change: func(b map[string]any) {
		setLimits(b, "service", map[string]any{"cpu": "21", "memory": "80Gi", "nvidia.com/gpu": "1"})
		TaskTemplate(b, "service")["nodeSelector"] = map[string]any{"accelerator": "nvidia-rtx-pro-6000"}
	}},
	{id: "compute.cpu", kind: "service", change: func(b map[string]any) { setLimits(b, "service", map[string]any{"cpu": "3"}) }},
	{id: "compute.memory-quantity", kind: "service", change: func(b map[string]any) { setLimits(b, "service", map[string]any{"memory": "bad"}) }},
	{id: "compute.rtx-memory", kind: "service", change: func(b map[string]any) {
		setLimits(b, "service", map[string]any{"cpu": "20", "memory": "64Gi", "nvidia.com/gpu": "1"})
		TaskTemplate(b, "service")["nodeSelector"] = map[string]any{"accelerator": "nvidia-rtx-pro-6000"}
	}},
	{id: "compute.memory", kind: "service", change: func(b map[string]any) { setLimits(b, "service", map[string]any{"cpu": "1", "memory": "8Gi"}) }},
	{id: "compute.memory", kind: "service", change: func(b map[string]any) {
		setLimits(b, "service", map[string]any{"cpu": "20", "memory": "100Gi", "nvidia.com/gpu": "1"})
		TaskTemplate(b, "service")["nodeSelector"] = map[string]any{"accelerator": "nvidia-rtx-pro-6000"}
	}},
	{id: "compute.cpu-idle-service-only", kind: "job", change: func(b map[string]any) {
		setLimits(b, "job", map[string]any{}, func(r map[string]any) { r["cpuIdle"] = true })
	}},
	{id: "fractional-cpu.kind", kind: "job", change: func(b map[string]any) { setLimits(b, "job", map[string]any{"cpu": "0.5"}) }},
	{id: "fractional-cpu.concurrency", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["maxInstanceRequestConcurrency"] = 80
		setLimits(b, "service", map[string]any{"cpu": "0.5"}, func(r map[string]any) { r["cpuIdle"] = true })
	}},
	{id: "fractional-cpu.cpu-idle", kind: "service", change: func(b map[string]any) { setLimits(b, "service", map[string]any{"cpu": "0.5"}) }},
	{id: "fractional-cpu.gen1", kind: "service", change: func(b map[string]any) {
		setLimits(b, "service", map[string]any{"cpu": "0.5"}, func(r map[string]any) { r["cpuIdle"] = true })
	}},
	{id: "compute.cpu-idle-explicit", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["resources"] = map[string]any{"limits": map[string]any{"memory": "1Gi"}}
	}},
	{id: "container.ingress", kind: "service", change: func(b map[string]any) { addContainer(b, "service", "sidecar", nil) }},
	{id: "config.base-null", kind: "service", check: renderYAML("description: null\n", "template: {containers: [{image: example.com/app:latest}]}\n")},
	{id: "config.array-shadow", kind: "service", check: renderYAML(
		"template: {containers: [{name: app, image: example.com/app:latest, resources: {cpuIdle: true}}]}\n",
		"template: {containers: [{name: app, image: example.com/app:v2}]}\n",
	)},
	{id: "config.merge-key", kind: "service", check: renderYAML("", "defaults: &d {image: example.com/app:latest}\ntemplate: {containers: [{<<: *d}]}\n")},
	{id: "config.key-type", kind: "service", check: renderYAML("", "1: x\n")},
	{id: "config.tag", kind: "service", check: renderYAML("", "description: !env FOO\ntemplate: {containers: [{image: example.com/app:latest}]}\n")},
	{id: "config.octal", kind: "service", check: renderYAML("", "template: {maxInstanceRequestConcurrency: 010, containers: [{image: example.com/app:latest}]}\n")},
	{id: "config.null-noop", kind: "service", check: renderYAML("", "description: null\ntemplate: {containers: [{image: example.com/app:latest}]}\n")},
	{id: "image.placeholder", kind: "service", check: func(kind string, _ map[string]any) error {
		overlay := "template:\n  containers:\n  - {name: app, image: example.com/app:latest, ports: [{containerPort: 8080}]}\n  - {name: sidecar, image: " + ImagePlaceholder + "}\n"
		_, err := Render(nil, []byte(overlay), RenderOptions{ServiceName: "app", ResourceType: kind, ImageContainer: "app"})
		return err
	}},
	{id: "gpu.limit", kind: "service", change: func(b map[string]any) { setLimits(b, "service", map[string]any{"nvidia.com/gpu": "2"}) }},
	{id: "gpu.accelerator", kind: "service", change: func(b map[string]any) {
		l4GPU(b, "service")
		TaskTemplate(b, "service")["nodeSelector"] = map[string]any{"accelerator": "nvidia-t4"}
	}},
	{id: "gpu.l4-minimum", kind: "service", change: func(b map[string]any) {
		l4GPU(b, "service")
		setLimits(b, "service", map[string]any{"cpu": "1", "memory": "4Gi", "nvidia.com/gpu": "1"})
	}},
	{id: "gpu.gen1", kind: "service", change: func(b map[string]any) {
		l4GPU(b, "service")
		TaskTemplate(b, "service")["executionEnvironment"] = "EXECUTION_ENVIRONMENT_GEN1"
	}},
	{id: "gpu.cpu-idle", kind: "service", change: func(b map[string]any) {
		l4GPU(b, "service")
		firstContainer(b, "service")["resources"].(map[string]any)["cpuIdle"] = true
	}},
	{id: "gpu.job-zonal", kind: "job", change: func(b map[string]any) { l4GPU(b, "job") }},
	{id: "gpu.job-timeout", kind: "job", change: func(b map[string]any) {
		l4GPU(b, "job")
		TaskTemplate(b, "job")["gpuZonalRedundancyDisabled"] = true
		TaskTemplate(b, "job")["timeout"] = "7200s"
	}},
	{id: "gpu.count", kind: "service", change: func(b map[string]any) {
		l4GPU(b, "service")
		firstContainer(b, "service")["ports"] = []any{map[string]any{"containerPort": 8080}}
		addContainer(b, "service", "sidecar", map[string]any{"resources": firstContainer(b, "service")["resources"]})
	}},
	{id: "gpu.accelerator-container", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["nodeSelector"] = map[string]any{"accelerator": "nvidia-l4"}
		setLimits(b, "service", map[string]any{"cpu": "4", "memory": "16Gi"})
	}},
	{id: "gpu.zonal-without-gpu", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["gpuZonalRedundancyDisabled"] = true }},
	{id: "dependency.unknown", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["dependsOn"] = []any{"missing"} }},
	{id: "dependency.cycle", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["dependsOn"] = []any{"app"} }},
	{id: "container.count", kind: "service", change: func(b map[string]any) {
		firstContainer(b, "service")["ports"] = []any{map[string]any{"containerPort": 8080}}
		for i := 0; i < 10; i++ {
			addContainer(b, "service", fmt.Sprintf("c%d", i), nil)
		}
	}},
	{id: "runtime.gen1", kind: "job", change: func(b map[string]any) { TaskTemplate(b, "job")["executionEnvironment"] = "EXECUTION_ENVIRONMENT_GEN1" }},
	{id: "identity.service-account", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["serviceAccount"] = "bad@@example" }},
	{id: "encryption.key", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["encryptionKey"] = "bad" }},
	{id: "encryption.requires-key", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["encryptionKeyRevocationAction"] = "SHUTDOWN"
	}},
	{id: "encryption.shutdown", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["encryptionKeyShutdownDuration"] = "1s" }},
	{id: "encryption.shutdown", kind: "service", change: func(b map[string]any) {
		TaskTemplate(b, "service")["encryptionKey"] = "projects/p/locations/us-west1/keyRings/r/cryptoKeys/k"
		TaskTemplate(b, "service")["encryptionKeyRevocationAction"] = "SHUTDOWN"
		TaskTemplate(b, "service")["encryptionKeyShutdownDuration"] = "5400s"
	}},
	{id: "env.count", kind: "service", change: func(b map[string]any) {
		env := []any{}
		for i := 0; i < 1001; i++ {
			env = append(env, map[string]any{"name": fmt.Sprintf("V%d", i)})
		}
		firstContainer(b, "service")["env"] = env
	}},
	{id: "env.count", kind: "service", change: func(b map[string]any) { fillContainers(b, "env", 600) }},
	{id: "container.args-count", kind: "service", change: func(b map[string]any) { fillContainers(b, "args", 600) }},
	{id: "container.args-count", kind: "service", change: func(b map[string]any) {
		args := []any{}
		for i := 0; i < 1001; i++ {
			args = append(args, "a")
		}
		firstContainer(b, "service")["args"] = args
	}},
	{id: "env.nul", kind: "service", change: func(b map[string]any) { setEnv(b, "service", map[string]any{"name": "X", "value": "a\x00b"}) }},
	{id: "env.reserved-job", kind: "job", change: func(b map[string]any) { setEnv(b, "job", map[string]any{"name": "CLOUD_RUN_JOB"}) }},
	{id: "container.working-dir", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["workingDir"] = "relative" }},
	{id: "container.nul", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["command"] = []any{"a\x00"} }},
	{id: "container.empty-command", kind: "service", change: func(b map[string]any) { firstContainer(b, "service")["command"] = []any{""} }},
	{id: "template.required", kind: "job", check: func(kind string, _ map[string]any) error {
		_, err := ContainerList(map[string]any{"template": map[string]any{}}, kind)
		return err
	}},
	{id: "template.containers", kind: "service", change: func(b map[string]any) { TaskTemplate(b, "service")["containers"] = []any{} }},
	{id: "image.platform", kind: "service", check: imageConfig(`{"os":"linux","architecture":"arm64"}`)},
	{id: "image.entrypoint", kind: "service", check: imageConfig(`{"os":"linux","architecture":"amd64","config":{}}`)},
	{id: "image.nul", kind: "service", check: imageConfig(`{"os":"linux","architecture":"amd64","config":{"Entrypoint":["/app\u0000"]}}`)},
	{id: "identity.revision", kind: "service", check: identity, change: func(b map[string]any) { TaskTemplate(b, "service")["revision"] = "other-rev" }},
	{id: "identity.execution-token", kind: "job", check: identity, change: func(b map[string]any) {
		b["startExecutionToken"] = strings.Repeat("t", 60)
	}},
}

func TestRuleIDs(t *testing.T) {
	for _, tc := range ruleCases {
		t.Run(tc.id, func(t *testing.T) {
			body := validBody(tc.kind)
			require.NoError(t, Validate(tc.kind, body), "the base fixture must be valid")
			if tc.change != nil {
				tc.change(body)
			}
			check := tc.check
			if check == nil {
				check = Validate
			}
			err := check(tc.kind, body)
			rule, ok := errors.AsType[*RuleError](err)
			require.True(t, ok, "expected a rule error, got %v", err)
			require.Equal(t, tc.id, rule.ID, err.Error())
		})
	}
}

// TestEveryRuleHasARejectingCase keeps the rule table and the rule sources in lockstep.
// Rule helpers that take an ID as their first argument must be added to the pattern.
func TestEveryRuleHasARejectingCase(t *testing.T) {
	pattern := regexp.MustCompile(`\b(?:ruleErr|bound|durationBound|oneOf)\("([a-z0-9.-]+)"`)
	files, err := fs.Glob(sources, "*.go")
	require.NoError(t, err)
	source := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		text, err := sources.ReadFile(name)
		require.NoError(t, err)
		for _, match := range pattern.FindAllStringSubmatch(string(text), -1) {
			source[match[1]] = true
		}
	}
	tested := map[string]bool{}
	for _, tc := range ruleCases {
		tested[tc.id] = true
	}
	require.Equal(t, slices.Sorted(maps.Keys(source)), slices.Sorted(maps.Keys(tested)))
}

func TestCompleteConfigurations(t *testing.T) {
	for _, kind := range []string{"service", "job", "worker"} {
		t.Run(kind, func(t *testing.T) {
			body := validBody(kind)
			tmpl := TaskTemplate(body, kind)
			c := firstContainer(body, kind)
			tmpl["volumes"] = []any{
				map[string]any{"name": "secret", "secret": map[string]any{"secret": "projects/project/secrets/password", "items": []any{map[string]any{"path": "password", "version": "7", "mode": 256}}, "defaultMode": 292}},
				map[string]any{"name": "cloudsql", "cloudSqlInstance": map[string]any{"instances": []any{"p:us-west1:db"}}},
				map[string]any{"name": "memory", "emptyDir": map[string]any{"medium": "MEMORY", "sizeLimit": "512Mi"}},
				map[string]any{"name": "disk", "emptyDir": map[string]any{"sizeLimit": "1Gi"}},
				map[string]any{"name": "gcs", "gcs": map[string]any{"bucket": "bucket", "readOnly": false, "mountOptions": []any{"implicit-dirs"}}},
				map[string]any{"name": "nfs", "nfs": map[string]any{"server": "10.0.0.1", "path": "/export", "readOnly": true}},
			}
			c["volumeMounts"] = []any{map[string]any{"name": "cloudsql", "mountPath": "/cloudsql"}, map[string]any{"name": "secret", "mountPath": "/secrets"}, map[string]any{"name": "disk", "mountPath": "/data"}}
			c["command"] = []any{"/app/server"}
			c["args"] = []any{"--flag"}
			c["workingDir"] = "/app"
			c["startupProbe"] = map[string]any{"tcpSocket": map[string]any{"port": 8080}, "initialDelaySeconds": 0, "periodSeconds": 1, "timeoutSeconds": 1, "failureThreshold": 2}
			if kind != "job" {
				c["livenessProbe"] = map[string]any{"grpc": map[string]any{"port": 8081, "service": "health"}}
			}
			if kind == "service" {
				c["readinessProbe"] = map[string]any{"httpGet": map[string]any{"port": 8080, "path": "/health", "httpHeaders": []any{map[string]any{"name": "X-Probe", "value": "ready"}}}}
			}
			tmpl["serviceAccount"] = "runtime@project.iam.gserviceaccount.com"
			tmpl["vpcAccess"] = map[string]any{"networkInterfaces": []any{map[string]any{"subnetwork": "subnet", "tags": []any{"app"}}}, "egress": "ALL_TRAFFIC"}
			tmpl["encryptionKey"] = "projects/p/locations/us-west1/keyRings/r/cryptoKeys/k"
			tmpl["nodeSelector"] = map[string]any{"accelerator": "nvidia-l4"}
			c["resources"] = map[string]any{"cpuIdle": false, "limits": map[string]any{"cpu": "6", "memory": "16Gi", "nvidia.com/gpu": "1"}}
			body["binaryAuthorization"] = map[string]any{"useDefault": true, "breakglassJustification": "test"}
			if kind == "service" {
				body["traffic"] = []any{map[string]any{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST", "percent": 100}}
				body["invokerIamDisabled"] = false
				body["iapEnabled"] = false
				body["customAudiences"] = []any{"https://example.com"}
				tmpl["timeout"] = "300s"
				tmpl["sessionAffinity"] = true
				tmpl["scaling"] = map[string]any{"minInstanceCount": 1}
				c["ports"] = []any{map[string]any{"name": "h2c", "containerPort": 8080}}
			}
			if kind == "job" {
				tmpl["maxRetries"] = 0
				tmpl["timeout"] = "3600s"
				tmpl["gpuZonalRedundancyDisabled"] = true
			}
			if kind == "worker" {
				body["scaling"] = map[string]any{"manualInstanceCount": 0}
			}
			require.NoError(t, Validate(kind, body))
			input, err := json.Marshal(body)
			require.NoError(t, err)
			output, err := Render(nil, input, RenderOptions{ServiceName: "app", ResourceType: kind})
			require.NoError(t, err)
			require.JSONEq(t, string(input), string(output))
		})
	}
}

// TestAcceptedBoundaries covers limit values the build accepts; they need cloud references, so conformance skips them.
func TestAcceptedBoundaries(t *testing.T) {
	cases := map[string]func(map[string]any){
		"manual scaling zeros": func(b map[string]any) {
			b["scaling"] = map[string]any{"minInstanceCount": 0, "maxInstanceCount": 0, "scalingMode": "MANUAL", "manualInstanceCount": 0}
		},
		"whole-hour key shutdown": func(b map[string]any) {
			TaskTemplate(b, "service")["encryptionKey"] = "projects/p/locations/us-west1/keyRings/r/cryptoKeys/k"
			TaskTemplate(b, "service")["encryptionKeyRevocationAction"] = "SHUTDOWN"
			TaskTemplate(b, "service")["encryptionKeyShutdownDuration"] = "3600s"
		},
		"default period with timeout": func(b map[string]any) {
			firstContainer(b, "service")["startupProbe"] = map[string]any{"grpc": map[string]any{}, "periodSeconds": 0, "timeoutSeconds": 5}
		},
		"startup budget at limit": func(b map[string]any) {
			firstContainer(b, "service")["startupProbe"] = map[string]any{"httpGet": map[string]any{}, "periodSeconds": 200, "failureThreshold": 3}
		},
		"GPU instance sidecar startup budget": func(b map[string]any) {
			l4GPU(b, "service")
			firstContainer(b, "service")["ports"] = []any{map[string]any{"containerPort": 8080}}
			addContainer(b, "service", "sidecar", map[string]any{"startupProbe": map[string]any{"tcpSocket": map[string]any{"port": 9090}, "periodSeconds": 120, "failureThreshold": 10}})
		},
	}
	for name, change := range cases {
		body := validBody("service")
		change(body)
		require.NoError(t, Validate("service", body), name)
	}
	require.Empty(t, WritableFields("unknown"))
	_, err := ResourcePath("unknown")
	require.Error(t, err)
}

func TestContainerSourceCodeReplacesImage(t *testing.T) {
	body := validBody("service")
	tmpl := TaskTemplate(body, "service")
	c := firstContainer(body, "service")
	c["dependsOn"] = []any{"sidecar"}
	c["ports"] = []any{map[string]any{"containerPort": 8080}}
	tmpl["containers"] = append(arrayAt(tmpl, "containers"), map[string]any{"name": "sidecar", "image": "image"})
	require.NoError(t, Validate("service", body))
	delete(c, "image")
	c["sourceCode"] = map[string]any{"cloudStorageSource": map[string]any{"bucket": "b", "object": "o", "generation": "3"}}
	require.NoError(t, Validate("service", body))
}

func TestComputeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		cpu, memory string
		good        bool
	}{{"4", "2Gi", true}, {"8", "4Gi", true}, {"0.5", "512Mi", true}, {"0.1", "128Mi", true}, {"0.4", "1Gi", false}} {
		body := validBody("service")
		tmpl := TaskTemplate(body, "service")
		tmpl["executionEnvironment"] = "EXECUTION_ENVIRONMENT_GEN1"
		tmpl["maxInstanceRequestConcurrency"] = 1
		firstContainer(body, "service")["resources"] = map[string]any{"cpuIdle": true, "limits": map[string]any{"cpu": tc.cpu, "memory": tc.memory}}
		err := Validate("service", body)
		if tc.good {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

// kindBody is a resource body with its resource type.
type kindBody struct {
	kind string
	body map[string]any
}

// acceptedBodies lists configurations the build and Cloud Run must both accept.
func acceptedBodies() map[string]kindBody {
	sidecar := validBody("service")
	TaskTemplate(sidecar, "service")["executionEnvironment"] = "EXECUTION_ENVIRONMENT_GEN1"
	firstContainer(sidecar, "service")["ports"] = []any{map[string]any{"containerPort": 8080}}
	addContainer(sidecar, "service", "sidecar", nil)

	defaults := validBody("worker")
	firstContainer(defaults, "worker")["startupProbe"] = map[string]any{"tcpSocket": map[string]any{"port": 8080}, "periodSeconds": 0, "failureThreshold": 0}

	startup := validBody("service")
	firstContainer(startup, "service")["startupProbe"] = map[string]any{"httpGet": map[string]any{}, "initialDelaySeconds": 240, "periodSeconds": 240, "timeoutSeconds": 240, "failureThreshold": 2}

	parallel := validBody("job")
	objectAt(parallel, "template")["parallelism"] = 5

	jobSidecar := validBody("job")
	firstContainer(jobSidecar, "job")["resources"] = map[string]any{"limits": map[string]any{"cpu": "1", "memory": "512Mi"}}
	addContainer(jobSidecar, "job", "sidecar", map[string]any{"resources": map[string]any{"limits": map[string]any{"cpu": "0.5", "memory": "512Mi"}}})

	manual := validBody("worker")
	manual["scaling"] = map[string]any{"manualInstanceCount": 1}

	serviceSidecar := validBody("service")
	TaskTemplate(serviceSidecar, "service")["executionEnvironment"] = "EXECUTION_ENVIRONMENT_GEN2"
	firstContainer(serviceSidecar, "service")["ports"] = []any{map[string]any{"containerPort": 8080}}
	firstContainer(serviceSidecar, "service")["resources"] = map[string]any{"cpuIdle": false, "limits": map[string]any{"cpu": "1", "memory": "512Mi"}}
	addContainer(serviceSidecar, "service", "sidecar", map[string]any{"resources": map[string]any{"cpuIdle": false, "limits": map[string]any{"cpu": "0.5", "memory": "512Mi"}}})

	unnamed := validBody("service")
	firstContainer(unnamed, "service")["ports"] = []any{map[string]any{"containerPort": 8080}}
	TaskTemplate(unnamed, "service")["containers"] = append(arrayAt(TaskTemplate(unnamed, "service"), "containers"),
		map[string]any{"image": "example.com/sidecar-a:latest"}, map[string]any{"image": "example.com/sidecar-b:latest"})

	fraction := validBody("service")
	TaskTemplate(fraction, "service")["executionEnvironment"] = "EXECUTION_ENVIRONMENT_GEN1"
	TaskTemplate(fraction, "service")["maxInstanceRequestConcurrency"] = 1
	firstContainer(fraction, "service")["resources"] = map[string]any{"cpuIdle": true, "limits": map[string]any{"cpu": "0.0801", "memory": "256Mi"}}

	dotted := validBody("service")
	firstContainer(dotted, "service")["name"] = "app.v2"
	TaskTemplate(dotted, "service")["volumes"] = []any{map[string]any{"name": "v", "secret": map[string]any{"secret": "s", "items": []any{map[string]any{"path": "a..b", "version": "latest"}}}}}
	firstContainer(dotted, "service")["readinessProbe"] = map[string]any{"httpGet": map[string]any{}, "periodSeconds": 3601}

	return map[string]kindBody{
		"gen1 sidecar":           {"service", sidecar},
		"zero probe defaults":    {"worker", defaults},
		"240s startup probe":     {"service", startup},
		"parallelism over tasks": {"job", parallel},
		"half-vCPU job sidecar":  {"job", jobSidecar},
		"worker manual count":    {"worker", manual},
		"half-vCPU sidecar":      {"service", serviceSidecar},
		"unnamed sidecars":       {"service", unnamed},
		"uneven fractional CPU":  {"service", fraction},
		"dotted names and paths": {"service", dotted},
	}
}

// TestAcceptedBodies checks the bodies TestConformance also sends to Cloud Run.
func TestAcceptedBodies(t *testing.T) {
	for name, c := range acceptedBodies() {
		require.NoError(t, Validate(c.kind, c.body), name)
	}
}

func TestVPCClearing(t *testing.T) {
	body := validBody("service")
	TaskTemplate(body, "service")["vpcAccess"] = map[string]any{"connector": "", "networkInterfaces": []any{}}
	require.NoError(t, Validate("service", body))
}
