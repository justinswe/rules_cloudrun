package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"
	"sigs.k8s.io/yaml"
)

const (
	defaultCPU            = 1
	defaultMemoryMiB      = 512
	defaultTimeoutSeconds = 300
	defaultTaskCount      = 1
	resourceTypeService   = "service"
	resourceTypeJob       = "job"
	resourceTypeWorker    = "worker"
)

// FileIO abstracts file system operations for testability.
type FileIO interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
}

// OSFileIO is the production implementation of FileIO.
type OSFileIO struct{}

func (OSFileIO) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (OSFileIO) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// Renderer generates Cloud Run manifests from apphosting configuration files.
type Renderer struct {
	fileIO FileIO
}

// RenderOptions specifies the parameters for manifest generation.
type RenderOptions struct {
	ConfigPath     string
	ServiceName    string
	Region         string
	Image          string
	ResourceType   string
	TimeoutSeconds int
	OutputPath     string
}

type appHostingConfig struct {
	RunConfig         runConfigEntry `yaml:"runConfig"`
	Env               []envEntry     `yaml:"env"`
	ServiceAccount    string         `yaml:"serviceAccount"`
	CloudSQLConnector string         `yaml:"cloudsqlConnector"`
}

type runConfigEntry struct {
	CPU            *int        `yaml:"cpu"`
	MemoryMiB      *int        `yaml:"memoryMiB"`
	MinInstances   *int        `yaml:"minInstances"`
	MaxInstances   *int        `yaml:"maxInstances"`
	Concurrency    *int        `yaml:"concurrency"`
	Network        string      `yaml:"network"`
	Subnet         string      `yaml:"subnet"`
	VPCConnector   string      `yaml:"vpcConnector"`
	VPCEgress      string      `yaml:"vpcEgress"`
	TaskCount      *int        `yaml:"taskCount"`
	Parallelism    *int        `yaml:"parallelism"`
	MaxRetries     *int        `yaml:"maxRetries"`
	TimeoutSeconds *int        `yaml:"timeoutSeconds"`
	LivenessProbe  *probeEntry `yaml:"livenessProbe"`
	ReadinessProbe *probeEntry `yaml:"readinessProbe"`
	StartupProbe   *probeEntry `yaml:"startupProbe"`
}

type probeEntry struct {
	InitialDelaySeconds *int32          `yaml:"initialDelaySeconds"`
	TimeoutSeconds      *int32          `yaml:"timeoutSeconds"`
	PeriodSeconds       *int32          `yaml:"periodSeconds"`
	SuccessThreshold    *int32          `yaml:"successThreshold"`
	FailureThreshold    *int32          `yaml:"failureThreshold"`
	HTTPGet             *httpGetEntry   `yaml:"httpGet"`
	TCPSocket           *tcpSocketEntry `yaml:"tcpSocket"`
	GRPC                *grpcEntry      `yaml:"grpc"`
}

type httpGetEntry struct {
	Path        string            `yaml:"path"`
	Port        *int32            `yaml:"port"`
	HTTPHeaders []httpHeaderEntry `yaml:"httpHeaders"`
}

type httpHeaderEntry struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type tcpSocketEntry struct {
	Port *int32 `yaml:"port"`
}

type grpcEntry struct {
	Port    *int32 `yaml:"port"`
	Service string `yaml:"service"`
}

type envEntry struct {
	Variable string `yaml:"variable"`
	Value    string `yaml:"value"`
	Secret   string `yaml:"secret"`
}

// NewRenderer creates a Renderer with the given FileIO implementation.
func NewRenderer(fileIO FileIO) *Renderer {
	if fileIO == nil {
		fileIO = OSFileIO{}
	}
	return &Renderer{fileIO: fileIO}
}

