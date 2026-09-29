package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPURuntimeConstraints(t *testing.T) {
	for _, kind := range []string{"service", "job", "worker"} {
		t.Run(kind, func(t *testing.T) {
			b := validBody(kind)
			tmpl := TaskTemplate(b, kind)
			c := firstContainer(b, kind)
			tmpl["nodeSelector"] = map[string]any{"accelerator": "nvidia-l4"}
			c["resources"] = map[string]any{"cpuIdle": false, "limits": map[string]any{"cpu": "4", "memory": "16Gi", "nvidia.com/gpu": "1"}}
			if kind == "job" {
				tmpl["gpuZonalRedundancyDisabled"] = true
				tmpl["timeout"] = "3600s"
			}
			c["startupProbe"] = map[string]any{"tcpSocket": map[string]any{"port": 8080}, "periodSeconds": 1, "failureThreshold": 1800}
			require.NoError(t, Validate(kind, b))
			objectAt(objectAt(c, "resources"), "limits")["memory"] = "2Gi"
			require.Error(t, Validate(kind, b))
			objectAt(objectAt(c, "resources"), "limits")["memory"] = "16Gi"
			objectAt(c, "resources")["cpuIdle"] = true
			require.Error(t, Validate(kind, b))
			objectAt(c, "resources")["cpuIdle"] = false
			if kind == "job" {
				tmpl["timeout"] = "3601s"
				require.Error(t, Validate(kind, b))
				tmpl["timeout"] = "3600s"
				delete(tmpl, "gpuZonalRedundancyDisabled")
				require.Error(t, Validate(kind, b))
			}
		})
	}
	b := validBody("service")
	tmpl := TaskTemplate(b, "service")
	tmpl["nodeSelector"] = map[string]any{"accelerator": "nvidia-rtx-pro-6000"}
	firstContainer(b, "service")["resources"] = map[string]any{"cpuIdle": false, "limits": map[string]any{"cpu": "20", "memory": "80Gi", "nvidia.com/gpu": "1"}}
	require.NoError(t, Validate("service", b))
}

func TestPartialInputCannotHideTypos(t *testing.T) {
	options := RenderOptions{ServiceName: "app", ResourceType: "service"}
	for _, input := range []string{"unknown: null", "uid: null", "template: {wrongField: null}"} {
		_, err := Render(nil, []byte(input), options)
		require.Error(t, err)
	}
	_, err := Render([]byte("unknown: value"), []byte("unknown: null"), options)
	require.Error(t, err)
}

func TestImageRuntimeContract(t *testing.T) {
	b := validBody("service")
	for _, config := range []string{`bad`, `{"os":"windows","architecture":"amd64"}`} {
		require.Error(t, ValidateImageConfig(firstContainer(b, "service"), []byte(config)))
	}
	config := []byte(`{"os":"linux","architecture":"amd64","config":{"Entrypoint":["/app/server"]}}`)
	require.NoError(t, ValidateImageConfig(firstContainer(b, "service"), config))
	dir := t.TempDir()
	path := filepath.Join(dir, "image.json")
	require.NoError(t, os.WriteFile(path, config, 0o600))
	body, err := json.Marshal(b)
	require.NoError(t, err)
	options := RenderOptions{ServiceName: "app", ResourceType: "service", ImageContainer: "app", ImageConfigPath: path}
	_, err = Render(nil, body, options)
	require.NoError(t, err)
	options.ImageConfigPath = "missing"
	_, err = Render(nil, body, options)
	require.Error(t, err)
	for _, image := range []string{"https://example.com/app", "example.com/UPPER", "example.com/a//b", "example.com/app:", "example.com/app/"} {
		require.Error(t, ValidateImage(image), image)
	}
}

func TestResourceIdentityConstraints(t *testing.T) {
	b := validBody("service")
	TaskTemplate(b, "service")["revision"] = "wrong-prefix"
	require.Error(t, ValidateResourceIdentity("service", b, "app"))
	TaskTemplate(b, "service")["revision"] = "app-v2"
	require.NoError(t, ValidateResourceIdentity("service", b, "app"))
	b = validBody("job")
	b["runExecutionToken"] = strings.Repeat("t", 60)
	require.Error(t, ValidateResourceIdentity("job", b, "app"))
	b["runExecutionToken"] = "token"
	require.NoError(t, ValidateResourceIdentity("job", b, "app"))
}
