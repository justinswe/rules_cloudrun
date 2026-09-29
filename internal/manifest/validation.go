package manifest

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/justinswe/std/errors"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/reflect/protoreflect"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
)

var (
	durationPattern      = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,9})?s$`)
	secretPattern        = regexp.MustCompile(`^(?:projects/[a-zA-Z0-9:_-]+/secrets/)?[a-zA-Z0-9_-]+$`)
	versionPattern       = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	containerNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	// Cloud Run container names follow RFC 1123 and may contain periods, unlike volume and revision names.
	dottedNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	headerNamePattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

// ReservedMetadataPrefixes are label and annotation namespaces that Cloud Run and Knative own.
var ReservedMetadataPrefixes = []string{"run.googleapis.com/", "cloud.googleapis.com/", "serving.knative.dev/", "autoscaling.knative.dev/"}

// RuleError is a validation failure identified by a stable rule ID.
type RuleError struct {
	ID      string
	Message string
}

// Error reports the message followed by the rule ID.
func (e *RuleError) Error() string { return e.Message + " [" + e.ID + "]" }

// ruleErr builds a RuleError; configuration mistakes go through it so tests can prove each ID is reachable.
func ruleErr(id, format string, args ...any) error {
	return &RuleError{ID: id, Message: fmt.Sprintf(format, args...)}
}

// objectAt returns m[key] as an object, or nil.
func objectAt(m map[string]any, key string) map[string]any {
	value, _ := m[key].(map[string]any)
	return value
}

// arrayAt returns m[key] as an array, or nil.
func arrayAt(m map[string]any, key string) []any { value, _ := m[key].([]any); return value }

// stringAt returns m[key] as a string, or "".
func stringAt(m map[string]any, key string) string { value, _ := m[key].(string); return value }

// TaskTemplate returns the template that holds containers and volumes: template.template for jobs, template otherwise.
func TaskTemplate(body map[string]any, kind string) map[string]any {
	template := objectAt(body, "template")
	if kind == "job" {
		return objectAt(template, "template")
	}
	return template
}

// validateResource applies message rules, then cross-field rules over containers, volumes, and runtime settings.
func validateResource(kind string, body map[string]any) error {
	if err := walkRules(body, roots[kind], kind, kind); err != nil {
		return err
	}
	containers, err := ContainerList(body, kind)
	if err != nil {
		return err
	}
	template := TaskTemplate(body, kind)
	volumes, err := volumesByName(template)
	if err != nil {
		return err
	}
	if err := validateContainers(kind, containers, template, volumes); err != nil {
		return err
	}
	return validateRuntime(kind, template, containers)
}

// volumesByName indexes the template's volumes, rejecting duplicate names.
func volumesByName(template map[string]any) (map[string]map[string]any, error) {
	volumes := map[string]map[string]any{}
	for _, raw := range arrayAt(template, "volumes") {
		volume := raw.(map[string]any)
		name := stringAt(volume, "name")
		if _, exists := volumes[name]; exists {
			return nil, ruleErr("volume.duplicate", "duplicate volume %q", name)
		}
		volumes[name] = volume
	}
	return volumes, nil
}

// validateContainers checks container names, serving ports, dependencies, and each container's compute, env, and mounts.
func validateContainers(kind string, containers []any, template map[string]any, volumes map[string]map[string]any) error {
	names := map[string]bool{}
	dependencies := map[string][]any{}
	servingPorts := 0
	var totalMilli int64
	for i, raw := range containers {
		c := raw.(map[string]any)
		name := stringAt(c, "name")
		if names[name] {
			return ruleErr("container.duplicate", "duplicate container %q", name)
		}
		if name == "" {
			// Cloud Run names unnamed containers; they cannot be dependency targets.
			name = fmt.Sprintf("#%d", i)
		} else {
			names[name] = true
		}
		dependencies[name] = arrayAt(c, "dependsOn")
		if len(arrayAt(c, "ports")) > 0 {
			servingPorts++
		}
		milli, err := validateContainer(kind, c, template, volumes)
		if err != nil {
			return err
		}
		totalMilli += milli
	}
	if err := validateFractionalCPU(kind, totalMilli, containers, template); err != nil {
		return err
	}
	if servingPorts > 1 {
		return ruleErr("container.serving-ports", "only one container may expose a serving port")
	}
	if kind == "service" && len(containers) > 1 && servingPorts == 0 {
		return ruleErr("container.ingress", "services with sidecars need exactly one container with ports, the ingress container")
	}
	return validateDependencies(dependencies, names)
}

// validateContainer checks one container's compute, environment names, and volume mounts, returning its CPU in millicores.
func validateContainer(kind string, c, template map[string]any, volumes map[string]map[string]any) (int64, error) {
	milli, err := validateCompute(kind, c, template)
	if err != nil {
		return 0, err
	}
	envNames := map[string]bool{}
	for _, raw := range arrayAt(c, "env") {
		name := stringAt(raw.(map[string]any), "name")
		if envNames[name] {
			return 0, ruleErr("env.duplicate", "duplicate environment variable %q", name)
		}
		envNames[name] = true
	}
	return milli, validateMounts(c, volumes)
}

// validateMounts checks that each mount names a known volume, uses a unique path, and suits the volume type.
func validateMounts(c map[string]any, volumes map[string]map[string]any) error {
	mounts := map[string]bool{}
	for _, raw := range arrayAt(c, "volumeMounts") {
		mount := raw.(map[string]any)
		volume, ok := volumes[stringAt(mount, "name")]
		if !ok {
			return ruleErr("mount.unknown-volume", "volumeMount references unknown volume %q", stringAt(mount, "name"))
		}
		mountPath := stringAt(mount, "mountPath")
		if mounts[path.Clean(mountPath)] {
			return ruleErr("mount.duplicate-path", "duplicate volume mount path %q", path.Clean(mountPath))
		}
		mounts[path.Clean(mountPath)] = true
		if !strings.HasPrefix(mountPath, "/") {
			return ruleErr("mount.relative-path", "volume mount path %q must be absolute", mountPath)
		}
		if volume["secret"] != nil && stringAt(mount, "subPath") != "" {
			return ruleErr("mount.secret-subpath", "secret volumes do not support subPath")
		}
	}
	return nil
}

// walkRules applies message-specific rules to every object in the body.
func walkRules(value any, message protoreflect.MessageDescriptor, kind, p string) error {
	m := value.(map[string]any)
	if err := validateObject(string(message.FullName()), m, kind, p); err != nil {
		return errors.Wrap(err, p)
	}
	for _, key := range slices.Sorted(maps.Keys(m)) {
		field := message.Fields().ByJSONName(key)
		child := field.Message()
		if child == nil || field.IsMap() || child.ParentFile().Package() == "google.protobuf" {
			continue
		}
		if !field.IsList() {
			if err := walkRules(m[key], child, kind, p+"."+key); err != nil {
				return err
			}
			continue
		}
		for i, item := range m[key].([]any) {
			if err := walkRules(item, child, kind, formatIndex(p+"."+key, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateObject applies the rules for one API message.
func validateObject(schema string, m map[string]any, kind, p string) error {
	if err := validateMetadata(m); err != nil {
		return err
	}
	switch schema {
	case "google.cloud.run.v2.Service", "google.cloud.run.v2.Job", "google.cloud.run.v2.WorkerPool":
		return validateRoot(m, kind)
	case "google.cloud.run.v2.ServiceScaling", "google.cloud.run.v2.RevisionScaling", "google.cloud.run.v2.WorkerPoolScaling":
		return validateScaling(m, schema)
	case "google.cloud.run.v2.Service.MultiRegionSettings":
		return validateRegions(m)
	case "google.cloud.run.v2.RevisionTemplate", "google.cloud.run.v2.TaskTemplate", "google.cloud.run.v2.WorkerPoolRevisionTemplate":
		return validateTemplate(m, kind)
	case "google.cloud.run.v2.ExecutionTemplate":
		if err := bound("execution.task-count", m, "taskCount", 0, 10000); err != nil {
			return err
		}
		return bound("execution.parallelism", m, "parallelism", 0, 10000)
	case "google.cloud.run.v2.Container":
		return validateContainerFields(m, kind)
	case "google.cloud.run.v2.EnvVar":
		return validateEnvVar(m, kind)
	case "google.cloud.run.v2.ResourceRequirements":
		if kind == "service" && m["cpuIdle"] == nil {
			return ruleErr("compute.cpu-idle-explicit", "services that set resources must set cpuIdle explicitly; when omitted, Cloud Run allocates CPU for the whole instance lifetime")
		}
	case "google.cloud.run.v2.EnvVarSource":
		if m["secretKeyRef"] == nil {
			return ruleErr("env.secret-required", "valueSource.secretKeyRef is required")
		}
	case "google.cloud.run.v2.SecretKeySelector", "google.cloud.run.v2.SecretVolumeSource":
		return validateSecret(m)
	case "google.cloud.run.v2.VersionToPath":
		return validateSecretItem(m)
	case "google.cloud.run.v2.Probe":
		return validateProbe(m, p)
	case "google.cloud.run.v2.HTTPGetAction", "google.cloud.run.v2.TCPSocketAction", "google.cloud.run.v2.GRPCAction":
		return bound("probe.port", m, "port", 0, 65535)
	case "google.cloud.run.v2.HTTPHeader":
		if !headerNamePattern.MatchString(stringAt(m, "name")) || strings.ContainsAny(stringAt(m, "value"), "\r\n") {
			return ruleErr("probe.header", "HTTP probe header names use letters, digits, and '-', and values cannot contain line breaks")
		}
	case "google.cloud.run.v2.ContainerPort":
		if name := stringAt(m, "name"); name != "" && name != "http1" && name != "h2c" {
			return ruleErr("port.name", "port name must be http1 or h2c")
		}
		// An omitted containerPort is sent as 0, which Cloud Run rejects.
		if port, _ := number(m["containerPort"]); port < 1 || port > 65535 {
			return ruleErr("port.range", "containerPort must be between 1 and 65535")
		}
	case "google.cloud.run.v2.VpcAccess":
		return validateVPCAccess(m)
	case "google.cloud.run.v2.VpcAccess.NetworkInterface":
		if stringAt(m, "network") == "" && stringAt(m, "subnetwork") == "" {
			return ruleErr("vpc.network", "network or subnetwork is required")
		}
	case "google.cloud.run.v2.Volume":
		return validateVolume(m)
	case "google.cloud.run.v2.EmptyDirVolumeSource":
		return validateEmptyDir(m)
	case "google.cloud.run.v2.VolumeMount":
		return validateMountPath(stringAt(m, "mountPath"))
	case "google.cloud.run.v2.SourceCode":
		return oneOf("source.required", m, "cloudStorageSource")
	}
	return nil
}

// validateMetadata rejects labels and annotations in namespaces Cloud Run owns.
func validateMetadata(m map[string]any) error {
	for _, key := range []string{"labels", "annotations"} {
		for name := range objectAt(m, key) {
			if slices.ContainsFunc(ReservedMetadataPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) }) {
				return ruleErr("metadata.reserved", "reserved %s namespace %q", key, name)
			}
		}
	}
	return nil
}

// validateRoot checks the resource name, deployer-owned and launch-stage fields, and traffic or instance splits.
func validateRoot(m map[string]any, kind string) error {
	if _, ok := m["etag"]; ok {
		return ruleErr("resource.etag", "etag is taken from the live resource at deploy time; remove it from the configuration")
	}
	if stage := stringAt(m, "launchStage"); stage != "" && !slices.Contains([]string{"LAUNCH_STAGE_UNSPECIFIED", "ALPHA", "BETA", "GA"}, stage) {
		return ruleErr("launch-stage", "launchStage must be ALPHA, BETA, or GA")
	}
	if name := stringAt(m, "name"); name != "" {
		parts := strings.Split(name, "/")
		collection, _ := ResourcePath(kind)
		if len(parts) != 6 || parts[0] != "projects" || parts[1] == "" || parts[2] != "locations" || parts[3] == "" || parts[4] != collection {
			return ruleErr("resource.name", "name must be a fully qualified resource name")
		}
		if err := ValidateName(parts[5]); err != nil {
			return err
		}
	}
	if err := validateSplits(arrayAt(m, "traffic"), "TRAFFIC_TARGET_ALLOCATION_TYPE_"); err != nil {
		return err
	}
	return validateSplits(arrayAt(m, "instanceSplits"), "INSTANCE_SPLIT_ALLOCATION_TYPE_")
}

// validateScaling bounds instance counts and requires MANUAL mode for a service's manual instance count.
func validateScaling(m map[string]any, schema string) error {
	for _, key := range []string{"minInstanceCount", "maxInstanceCount", "manualInstanceCount"} {
		if err := bound("scaling.range", m, key, 0, 2147483647); err != nil {
			return err
		}
	}
	minCount, _ := number(m["minInstanceCount"])
	maxCount, _ := number(m["maxInstanceCount"])
	if maxCount > 0 && minCount > maxCount {
		return ruleErr("scaling.order", "minInstanceCount must not exceed maxInstanceCount")
	}
	if schema == "google.cloud.run.v2.ServiceScaling" && m["manualInstanceCount"] != nil && stringAt(m, "scalingMode") != "MANUAL" {
		return ruleErr("scaling.manual-mode", "manualInstanceCount requires scalingMode: MANUAL")
	}
	return nil
}

// validateRegions requires at least two unique regions.
func validateRegions(m map[string]any) error {
	regions := arrayAt(m, "regions")
	if len(regions) < 2 {
		return ruleErr("regions.count", "multi-region services need at least two regions")
	}
	seen := map[string]bool{}
	for _, raw := range regions {
		region := raw.(string)
		if region == "" || seen[region] {
			return ruleErr("regions.unique", "regions must be nonempty and unique")
		}
		seen[region] = true
	}
	return nil
}

// validateTemplate bounds concurrency, retries, and the request, task, or shutdown durations.
func validateTemplate(m map[string]any, kind string) error {
	if err := bound("template.concurrency", m, "maxInstanceRequestConcurrency", 0, 1000); err != nil {
		return err
	}
	if err := bound("template.max-retries", m, "maxRetries", 0, 10); err != nil {
		return err
	}
	maxTimeout := time.Hour
	if kind == "job" {
		maxTimeout = 168 * time.Hour
	}
	if err := durationBound("template.timeout", m, "timeout", time.Second, maxTimeout); err != nil {
		return err
	}
	return durationBound("encryption.shutdown", m, "encryptionKeyShutdownDuration", time.Hour, 0)
}

// validateContainerFields checks a container's name, image, and service-only fields.
func validateContainerFields(m map[string]any, kind string) error {
	if name := stringAt(m, "name"); name != "" && (len(name) > 63 || !dottedNamePattern.MatchString(name)) {
		return ruleErr("container.name", "container names use lowercase letters, digits, hyphens, and periods, starting and ending with a letter or digit")
	}
	if image := stringAt(m, "image"); image != "" {
		if err := ValidateImage(image); err != nil {
			return ruleErr("container.image", "%v", err)
		}
	} else if m["sourceCode"] == nil {
		return ruleErr("container.image-required", "container image is required")
	}
	ports := arrayAt(m, "ports")
	if len(ports) > 1 {
		return ruleErr("container.ports", "only one container port is supported")
	}
	if kind != "service" && (len(ports) > 0 || stringAt(m, "baseImageUri") != "" || m["sourceCode"] != nil) {
		return ruleErr("container.service-only", "ports, baseImageUri, and sourceCode are service-only")
	}
	return nil
}

// validateEnvVar checks an environment variable's name and value size.
func validateEnvVar(m map[string]any, kind string) error {
	name := stringAt(m, "name")
	if name == "" || strings.Contains(name, "=") || len(name) > 32768 {
		return ruleErr("env.name", "invalid environment variable name")
	}
	if err := validateReservedEnv(kind, name); err != nil {
		return err
	}
	if len(stringAt(m, "value")) > 32768 {
		return ruleErr("env.value-size", "environment variable value exceeds 32768 bytes")
	}
	return nil
}

// validateSecret checks a Secret Manager reference, version, and default file mode.
func validateSecret(m map[string]any) error {
	if !secretPattern.MatchString(stringAt(m, "secret")) {
		return ruleErr("secret.reference", "invalid Secret Manager reference")
	}
	if version, ok := m["version"]; ok && !versionPattern.MatchString(version.(string)) {
		return ruleErr("secret.version", "invalid secret version")
	}
	return bound("secret.mode", m, "defaultMode", 0, 511)
}

// validateSecretItem checks one secret volume item's path, version, and file mode.
func validateSecretItem(m map[string]any) error {
	p := stringAt(m, "path")
	if p == "" || path.IsAbs(p) || slices.Contains(strings.Split(p, "/"), "..") {
		return ruleErr("secret-item.path", "secret item path must be a relative path without '..' segments")
	}
	if !versionPattern.MatchString(stringAt(m, "version")) {
		return ruleErr("secret-item.version", "secret item version is required: a version number or alias such as latest")
	}
	return bound("secret-item.mode", m, "mode", 0, 511)
}

// validateProbe checks a probe's type, failure threshold, and timeout against its period.
func validateProbe(m map[string]any, p string) error {
	if err := oneOf("probe.type", m, "httpGet", "tcpSocket", "grpc"); err != nil {
		return err
	}
	if !strings.HasSuffix(p, "startupProbe") && m["tcpSocket"] != nil {
		return ruleErr("probe.tcp-startup-only", "TCP probes are only supported for startup")
	}
	if err := bound("probe.failure-threshold", m, "failureThreshold", 0, 2147483647); err != nil {
		return err
	}
	if probeSetting(m, "timeoutSeconds", 1) > probeSetting(m, "periodSeconds", 10) {
		return ruleErr("probe.timeout-period", "probe timeoutSeconds must not exceed periodSeconds")
	}
	return nil
}

// validateVPCAccess allows one connection mode and at most one network interface.
func validateVPCAccess(m map[string]any) error {
	interfaces := arrayAt(m, "networkInterfaces")
	if stringAt(m, "connector") != "" && len(interfaces) > 0 {
		return ruleErr("vpc.mode", "exactly one VPC connection mode may be set")
	}
	if len(interfaces) > 1 {
		return ruleErr("vpc.interfaces", "at most one VPC network interface is supported")
	}
	return nil
}

// validateVolume checks a volume's name and that it has a source.
func validateVolume(m map[string]any) error {
	name := stringAt(m, "name")
	if len(name) > 63 || !containerNamePattern.MatchString(name) {
		return ruleErr("volume.name", "invalid volume name")
	}
	if err := oneOf("volume.source", m, "secret", "cloudSqlInstance", "emptyDir", "nfs", "gcs"); err != nil {
		return err
	}
	if (m["cloudSqlInstance"] != nil) != (name == "cloudsql") {
		return ruleErr("volume.cloudsql-name", "a Cloud SQL volume must be named cloudsql, and only a Cloud SQL volume may use that name")
	}
	return nil
}

// validateEmptyDir requires a positive size limit when one is set.
func validateEmptyDir(m map[string]any) error {
	size := stringAt(m, "sizeLimit")
	if size == "" {
		return nil
	}
	if q, err := apiresource.ParseQuantity(size); err != nil || q.Sign() <= 0 {
		return ruleErr("volume.size-quantity", "sizeLimit must be a positive resource quantity")
	}
	return nil
}

// validateMountPath rejects colons and reserved filesystems in a mount path.
func validateMountPath(mount string) error {
	if strings.Contains(mount, ":") {
		return ruleErr("mount.colon", "mountPath cannot contain ':'")
	}
	clean := path.Clean(mount)
	for _, reserved := range []string{"/dev", "/proc", "/sys"} {
		if clean == reserved || strings.HasPrefix(clean, reserved+"/") {
			return ruleErr("mount.reserved-path", "mountPath uses a reserved filesystem")
		}
	}
	return nil
}

// validateReservedEnv rejects names Cloud Run sets for the given resource kind.
func validateReservedEnv(kind, name string) error {
	if strings.HasPrefix(name, "X_GOOGLE_") {
		return ruleErr("env.reserved-google", "environment variable %q uses the reserved X_GOOGLE_ prefix", name)
	}
	if name == "PORT" {
		return ruleErr("env.reserved-port", "environment variable PORT is set by Cloud Run")
	}
	if kind == "service" && slices.Contains([]string{"K_SERVICE", "K_REVISION", "K_CONFIGURATION"}, name) {
		return ruleErr("env.reserved-service", "environment variable %q is set by Cloud Run for services", name)
	}
	if kind == "job" && slices.Contains([]string{"CLOUD_RUN_JOB", "CLOUD_RUN_EXECUTION", "CLOUD_RUN_TASK_INDEX", "CLOUD_RUN_TASK_ATTEMPT", "CLOUD_RUN_TASK_COUNT"}, name) {
		return ruleErr("env.reserved-job", "environment variable %q is set by Cloud Run for jobs", name)
	}
	return nil
}

// oneOf requires exactly one of keys; the schema already rejects setting more than one.
func oneOf(id string, m map[string]any, keys ...string) error {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return nil
		}
	}
	return ruleErr(id, "exactly one of %s must be set", strings.Join(keys, ", "))
}

// bound checks that a present numeric field lies within [min, max].
func bound(id string, m map[string]any, key string, min, max float64) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	n, _ := number(v)
	if n < min || n > max {
		return ruleErr(id, "%s must be between %s and %s", key, formatNumber(min), formatNumber(max))
	}
	return nil
}

// formatNumber prints a bound without exponent notation.
func formatNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// probeSetting reads a probe field where zero, like absence, selects the API default.
func probeSetting(m map[string]any, key string, fallback float64) float64 {
	if n, _ := number(m[key]); n != 0 {
		return n
	}
	return fallback
}

// durationBound checks that a present duration string lies within [min, max]; a zero max means unbounded.
func durationBound(id string, m map[string]any, key string, min, max time.Duration) error {
	value, ok := m[key]
	if !ok {
		return nil
	}
	text := value.(string)
	if !durationPattern.MatchString(text) {
		return ruleErr("duration.format", "%s must be a duration in seconds, for example 300s", key)
	}
	duration, err := time.ParseDuration(text)
	if err != nil || duration < min || (max > 0 && duration > max) {
		return ruleErr(id, "%s is outside the supported duration range", key)
	}
	return nil
}

// validateSplits checks traffic or instance split percentages, revisions, and tags.
func validateSplits(splits []any, prefix string) error {
	if len(splits) == 0 {
		return nil
	}
	total := float64(0)
	tags := map[string]bool{}
	for _, raw := range splits {
		s := raw.(map[string]any)
		if err := bound("split.percent", s, "percent", 0, 100); err != nil {
			return err
		}
		n, _ := number(s["percent"])
		total += n
		allocation := stringAt(s, "type")
		revision := stringAt(s, "revision")
		if allocation == prefix+"REVISION" && revision == "" {
			return ruleErr("split.revision", "revision allocation requires a revision")
		}
		if allocation == prefix+"LATEST" && revision != "" {
			return ruleErr("split.latest", "latest allocation cannot specify a revision")
		}
		if tag := stringAt(s, "tag"); tag != "" {
			if tags[tag] {
				return ruleErr("split.tag", "traffic tags must be unique")
			}
			tags[tag] = true
		}
	}
	if total != 100 {
		return ruleErr("split.total", "traffic or instance split percentages must sum to 100")
	}
	return nil
}

// validateCompute checks one container's resource limits against Cloud Run's CPU, memory, and GPU combinations, returning its CPU in millicores.
func validateCompute(kind string, c, template map[string]any) (int64, error) {
	limits := objectAt(objectAt(c, "resources"), "limits")
	for key := range limits {
		if key != "cpu" && key != "memory" && key != "nvidia.com/gpu" {
			return 0, ruleErr("compute.limit", "unsupported resource limit %q", key)
		}
	}
	milli, err := cpuMillis(limits)
	if err != nil {
		return 0, err
	}
	accelerator := stringAt(objectAt(template, "nodeSelector"), "accelerator")
	rtx := limits["nvidia.com/gpu"] == "1" && accelerator == "nvidia-rtx-pro-6000"
	if err := validateCPU(milli, rtx); err != nil {
		return 0, err
	}
	mib, err := memoryMiB(limits)
	if err != nil {
		return 0, err
	}
	if err := validateMemory(kind, milli, mib, rtx, template); err != nil {
		return 0, err
	}
	return milli, validateGPU(limits, accelerator, milli, mib)
}

// cpuMillis parses the CPU limit, defaulting to 1 vCPU, in thousandths of a vCPU.
func cpuMillis(limits map[string]any) (int64, error) {
	cpu := apiresource.MustParse("1")
	if text := stringAt(limits, "cpu"); text != "" {
		var err error
		if cpu, err = apiresource.ParseQuantity(text); err != nil {
			return 0, ruleErr("compute.cpu-quantity", "invalid CPU quantity")
		}
	}
	return cpu.MilliValue(), nil
}

// validateCPU checks the CPU limit against the supported sizes.
func validateCPU(milli int64, rtx bool) error {
	if rtx && (milli < 20000 || milli > 30000 || milli%2000 != 0) {
		return ruleErr("compute.rtx-cpu", "RTX PRO 6000 requires 20, 22, 24, 26, 28, or 30 vCPU")
	}
	if milli < 80 || (!rtx && (milli > 8000 || (milli >= 1000 && !slices.Contains([]int64{1000, 2000, 4000, 6000, 8000}, milli)))) {
		return ruleErr("compute.cpu", "CPU must be 0.08-1, 2, 4, 6, or 8 vCPU")
	}
	return nil
}

// memoryMiB parses the memory limit, defaulting to 512Mi, in mebibytes.
func memoryMiB(limits map[string]any) (float64, error) {
	memory := apiresource.MustParse("512Mi")
	if text := stringAt(limits, "memory"); text != "" {
		var err error
		if memory, err = apiresource.ParseQuantity(text); err != nil {
			return 0, ruleErr("compute.memory-quantity", "invalid memory quantity")
		}
	}
	return float64(memory.Value()) / (1 << 20), nil
}

// validateMemory checks memory against the range allowed for the CPU and execution environment.
func validateMemory(kind string, milli int64, mib float64, rtx bool, template map[string]any) error {
	minMem, maxMem := memoryRange(milli, rtx)
	if (template["executionEnvironment"] == "EXECUTION_ENVIRONMENT_GEN2" || kind != "service") && minMem < 512 {
		minMem = 512
	}
	if rtx && mib < minMem {
		return ruleErr("compute.rtx-memory", "RTX PRO 6000 requires at least 80Gi memory")
	}
	if mib < minMem || (maxMem > 0 && mib > maxMem) {
		return ruleErr("compute.memory", "memory must be between %gMi and %gMi for the configured CPU and execution environment", minMem, maxMem)
	}
	return nil
}

// memoryRange returns the memory bounds in MiB for a CPU size; a zero maximum is unbounded.
func memoryRange(milli int64, rtx bool) (float64, float64) {
	switch {
	case rtx:
		// RTX PRO 6000 allows 80Gi up to 4Gi per vCPU.
		return 81920, float64(milli/1000) * 4096
	case milli >= 8000:
		return 4096, 32768
	case milli >= 6000:
		return 4096, 24576
	case milli >= 4000:
		return 2048, 16384
	case milli >= 2000:
		return 128, 8192
	case milli >= 1000:
		return 128, 4096
	case milli >= 500:
		return 128, 1024
	}
	return 128, 512
}

// validateFractionalCPU checks, as separate rules, each requirement for an instance with less than one vCPU in total.
func validateFractionalCPU(kind string, totalMilli int64, containers []any, template map[string]any) error {
	if totalMilli >= 1000 {
		return nil
	}
	if kind != "service" {
		return ruleErr("fractional-cpu.kind", "less than 1 vCPU in total is only supported for services")
	}
	if concurrency, _ := number(template["maxInstanceRequestConcurrency"]); concurrency != 0 && concurrency != 1 {
		return ruleErr("fractional-cpu.concurrency", "less than 1 vCPU in total requires maxInstanceRequestConcurrency 1 (or default)")
	}
	for _, raw := range containers {
		if objectAt(raw.(map[string]any), "resources")["cpuIdle"] != true {
			return ruleErr("fractional-cpu.cpu-idle", "less than 1 vCPU in total requires cpuIdle: true")
		}
	}
	if template["executionEnvironment"] != "EXECUTION_ENVIRONMENT_GEN1" {
		return ruleErr("fractional-cpu.gen1", "less than 1 vCPU in total requires the first generation execution environment")
	}
	return nil
}

// validateGPU checks the GPU limit, accelerator, and the L4 minimum size.
func validateGPU(limits map[string]any, accelerator string, milli int64, mib float64) error {
	gpu, ok := limits["nvidia.com/gpu"]
	if !ok {
		return nil
	}
	if gpu != "1" || accelerator == "" {
		return ruleErr("gpu.limit", "GPU requires limit '1' and a nodeSelector accelerator")
	}
	if accelerator != "nvidia-l4" && accelerator != "nvidia-rtx-pro-6000" {
		return ruleErr("gpu.accelerator", "unsupported GPU accelerator; update the validated runtime policy before adoption")
	}
	if accelerator == "nvidia-l4" && (milli < 4000 || mib < 16384) {
		return ruleErr("gpu.l4-minimum", "L4 GPU requires at least 4 CPU and 16Gi memory")
	}
	return nil
}

// validateDependencies rejects unknown and cyclic container dependencies.
func validateDependencies(deps map[string][]any, names map[string]bool) error {
	state := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return ruleErr("dependency.cycle", "container dependencies contain a cycle")
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, raw := range deps[name] {
			dependency := raw.(string)
			if !names[dependency] {
				return ruleErr("dependency.unknown", "unknown container dependency %q", dependency)
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(deps)) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// WritableFields lists top-level configuration fields for replacement updates.
func WritableFields(kind string) []string {
	root, ok := roots[kind]
	if !ok {
		return nil
	}
	fields := []string{}
	for i := 0; i < root.Fields().Len(); i++ {
		field := root.Fields().Get(i)
		key := field.JSONName()
		if !hasBehavior(field, annotations.FieldBehavior_OUTPUT_ONLY) && key != "name" && key != "etag" {
			fields = append(fields, key)
		}
	}
	slices.Sort(fields)
	return fields
}

// ResourcePath returns the REST collection for a resource type.
func ResourcePath(kind string) (string, error) {
	switch kind {
	case "service":
		return "services", nil
	case "job":
		return "jobs", nil
	case "worker":
		return "workerPools", nil
	default:
		return "", errors.Errorf("unknown resource type %q", kind)
	}
}