// RenderManifest reads an apphosting config and writes a Cloud Run manifest.
func (r *Renderer) RenderManifest(options RenderOptions) error {
	if err := validateRenderOptions(options); err != nil {
		return err
	}

	configContent, err := r.fileIO.ReadFile(options.ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var config appHostingConfig
	if err := yamlv3.Unmarshal(configContent, &config); err != nil {
		return fmt.Errorf("parse config yaml: %w", err)
	}

	var manifestContent []byte
	switch options.ResourceType {
	case resourceTypeJob:
		job := buildJobManifest(config, options)
		manifestContent, err = yaml.Marshal(job)
		if err != nil {
			return fmt.Errorf("marshal job manifest: %w", err)
		}
	case resourceTypeWorker:
		worker := buildWorkerManifest(config, options)
		manifestContent, err = yaml.Marshal(worker)
		if err != nil {
			return fmt.Errorf("marshal worker manifest: %w", err)
		}
	default:
		service, buildErr := buildServiceManifest(config, options)
		if buildErr != nil {
			return buildErr
		}
		raw, marshalErr := yaml.Marshal(service)
		if marshalErr != nil {
			return fmt.Errorf("marshal service manifest: %w", marshalErr)
		}
		manifestContent, err = cleanServiceManifest(raw)
		if err != nil {
			return fmt.Errorf("clean service manifest: %w", err)
		}
	}

	return r.fileIO.WriteFile(options.OutputPath, manifestContent, 0o644)
}

// ── Service manifest ────────────────────────────────────────────────────────

func buildServiceManifest(config appHostingConfig, options RenderOptions) (*servingv1.Service, error) {
	timeout := options.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeoutSeconds
	}

	cpu := defaultCPU
	if config.RunConfig.CPU != nil {
		cpu = *config.RunConfig.CPU
	}
	memoryMiB := defaultMemoryMiB
	if config.RunConfig.MemoryMiB != nil {
		memoryMiB = *config.RunConfig.MemoryMiB
	}

	limits := corev1.ResourceList{
		corev1.ResourceCPU:    apiresource.MustParse(fmt.Sprintf("%dm", cpu*1000)),
		corev1.ResourceMemory: apiresource.MustParse(fmt.Sprintf("%dMi", memoryMiB)),
	}

	templateAnnotations := map[string]string{
	        "run.googleapis.com/execution-environment": "gen2",
	}
	if config.CloudSQLConnector != "" {
	        templateAnnotations["run.googleapis.com/cloudsql-instances"] = config.CloudSQLConnector
	}
	if config.RunConfig.Network != "" && config.RunConfig.Subnet != "" {
		templateAnnotations["run.googleapis.com/network-interfaces"] = fmt.Sprintf(
			`[{"network":"%s","subnetwork":"%s"}]`,
			config.RunConfig.Network,
			config.RunConfig.Subnet,
		)
	}
	if config.RunConfig.VPCConnector != "" {
		templateAnnotations["run.googleapis.com/vpc-access-connector"] = config.RunConfig.VPCConnector
	}
	if config.RunConfig.VPCEgress != "" {
		templateAnnotations["run.googleapis.com/vpc-access-egress"] = config.RunConfig.VPCEgress
	}

	envVars := buildServiceEnvironmentVariables(config.Env)
	container := corev1.Container{
		Image: options.Image,
		Resources: corev1.ResourceRequirements{
			Limits: limits,
		},
		Env:            envVars,
		LivenessProbe:  buildCoreV1Probe(config.RunConfig.LivenessProbe),
		ReadinessProbe: buildCoreV1Probe(config.RunConfig.ReadinessProbe),
		StartupProbe:   buildCoreV1Probe(config.RunConfig.StartupProbe),
	}

	timeoutSeconds := int64(timeout)
	revisionSpec := servingv1.RevisionSpec{
		PodSpec: corev1.PodSpec{
			Containers: []corev1.Container{container},
		},
		TimeoutSeconds: &timeoutSeconds,
	}
	if config.ServiceAccount != "" {
		revisionSpec.PodSpec.ServiceAccountName = config.ServiceAccount
	}
	if config.RunConfig.Concurrency != nil {
		containerConcurrency := int64(*config.RunConfig.Concurrency)
		revisionSpec.ContainerConcurrency = &containerConcurrency
	}

	serviceAnnotations := map[string]string{
	        "run.googleapis.com/ingress": "all",
	}
	if config.RunConfig.MinInstances != nil {
	        serviceAnnotations["run.googleapis.com/minScale"] = strconv.Itoa(*config.RunConfig.MinInstances)
	}
	if config.RunConfig.MaxInstances != nil {
	        serviceAnnotations["run.googleapis.com/maxScale"] = strconv.Itoa(*config.RunConfig.MaxInstances)
	}
	if config.RunConfig.LivenessProbe != nil || config.RunConfig.ReadinessProbe != nil || config.RunConfig.StartupProbe != nil {
	        serviceAnnotations["run.googleapis.com/launch-stage"] = "BETA"
	}

	service := &servingv1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "serving.knative.dev/v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        options.ServiceName,
			Annotations: serviceAnnotations,
		},
		Spec: servingv1.ServiceSpec{
			ConfigurationSpec: servingv1.ConfigurationSpec{
				Template: servingv1.RevisionTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: templateAnnotations,
					},
					Spec: revisionSpec,
				},
			},
		},
	}
	return service, nil
}

