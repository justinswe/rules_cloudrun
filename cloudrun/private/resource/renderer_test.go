package resource

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"sigs.k8s.io/yaml"
)

type rendererSuite struct {
	suite.Suite
}

type fakeFileIO struct {
	readFiles  map[string][]byte
	writeFiles map[string][]byte
	readErr    error
	writeErr   error
}

func (f *fakeFileIO) ReadFile(path string) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	content, found := f.readFiles[path]
	if !found {
		return nil, os.ErrNotExist
	}
	return content, nil
}

func (f *fakeFileIO) WriteFile(path string, data []byte, _ os.FileMode) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writeFiles[path] = append([]byte{}, data...)
	return nil
}

func TestRendererSuite(t *testing.T) {
	suite.Run(t, new(rendererSuite))
}

func (s *rendererSuite) TestRenderServiceManifest() {
	s.Run("renders service manifest with defaults", func() {
		fileIO := &fakeFileIO{
			readFiles: map[string][]byte{
				"config.yaml": []byte(`
runConfig:
  cpu: 1
  memoryMiB: 512
  minInstances: 0
  maxInstances: 3
  concurrency: 1000
env:
  - variable: LOG_LEVEL
    value: info
`),
			},
			writeFiles: map[string][]byte{},
		}

		renderer := NewRenderer(fileIO)
		options := RenderOptions{
			ConfigPath:     "config.yaml",
			ServiceName:    "myapp",
			Region:         "us-central1",
			Image:          "example.com/myapp@sha256:abc",
			ResourceType:   "service",
			TimeoutSeconds: 300,
			OutputPath:     "manifest.yaml",
		}
		err := renderer.RenderManifest(options)
		require.NoError(s.T(), err)

		manifestData, found := fileIO.writeFiles["manifest.yaml"]
		require.True(s.T(), found)

		var raw map[string]interface{}
		require.NoError(s.T(), yaml.Unmarshal(manifestData, &raw))

		require.Equal(s.T(), "serving.knative.dev/v1", raw["apiVersion"])
		require.Equal(s.T(), "Service", raw["kind"])
		require.Nil(s.T(), raw["status"], "status field must not be present")

		metadata := raw["metadata"].(map[string]interface{})
		require.Equal(s.T(), "myapp", metadata["name"])
		require.Nil(s.T(), metadata["labels"])

		spec := raw["spec"].(map[string]interface{})
		template := spec["template"].(map[string]interface{})
		tmplMeta := template["metadata"].(map[string]interface{})
		annotations := tmplMeta["annotations"].(map[string]interface{})
		require.Equal(s.T(), "gen2", annotations["run.googleapis.com/execution-environment"])
		require.Equal(s.T(), "0", annotations["autoscaling.knative.dev/minScale"])
		require.Equal(s.T(), "3", annotations["autoscaling.knative.dev/maxScale"])

		tmplSpec := template["spec"].(map[string]interface{})
		require.EqualValues(s.T(), 1000, tmplSpec["containerConcurrency"])
		require.EqualValues(s.T(), 300, tmplSpec["timeoutSeconds"])

		containers := tmplSpec["containers"].([]interface{})
		require.Len(s.T(), containers, 1)

		container := containers[0].(map[string]interface{})
		require.Equal(s.T(), "example.com/myapp@sha256:abc", container["image"])
		require.Nil(s.T(), container["name"], "container name must not be present when empty")

		envList := container["env"].([]interface{})
		require.Len(s.T(), envList, 1)
		env0 := envList[0].(map[string]interface{})
		require.Equal(s.T(), "LOG_LEVEL", env0["name"])
		require.Equal(s.T(), "info", env0["value"])
	})

	s.Run("renders optional annotations and secret env", func() {
		fileIO := &fakeFileIO{
			readFiles: map[string][]byte{
				"config.yaml": []byte(`
runConfig:
  cpu: 2
  memoryMiB: 1024
  minInstances: 1
  maxInstances: 10
  concurrency: 250
  network: default
  subnet: app-subnet
  vpcConnector: connector-a
  vpcEgress: all-traffic
serviceAccount: app@project.iam.gserviceaccount.com
cloudsqlConnector: project:region:instance
env:
  - variable: API_KEY
    secret: projects/123456789/secrets/API_KEY
`),
			},
			writeFiles: map[string][]byte{},
		}

		renderer := NewRenderer(fileIO)
		options := RenderOptions{
			ConfigPath:     "config.yaml",
			ServiceName:    "myapp",
			Region:         "us-central1",
			Image:          "example.com/myapp@sha256:def",
			ResourceType:   "worker",
			TimeoutSeconds: 900,
			OutputPath:     "manifest.yaml",
		}
		err := renderer.RenderManifest(options)
		require.NoError(s.T(), err)

		manifestData := fileIO.writeFiles["manifest.yaml"]
		var raw map[string]interface{}
		require.NoError(s.T(), yaml.Unmarshal(manifestData, &raw))

		require.Nil(s.T(), raw["status"])

		spec := raw["spec"].(map[string]interface{})
		template := spec["template"].(map[string]interface{})
		tmplMeta := template["metadata"].(map[string]interface{})
		annotations := tmplMeta["annotations"].(map[string]interface{})
		require.Equal(s.T(), "project:region:instance", annotations["run.googleapis.com/cloudsql-instances"])
		require.Equal(s.T(), `[{"network":"default","subnetwork":"app-subnet"}]`, annotations["run.googleapis.com/network-interfaces"])
		require.Equal(s.T(), "connector-a", annotations["run.googleapis.com/vpc-access-connector"])
		require.Equal(s.T(), "all-traffic", annotations["run.googleapis.com/vpc-access-egress"])

		tmplSpec := template["spec"].(map[string]interface{})
		require.EqualValues(s.T(), 250, tmplSpec["containerConcurrency"])
		require.EqualValues(s.T(), 900, tmplSpec["timeoutSeconds"])
		require.Equal(s.T(), "app@project.iam.gserviceaccount.com", tmplSpec["serviceAccountName"])

		containers := tmplSpec["containers"].([]interface{})
		require.Len(s.T(), containers, 1)
		container := containers[0].(map[string]interface{})
		require.Nil(s.T(), container["name"])

		envList := container["env"].([]interface{})
		require.Len(s.T(), envList, 1)
		env0 := envList[0].(map[string]interface{})
		require.Equal(s.T(), "API_KEY", env0["name"])
		valueFrom := env0["valueFrom"].(map[string]interface{})
		secretKeyRef := valueFrom["secretKeyRef"].(map[string]interface{})
		require.Equal(s.T(), "API_KEY", secretKeyRef["name"])
		require.Equal(s.T(), "latest", secretKeyRef["key"])
	})
}

