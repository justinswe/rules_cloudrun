package manifest

import (
	"encoding/json"
	"math"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/justinswe/std/errors"
)

var (
	// Cloud Run accepts a service account email or a bare account ID, which it expands in the project.
	serviceAccountPattern = regexp.MustCompile(`^(?:[a-z][a-z0-9-]{4,28}[a-z0-9]|[a-z0-9][a-z0-9._-]*@[a-z0-9.-]+\.gserviceaccount\.com)$`)
	connectorPattern      = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/connectors/[^/]+$`)
	encryptionKeyPattern  = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+$`)
	connectionNamePattern = regexp.MustCompile(`^(?:[a-z0-9.-]+:)?[a-z0-9-]+:[a-z]+(?:-[a-z]+)+[0-9]+:[a-z][a-z0-9-]*$`)
)

// ValidateResourceIdentity checks restrictions that need the resource ID; callers validate the ID itself with ValidateName.
func ValidateResourceIdentity(kind string, body map[string]any, id string) error {
	template := objectAt(body, "template")
	if revision := stringAt(template, "revision"); revision != "" && (!strings.HasPrefix(revision, id+"-") || len(revision) > 63 || !containerNamePattern.MatchString(revision)) {
		return ruleErr("identity.revision", "revision must start with resource_name followed by '-' and be a valid name of at most 63 characters")
	}
	if kind != "job" {
		return nil
	}
	for _, field := range []string{"startExecutionToken", "runExecutionToken"} {
		if token := stringAt(body, field); token != "" && len(id)+len(token) >= 63 {
			return ruleErr("identity.execution-token", "job name and execution token together must contain fewer than 63 characters")
		}
	}
	return nil
}

// validateRuntime checks template-level runtime settings, each container's runtime limits, GPU placement, and volume sources.
func validateRuntime(kind string, template map[string]any, containers []any) error {
	if len(containers) > 10 {
		return ruleErr("container.count", "Cloud Run supports at most 10 containers")
	}
	gen1 := template["executionEnvironment"] == "EXECUTION_ENVIRONMENT_GEN1"
	if gen1 && kind != "service" {
		return ruleErr("runtime.gen1", "first generation is only supported for services")
	}
	if err := validateTemplateReferences(template); err != nil {
		return err
	}
	if err := validateProcessTotals(containers); err != nil {
		return err
	}
	gpuCount := 0
	for _, raw := range containers {
		if hasGPU(raw.(map[string]any)) {
			gpuCount++
		}
	}
	for _, raw := range containers {
		c := raw.(map[string]any)
		if err := validateContainerRuntime(kind, c, template, gen1, hasGPU(c), gpuCount > 0); err != nil {
			return err
		}
	}
	if err := validateGPUPlacement(template, gpuCount); err != nil {
		return err
	}
	return validateVolumeSources(template, gen1)
}

// validateTemplateReferences checks the service account, CMEK, and VPC connector resource names.
func validateTemplateReferences(template map[string]any) error {
	if sa := stringAt(template, "serviceAccount"); sa != "" && !serviceAccountPattern.MatchString(sa) {
		return ruleErr("identity.service-account", "serviceAccount must be a service account email or account ID")
	}
	if key := stringAt(template, "encryptionKey"); key != "" && !encryptionKeyPattern.MatchString(key) {
		return ruleErr("encryption.key", "encryptionKey must be a full Cloud KMS cryptoKey resource name")
	}
	if connector := stringAt(objectAt(template, "vpcAccess"), "connector"); connector != "" && !connectorPattern.MatchString(connector) {
		return ruleErr("vpc.connector", "vpcAccess.connector must be projects/PROJECT/locations/REGION/connectors/CONNECTOR")
	}
	for _, field := range []string{"encryptionKeyRevocationAction", "encryptionKeyShutdownDuration"} {
		if template[field] != nil && stringAt(template, "encryptionKey") == "" {
			return ruleErr("encryption.requires-key", "%s requires encryptionKey", field)
		}
	}
	if text := stringAt(template, "encryptionKeyShutdownDuration"); text != "" {
		d, _ := time.ParseDuration(text)
		if d%time.Hour != 0 || template["encryptionKeyRevocationAction"] != "SHUTDOWN" {
			return ruleErr("encryption.shutdown", "encryptionKeyShutdownDuration requires SHUTDOWN and whole-hour increments")
		}
	}
	return nil
}

// hasGPU reports whether a container requests a GPU.
func hasGPU(c map[string]any) bool {
	return stringAt(objectAt(objectAt(c, "resources"), "limits"), "nvidia.com/gpu") != ""
}