func cleanServiceManifest(data []byte) ([]byte, error) {
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	delete(raw, "status")
	removeEmptyContainerNames(raw)
	return yaml.Marshal(raw)
}

func removeEmptyContainerNames(node interface{}) {
	switch v := node.(type) {
	case map[string]interface{}:
		if _, hasImage := v["image"]; hasImage {
			if name, ok := v["name"].(string); ok && name == "" {
				delete(v, "name")
			}
		}
		for _, child := range v {
			removeEmptyContainerNames(child)
		}
	case []interface{}:
		for _, item := range v {
			removeEmptyContainerNames(item)
		}
	}
}

func buildCoreV1Probe(p *probeEntry) *corev1.Probe {
	if p == nil {
		return nil
	}
	probe := &corev1.Probe{}
	if p.InitialDelaySeconds != nil {
		probe.InitialDelaySeconds = *p.InitialDelaySeconds
	}
	if p.TimeoutSeconds != nil {
		probe.TimeoutSeconds = *p.TimeoutSeconds
	}
	if p.PeriodSeconds != nil {
		probe.PeriodSeconds = *p.PeriodSeconds
	}
	if p.SuccessThreshold != nil {
		probe.SuccessThreshold = *p.SuccessThreshold
	}
	if p.FailureThreshold != nil {
		probe.FailureThreshold = *p.FailureThreshold
	}

	if p.HTTPGet != nil {
		probe.HTTPGet = &corev1.HTTPGetAction{
			Path: p.HTTPGet.Path,
		}
		if p.HTTPGet.Port != nil {
			probe.HTTPGet.Port = intstr.FromInt32(*p.HTTPGet.Port)
		}
		for _, h := range p.HTTPGet.HTTPHeaders {
			probe.HTTPGet.HTTPHeaders = append(probe.HTTPGet.HTTPHeaders, corev1.HTTPHeader{
				Name:  h.Name,
				Value: h.Value,
			})
		}
	} else if p.TCPSocket != nil {
		probe.TCPSocket = &corev1.TCPSocketAction{}
		if p.TCPSocket.Port != nil {
			probe.TCPSocket.Port = intstr.FromInt32(*p.TCPSocket.Port)
		}
	} else if p.GRPC != nil {
		probe.GRPC = &corev1.GRPCAction{}
		if p.GRPC.Port != nil {
			probe.GRPC.Port = *p.GRPC.Port
		}
		if p.GRPC.Service != "" {
			service := p.GRPC.Service
			probe.GRPC.Service = &service
		}
	}
	return probe
}

func buildServiceEnvironmentVariables(entries []envEntry) []corev1.EnvVar {
	envVars := make([]corev1.EnvVar, 0, len(entries))
	for _, entry := range entries {
		if entry.Variable == "" {
			continue
		}
		if entry.Value != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  entry.Variable,
				Value: entry.Value,
			})
			continue
		}
		if entry.Secret == "" {
			continue
		}
		secretName := secretNameFromReference(entry.Secret)
		envVars = append(envVars, corev1.EnvVar{
			Name: entry.Variable,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "latest",
				},
			},
		})
	}
	return envVars
}