func (s *rendererSuite) TestRenderJobManifest() {
	s.Run("renders job manifest with all fields", func() {
		fileIO := &fakeFileIO{
			readFiles: map[string][]byte{
				"config.yaml": []byte(`
runConfig:
  cpu: 2
  memoryMiB: 1024
  taskCount: 5
  parallelism: 3
  maxRetries: 2
  timeoutSeconds: 600
serviceAccount: job@project.iam.gserviceaccount.com
cloudsqlConnector: project:region:instance
env:
  - variable: BATCH_SIZE
    value: "100"
  - variable: DB_PASSWORD
    secret: projects/123456789/secrets/DB_PASSWORD
`),
			},
			writeFiles: map[string][]byte{},
		}

		renderer := NewRenderer(fileIO)
		options := RenderOptions{
			ConfigPath:   "config.yaml",
			ServiceName:  "myjob",
			Region:       "us-central1",
			Image:        "example.com/myjob@sha256:abc123",
			ResourceType: "job",
			OutputPath:   "manifest.yaml",
		}
		err := renderer.RenderManifest(options)
		require.NoError(s.T(), err)

		manifestData := fileIO.writeFiles["manifest.yaml"]
		var raw map[string]interface{}
		require.NoError(s.T(), yaml.Unmarshal(manifestData, &raw))

		require.Equal(s.T(), "run.googleapis.com/v1", raw["apiVersion"])
		require.Equal(s.T(), "Job", raw["kind"])
		require.Nil(s.T(), raw["status"])

		metadata := raw["metadata"].(map[string]interface{})
		require.Equal(s.T(), "myjob", metadata["name"])
		require.Nil(s.T(), metadata["labels"])

		spec := raw["spec"].(map[string]interface{})
		tmpl := spec["template"].(map[string]interface{})

		tmplMeta := tmpl["metadata"].(map[string]interface{})
		annotations := tmplMeta["annotations"].(map[string]interface{})
		require.Equal(s.T(), "project:region:instance", annotations["run.googleapis.com/cloudsql-instances"])

		tmplSpec := tmpl["spec"].(map[string]interface{})
		require.EqualValues(s.T(), 5, tmplSpec["taskCount"])
		require.EqualValues(s.T(), 3, tmplSpec["parallelism"])

		taskTmpl := tmplSpec["template"].(map[string]interface{})
		taskSpec := taskTmpl["spec"].(map[string]interface{})
		require.EqualValues(s.T(), 2, taskSpec["maxRetries"])
		require.EqualValues(s.T(), 600, taskSpec["timeoutSeconds"])
		require.Equal(s.T(), "job@project.iam.gserviceaccount.com", taskSpec["serviceAccountName"])

		containers := taskSpec["containers"].([]interface{})
		require.Len(s.T(), containers, 1)
		container := containers[0].(map[string]interface{})
		require.Equal(s.T(), "example.com/myjob@sha256:abc123", container["image"])

		resources := container["resources"].(map[string]interface{})
		limits := resources["limits"].(map[string]interface{})
		require.Equal(s.T(), "2000m", limits["cpu"])
		require.Equal(s.T(), "1024Mi", limits["memory"])

		envList := container["env"].([]interface{})
		require.Len(s.T(), envList, 2)
		require.Equal(s.T(), "BATCH_SIZE", envList[0].(map[string]interface{})["name"])
		require.Equal(s.T(), "100", envList[0].(map[string]interface{})["value"])
		env1 := envList[1].(map[string]interface{})
		require.Equal(s.T(), "DB_PASSWORD", env1["name"])
		secretRef := env1["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
		require.Equal(s.T(), "DB_PASSWORD", secretRef["name"])
		require.Equal(s.T(), "latest", secretRef["key"])
	})

	s.Run("renders job manifest with defaults", func() {
		fileIO := &fakeFileIO{
			readFiles: map[string][]byte{
				"config.yaml": []byte(`
runConfig:
  cpu: 1
  memoryMiB: 512
`),
			},
			writeFiles: map[string][]byte{},
		}

		renderer := NewRenderer(fileIO)
		options := RenderOptions{
			ConfigPath:   "config.yaml",
			ServiceName:  "simple-job",
			Region:       "us-central1",
			Image:        "example.com/job@sha256:def",
			ResourceType: "job",
			OutputPath:   "manifest.yaml",
		}
		err := renderer.RenderManifest(options)
		require.NoError(s.T(), err)

		manifestData := fileIO.writeFiles["manifest.yaml"]
		var raw map[string]interface{}
		require.NoError(s.T(), yaml.Unmarshal(manifestData, &raw))

		spec := raw["spec"].(map[string]interface{})
		tmpl := spec["template"].(map[string]interface{})
		require.Nil(s.T(), tmpl["metadata"], "no metadata when no annotations")

		tmplSpec := tmpl["spec"].(map[string]interface{})
		require.EqualValues(s.T(), 1, tmplSpec["taskCount"])
		require.Nil(s.T(), tmplSpec["parallelism"])

		taskTmpl := tmplSpec["template"].(map[string]interface{})
		taskSpec := taskTmpl["spec"].(map[string]interface{})
		require.Nil(s.T(), taskSpec["maxRetries"])
		require.Nil(s.T(), taskSpec["timeoutSeconds"])
		require.Empty(s.T(), taskSpec["serviceAccountName"])
	})
}

func (s *rendererSuite) TestRenderManifestErrors() {
	s.Run("rejects invalid resource type", func() {
		fileIO := &fakeFileIO{
			readFiles:  map[string][]byte{},
			writeFiles: map[string][]byte{},
		}
		renderer := NewRenderer(fileIO)

		err := renderer.RenderManifest(RenderOptions{
			ConfigPath:   "config.yaml",
			ServiceName:  "myapp",
			Region:       "us-central1",
			Image:        "example.com/myapp@sha256:abc",
			ResourceType: "invalid",
			OutputPath:   "manifest.yaml",
		})
		require.Error(s.T(), err)
		require.Contains(s.T(), err.Error(), "resource type")
	})

	s.Run("returns error on file read failure", func() {
		fileIO := &fakeFileIO{
			readFiles:  map[string][]byte{},
			writeFiles: map[string][]byte{},
			readErr:    errors.New("read failure"),
		}
		renderer := NewRenderer(fileIO)

		err := renderer.RenderManifest(RenderOptions{
			ConfigPath:   "config.yaml",
			ServiceName:  "myapp",
			Region:       "us-central1",
			Image:        "example.com/myapp@sha256:abc",
			ResourceType: "service",
			OutputPath:   "manifest.yaml",
		})
		require.Error(s.T(), err)
		require.Contains(s.T(), err.Error(), "read failure")
	})
}

func (s *rendererSuite) TestSecretNameFromReference() {
	s.Run("extracts final segment", func() {
		require.Equal(s.T(), "API_KEY", secretNameFromReference("projects/123456789/secrets/API_KEY"))
	})

	s.Run("returns raw value when no delimiter", func() {
		require.Equal(s.T(), "API_KEY", secretNameFromReference("API_KEY"))
	})
}