// validateProcessTotals applies Cloud Run's per-resource limits on environment variables and arguments.
func validateProcessTotals(containers []any) error {
	env, args := 0, 0
	for _, raw := range containers {
		env += len(arrayAt(raw.(map[string]any), "env"))
		args += len(arrayAt(raw.(map[string]any), "args"))
	}
	if env > 1000 {
		return ruleErr("env.count", "all containers together may have at most 1000 environment variables")
	}
	if args > 1000 {
		return ruleErr("container.args-count", "all containers together may have at most 1000 arguments")
	}
	return nil
}

// validateContainerRuntime checks one container's probes, process fields, and CPU or GPU runtime constraints.
func validateContainerRuntime(kind string, c, template map[string]any, gen1, gpu, instanceGPU bool) error {
	if kind == "job" && c["livenessProbe"] != nil {
		return ruleErr("probe.job-liveness", "Cloud Run jobs do not support liveness probes")
	}
	if kind != "service" && c["readinessProbe"] != nil {
		return ruleErr("probe.readiness-service-only", "readiness probes are only supported for services")
	}
	if err := validateProcess(c); err != nil {
		return err
	}
	cpuIdle := objectAt(c, "resources")["cpuIdle"] == true
	if kind != "service" && cpuIdle {
		return ruleErr("compute.cpu-idle-service-only", "cpuIdle: true is only supported for services")
	}
	if err := validateContainerGPU(kind, template, gen1, gpu, cpuIdle); err != nil {
		return err
	}
	if err := validateProbeLimits(c); err != nil {
		return err
	}
	return validateStartupBudget(c, instanceGPU)
}

// validateProcess checks NUL bytes, the working directory, and the command.
func validateProcess(c map[string]any) error {
	for _, raw := range arrayAt(c, "env") {
		e := raw.(map[string]any)
		if strings.ContainsRune(stringAt(e, "name")+stringAt(e, "value"), 0) {
			return ruleErr("env.nul", "environment names and values cannot contain NUL")
		}
	}
	if dir := stringAt(c, "workingDir"); dir != "" && (!path.IsAbs(dir) || strings.ContainsRune(dir, 0)) {
		return ruleErr("container.working-dir", "workingDir must be an absolute container path")
	}
	for _, key := range []string{"command", "args"} {
		for _, v := range arrayAt(c, key) {
			if strings.ContainsRune(v.(string), 0) {
				return ruleErr("container.nul", "%s cannot contain NUL", key)
			}
		}
	}
	if command := arrayAt(c, "command"); len(command) > 0 && command[0] == "" {
		return ruleErr("container.empty-command", "container command must not start with an empty executable")
	}
	return nil
}

// validateContainerGPU checks the execution environment, CPU allocation, and job settings a GPU container needs.
func validateContainerGPU(kind string, template map[string]any, gen1, gpu, cpuIdle bool) error {
	if !gpu {
		return nil
	}
	if gen1 {
		return ruleErr("gpu.gen1", "GPU requires second generation")
	}
	if cpuIdle {
		return ruleErr("gpu.cpu-idle", "GPU requires instance-based CPU allocation")
	}
	if kind != "job" {
		return nil
	}
	if template["gpuZonalRedundancyDisabled"] != true {
		return ruleErr("gpu.job-zonal", "GPU jobs require gpuZonalRedundancyDisabled: true")
	}
	return durationBound("gpu.job-timeout", template, "timeout", time.Second, time.Hour)
}

// validateGPUPlacement requires at most one GPU container, matched by an accelerator and GPU-only settings.
func validateGPUPlacement(template map[string]any, gpuCount int) error {
	if gpuCount > 1 {
		return ruleErr("gpu.count", "only one container may request a GPU")
	}
	if stringAt(objectAt(template, "nodeSelector"), "accelerator") != "" && gpuCount != 1 {
		return ruleErr("gpu.accelerator-container", "nodeSelector.accelerator requires exactly one GPU container")
	}
	if template["gpuZonalRedundancyDisabled"] != nil && gpuCount == 0 {
		return ruleErr("gpu.zonal-without-gpu", "gpuZonalRedundancyDisabled requires a GPU")
	}
	return nil
}