// ── Job manifest ────────────────────────────────────────────────────────────

type jobManifest struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   jobMetadata `json:"metadata"`
	Spec       jobSpec     `json:"spec"`
}

type jobMetadata struct {
	Name string `json:"name"`
}

type jobSpec struct {
	Template jobExecutionTemplate `json:"template"`
}

type jobExecutionTemplate struct {
	Metadata *jobAnnotatedMetadata `json:"metadata,omitempty"`
	Spec     jobExecutionSpec      `json:"spec"`
}

type jobAnnotatedMetadata struct {
	Annotations map[string]string `json:"annotations,omitempty"`
}

type jobExecutionSpec struct {
	TaskCount   int             `json:"taskCount"`
	Parallelism *int            `json:"parallelism,omitempty"`
	Template    jobTaskTemplate `json:"template"`
}

type jobTaskTemplate struct {
	Spec jobTaskSpec `json:"spec"`
}

type jobTaskSpec struct {
	Containers         []jobContainer `json:"containers"`
	ServiceAccountName string         `json:"serviceAccountName,omitempty"`
	MaxRetries         *int           `json:"maxRetries,omitempty"`
	TimeoutSeconds     *int           `json:"timeoutSeconds,omitempty"`
}

type jobContainer struct {
	Image     string                `json:"image"`
	Resources jobContainerResources `json:"resources"`
	Env       []jobEnvVar           `json:"env,omitempty"`
}

type jobContainerResources struct {
	Limits map[string]string `json:"limits"`
}

type jobEnvVar struct {
	Name      string           `json:"name"`
	Value     string           `json:"value,omitempty"`
	ValueFrom *jobEnvVarSource `json:"valueFrom,omitempty"`
}

type jobEnvVarSource struct {
	SecretKeyRef *jobSecretKeyRef `json:"secretKeyRef"`
}

type jobSecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

func buildJobManifest(config appHostingConfig, options RenderOptions) *jobManifest {
	cpu := defaultCPU
	if config.RunConfig.CPU != nil {
		cpu = *config.RunConfig.CPU
	}
	memoryMiB := defaultMemoryMiB
	if config.RunConfig.MemoryMiB != nil {
		memoryMiB = *config.RunConfig.MemoryMiB
	}
	taskCount := defaultTaskCount
	if config.RunConfig.TaskCount != nil {
		taskCount = *config.RunConfig.TaskCount
	}

	container := jobContainer{
		Image: options.Image,
		Resources: jobContainerResources{
			Limits: map[string]string{
				"cpu":    fmt.Sprintf("%dm", cpu*1000),
				"memory": fmt.Sprintf("%dMi", memoryMiB),
			},
		},
		Env: buildJobEnvironmentVariables(config.Env),
	}

	taskSpec := jobTaskSpec{
		Containers: []jobContainer{container},
	}
	if config.ServiceAccount != "" {
		taskSpec.ServiceAccountName = config.ServiceAccount
	}
	if config.RunConfig.MaxRetries != nil {
		taskSpec.MaxRetries = config.RunConfig.MaxRetries
	}
	if config.RunConfig.TimeoutSeconds != nil {
		taskSpec.TimeoutSeconds = config.RunConfig.TimeoutSeconds
	}

	executionSpec := jobExecutionSpec{
		TaskCount: taskCount,
		Template:  jobTaskTemplate{Spec: taskSpec},
	}
	if config.RunConfig.Parallelism != nil {
		executionSpec.Parallelism = config.RunConfig.Parallelism
	}

	executionTemplate := jobExecutionTemplate{
		Spec: executionSpec,
	}
	annotations := map[string]string{}
	if config.CloudSQLConnector != "" {
		annotations["run.googleapis.com/cloudsql-instances"] = config.CloudSQLConnector
	}
	if config.RunConfig.Network != "" && config.RunConfig.Subnet != "" {
		annotations["run.googleapis.com/network-interfaces"] = fmt.Sprintf(
			`[{"network":"%s","subnetwork":"%s"}]`,
			config.RunConfig.Network,
			config.RunConfig.Subnet,
		)
	}
	if config.RunConfig.VPCConnector != "" {
		annotations["run.googleapis.com/vpc-access-connector"] = config.RunConfig.VPCConnector
	}
	if config.RunConfig.VPCEgress != "" {
		annotations["run.googleapis.com/vpc-access-egress"] = config.RunConfig.VPCEgress
	}

	if len(annotations) > 0 {
		executionTemplate.Metadata = &jobAnnotatedMetadata{
			Annotations: annotations,
		}
	}

	return &jobManifest{
		APIVersion: "run.googleapis.com/v1",
		Kind:       "Job",
		Metadata:   jobMetadata{Name: options.ServiceName},
		Spec:       jobSpec{Template: executionTemplate},
	}
}