// validateVolumeSources checks each volume source's required fields and execution environment.
func validateVolumeSources(template map[string]any, gen1 bool) error {
	for _, raw := range arrayAt(template, "volumes") {
		volume := raw.(map[string]any)
		if gen1 && (volume["gcs"] != nil || volume["nfs"] != nil || volume["emptyDir"] != nil) {
			return ruleErr("volume.gen1-source", "GCS, NFS, and emptyDir volumes require second generation")
		}
		if gcs := objectAt(volume, "gcs"); gcs != nil && stringAt(gcs, "bucket") == "" {
			return ruleErr("volume.gcs-bucket", "GCS volume requires a bucket")
		}
		if nfs := objectAt(volume, "nfs"); nfs != nil && (stringAt(nfs, "server") == "" || !strings.HasPrefix(stringAt(nfs, "path"), "/")) {
			return ruleErr("volume.nfs", "NFS volume requires a server and absolute export path")
		}
		if sql := objectAt(volume, "cloudSqlInstance"); sql != nil {
			if err := validateCloudSQL(sql); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateCloudSQL requires at least one instance, each a project:region:instance connection name.
func validateCloudSQL(sql map[string]any) error {
	instances := arrayAt(sql, "instances")
	if len(instances) == 0 {
		return ruleErr("volume.cloudsql-empty", "Cloud SQL volume requires at least one instance")
	}
	for _, v := range instances {
		if !connectionNamePattern.MatchString(v.(string)) {
			return ruleErr("volume.cloudsql-instance", "Cloud SQL instances must use project:region:instance connection names")
		}
	}
	return nil
}

// probeLimits caps each probe's timing fields as Cloud Run does; startup probes stay at 240 seconds even with a GPU.
var probeLimits = []struct {
	key                    string
	period, timeout, delay float64
}{
	{"startupProbe", 240, 240, 240},
	{"livenessProbe", 3600, 3600, 3600},
	{"readinessProbe", math.MaxInt32, 300, 0},
}

// validateProbeLimits bounds probe timing per probe; zero keeps the API default.
func validateProbeLimits(container map[string]any) error {
	for _, limit := range probeLimits {
		m := objectAt(container, limit.key)
		if err := bound("probe.interval", m, "periodSeconds", 0, limit.period); err != nil {
			return errors.Wrap(err, limit.key)
		}
		if err := bound("probe.interval", m, "timeoutSeconds", 0, limit.timeout); err != nil {
			return errors.Wrap(err, limit.key)
		}
		if limit.delay == 0 && probeSetting(m, "initialDelaySeconds", 0) != 0 {
			return ruleErr("probe.readiness-delay", "%s does not support initialDelaySeconds", limit.key)
		}
		if err := bound("probe.initial-delay", m, "initialDelaySeconds", 0, limit.delay); err != nil {
			return errors.Wrap(err, limit.key)
		}
	}
	return nil
}

// validateStartupBudget bounds how long a startup probe may keep failing: 600 seconds, or 1800 when the instance has a GPU.
func validateStartupBudget(container map[string]any, gpu bool) error {
	probe := objectAt(container, "startupProbe")
	if probe == nil {
		return nil
	}
	maximum := float64(600)
	if gpu {
		maximum = 1800
	}
	if probeSetting(probe, "periodSeconds", 10)*probeSetting(probe, "failureThreshold", 3) > maximum {
		return ruleErr("probe.startup-budget", "startup probe failureThreshold * periodSeconds must not exceed %g", maximum)
	}
	return nil
}

// ValidateImageConfig checks the selected container's command against the built OCI image configuration.
func ValidateImageConfig(container map[string]any, data []byte) error {
	var config struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Config       struct {
			Entrypoint []string
			Cmd        []string
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return errors.Wrap(err, "parse OCI config")
	}
	if config.OS != "linux" || config.Architecture != "amd64" {
		return ruleErr("image.platform", "Cloud Run image must include Linux amd64")
	}
	command := stringsOr(arrayAt(container, "command"), config.Config.Entrypoint)
	command = append(command, stringsOr(arrayAt(container, "args"), config.Config.Cmd)...)
	if len(command) == 0 || command[0] == "" {
		return ruleErr("image.entrypoint", "container has no command or image entrypoint")
	}
	for _, value := range command {
		if strings.ContainsRune(value, 0) {
			return ruleErr("image.nul", "image command and arguments cannot contain NUL")
		}
	}
	return nil
}

// stringsOr returns a nonempty manifest override as strings, or the image default.
func stringsOr(override []any, fallback []string) []string {
	if len(override) == 0 {
		return fallback
	}
	values := make([]string, 0, len(override))
	for _, value := range override {
		values = append(values, value.(string))
	}
	return values
}