func buildJobEnvironmentVariables(entries []envEntry) []jobEnvVar {
	envVars := make([]jobEnvVar, 0, len(entries))
	for _, entry := range entries {
		if entry.Variable == "" {
			continue
		}
		if entry.Value != "" {
			envVars = append(envVars, jobEnvVar{
				Name:  entry.Variable,
				Value: entry.Value,
			})
			continue
		}
		if entry.Secret == "" {
			continue
		}
		secretName := secretNameFromReference(entry.Secret)
		envVars = append(envVars, jobEnvVar{
			Name: entry.Variable,
			ValueFrom: &jobEnvVarSource{
				SecretKeyRef: &jobSecretKeyRef{
					Name: secretName,
					Key:  "latest",
				},
			},
		})
	}
	return envVars
}

// ── Worker pool manifest ────────────────────────────────────────────────────

type workerPoolManifest struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Metadata   workerPoolMetadata `json:"metadata"`
	Spec       workerPoolSpec     `json:"spec"`
}

type workerPoolMetadata struct {
	Name        string            `json:"name"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type workerPoolSpec struct {
	Template workerPoolTemplate `json:"template"`
}

type workerPoolTemplate struct {
	Metadata *workerPoolAnnotatedMetadata `json:"metadata,omitempty"`
	Spec     workerPoolRevisionSpec       `json:"spec"`
}

type workerPoolAnnotatedMetadata struct {
	Annotations map[string]string `json:"annotations,omitempty"`
}

type workerPoolRevisionSpec struct {
	Containers         []workerPoolContainer `json:"containers"`
	ServiceAccountName string                `json:"serviceAccountName,omitempty"`
	TimeoutSeconds     *int                  `json:"timeoutSeconds,omitempty"`
}

type workerPoolContainer struct {
	Image          string                       `json:"image"`
	Resources      workerPoolContainerResources `json:"resources"`
	Env            []workerPoolEnvVar           `json:"env,omitempty"`
	LivenessProbe  *workerPoolProbe             `json:"livenessProbe,omitempty"`
	ReadinessProbe *workerPoolProbe             `json:"readinessProbe,omitempty"`
	StartupProbe   *workerPoolProbe             `json:"startupProbe,omitempty"`
}

type workerPoolProbe struct {
	InitialDelaySeconds *int32                     `json:"initialDelaySeconds,omitempty"`
	TimeoutSeconds      *int32                     `json:"timeoutSeconds,omitempty"`
	PeriodSeconds       *int32                     `json:"periodSeconds,omitempty"`
	SuccessThreshold    *int32                     `json:"successThreshold,omitempty"`
	FailureThreshold    *int32                     `json:"failureThreshold,omitempty"`
	HTTPGet             *workerPoolHTTPGetAction   `json:"httpGet,omitempty"`
	TCPSocket           *workerPoolTCPSocketAction `json:"tcpSocket,omitempty"`
	GRPC                *workerPoolGRPCAction      `json:"grpc,omitempty"`
}

type workerPoolHTTPGetAction struct {
	Path        string                 `json:"path,omitempty"`
	Port        *int32                 `json:"port,omitempty"`
	HTTPHeaders []workerPoolHTTPHeader `json:"httpHeaders,omitempty"`
}

type workerPoolHTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type workerPoolTCPSocketAction struct {
	Port *int32 `json:"port,omitempty"`
}

type workerPoolGRPCAction struct {
	Port    *int32 `json:"port,omitempty"`
	Service string `json:"service,omitempty"`
}

type workerPoolContainerResources struct {
	Limits map[string]string `json:"limits"`
}

type workerPoolEnvVar struct {
	Name      string                  `json:"name"`
	Value     string                  `json:"value,omitempty"`
	ValueFrom *workerPoolEnvVarSource `json:"valueFrom,omitempty"`
}

type workerPoolEnvVarSource struct {
	SecretKeyRef *workerPoolSecretKeyRef `json:"secretKeyRef"`
}

type workerPoolSecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

func buildWorkerPoolProbe(p *probeEntry) *workerPoolProbe {
	if p == nil {
		return nil
	}
	probe := &workerPoolProbe{
		InitialDelaySeconds: p.InitialDelaySeconds,
		TimeoutSeconds:      p.TimeoutSeconds,
		PeriodSeconds:       p.PeriodSeconds,
		SuccessThreshold:    p.SuccessThreshold,
		FailureThreshold:    p.FailureThreshold,
	}

	if p.HTTPGet != nil {
		probe.HTTPGet = &workerPoolHTTPGetAction{
			Path: p.HTTPGet.Path,
			Port: p.HTTPGet.Port,
		}
		for _, h := range p.HTTPGet.HTTPHeaders {
			probe.HTTPGet.HTTPHeaders = append(probe.HTTPGet.HTTPHeaders, workerPoolHTTPHeader{
				Name:  h.Name,
				Value: h.Value,
			})
		}
	} else if p.TCPSocket != nil {
		probe.TCPSocket = &workerPoolTCPSocketAction{
			Port: p.TCPSocket.Port,
		}
	} else if p.GRPC != nil {
		var service string
		if p.GRPC.Service != "" {
			service = p.GRPC.Service
		}
		probe.GRPC = &workerPoolGRPCAction{
			Port:    p.GRPC.Port,
			Service: service,
		}
	}
	return probe
}

func buildWorkerManifest(config appHostingConfig, options RenderOptions) *workerPoolManifest {
	cpu := defaultCPU
	if config.RunConfig.CPU != nil {
		cpu = *config.RunConfig.CPU
	}
	memoryMiB := defaultMemoryMiB
	if config.RunConfig.MemoryMiB != nil {
		memoryMiB = *config.RunConfig.MemoryMiB
	}

	container := workerPoolContainer{
		Image: options.Image,
		Resources: workerPoolContainerResources{
			Limits: map[string]string{
				"cpu":    fmt.Sprintf("%dm", cpu*1000),
				"memory": fmt.Sprintf("%dMi", memoryMiB),
			},
		},
		Env:            buildWorkerEnvironmentVariables(config.Env),
		LivenessProbe:  buildWorkerPoolProbe(config.RunConfig.LivenessProbe),
		ReadinessProbe: buildWorkerPoolProbe(config.RunConfig.ReadinessProbe),
		StartupProbe:   buildWorkerPoolProbe(config.RunConfig.StartupProbe),
	}

	revisionSpec := workerPoolRevisionSpec{
		Containers: []workerPoolContainer{container},
	}
	if config.ServiceAccount != "" {
		revisionSpec.ServiceAccountName = config.ServiceAccount
	}
	timeout := options.TimeoutSeconds
	if timeout > 0 {
		revisionSpec.TimeoutSeconds = &timeout
	}

	templateAnnotations := map[string]string{
		"run.googleapis.com/execution-environment": "gen2",
	}
	if config.CloudSQLConnector != "" {
	        templateAnnotations["run.googleapis.com/cloudsql-instances"] = config.CloudSQLConnector
	}
	if config.RunConfig.Network != "" && config.RunConfig.Subnet != "" {
		templateAnnotations["run.googleapis.com/network-interfaces"] = fmt.Sprintf(
			`[{"network":"%s","subnetwork":"%s"}]`,
			config.RunConfig.Network,
			config.RunConfig.Subnet,
		)
	}
	if config.RunConfig.VPCConnector != "" {
		templateAnnotations["run.googleapis.com/vpc-access-connector"] = config.RunConfig.VPCConnector
	}
	if config.RunConfig.VPCEgress != "" {
		templateAnnotations["run.googleapis.com/vpc-access-egress"] = config.RunConfig.VPCEgress
	}

	template := workerPoolTemplate{
		Spec: revisionSpec,
	}
	if len(templateAnnotations) > 0 {
		template.Metadata = &workerPoolAnnotatedMetadata{
			Annotations: templateAnnotations,
		}
	}

	workerAnnotations := map[string]string{}
	if config.RunConfig.MinInstances != nil {
	        workerAnnotations["run.googleapis.com/minScale"] = strconv.Itoa(*config.RunConfig.MinInstances)
	}
	if config.RunConfig.MaxInstances != nil {
	        workerAnnotations["run.googleapis.com/maxScale"] = strconv.Itoa(*config.RunConfig.MaxInstances)
	}
	if config.RunConfig.LivenessProbe != nil || config.RunConfig.ReadinessProbe != nil || config.RunConfig.StartupProbe != nil {
	        workerAnnotations["run.googleapis.com/launch-stage"] = "BETA"
	}

	metadata := workerPoolMetadata{
		Name: options.ServiceName,
	}
	if len(workerAnnotations) > 0 {
		metadata.Annotations = workerAnnotations
	}

	return &workerPoolManifest{
		APIVersion: "run.googleapis.com/v1",
		Kind:       "WorkerPool",
		Metadata:   metadata,
		Spec:       workerPoolSpec{Template: template},
	}
}

func buildWorkerEnvironmentVariables(entries []envEntry) []workerPoolEnvVar {
	envVars := make([]workerPoolEnvVar, 0, len(entries))
	for _, entry := range entries {
		if entry.Variable == "" {
			continue
		}
		if entry.Value != "" {
			envVars = append(envVars, workerPoolEnvVar{
				Name:  entry.Variable,
				Value: entry.Value,
			})
			continue
		}
		if entry.Secret == "" {
			continue
		}
		secretName := secretNameFromReference(entry.Secret)
		envVars = append(envVars, workerPoolEnvVar{
			Name: entry.Variable,
			ValueFrom: &workerPoolEnvVarSource{
				SecretKeyRef: &workerPoolSecretKeyRef{
					Name: secretName,
					Key:  "latest",
				},
			},
		})
	}
	return envVars
}

// ── Shared utilities ────────────────────────────────────────────────────────

func validateRenderOptions(options RenderOptions) error {
	if options.ConfigPath == "" {
		return errors.New("config path is required")
	}
	if options.ServiceName == "" {
		return errors.New("service name is required")
	}
	if options.Region == "" {
		return errors.New("region is required")
	}
	if options.Image == "" {
		return errors.New("image is required")
	}
	if options.OutputPath == "" {
		return errors.New("output path is required")
	}
	resourceType := options.ResourceType
	if resourceType == "" {
		resourceType = resourceTypeService
	}
	if resourceType != resourceTypeService && resourceType != resourceTypeWorker && resourceType != resourceTypeJob {
		return fmt.Errorf("resource type must be %q, %q, or %q, got %q",
			resourceTypeService, resourceTypeWorker, resourceTypeJob, options.ResourceType)
	}
	outputDirectory := filepath.Dir(options.OutputPath)
	if outputDirectory == "." || outputDirectory == "" {
		return nil
	}
	return os.MkdirAll(outputDirectory, 0o755)
}

func secretNameFromReference(secretReference string) string {
	parts := strings.Split(secretReference, "/")
	if len(parts) == 0 {
		return secretReference
	}
	return parts[len(parts)-1]
}
