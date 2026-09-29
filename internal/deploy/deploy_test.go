package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justinswe/rules_cloudrun/internal/runapi"
	"github.com/stretchr/testify/require"
)

var digestImage = "example.com/app@sha256:" + strings.Repeat("a", 64)

// call is one request the fake Cloud Run API received.
type call struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

// fakeRun is an in-memory Cloud Run v2 API whose operations complete immediately.
type fakeRun struct {
	mu        sync.Mutex
	resources map[string]map[string]any
	calls     []call
}

// ServeHTTP implements GET, create (POST), and update (PATCH) for any resource collection.
func (f *fakeRun) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.calls = append(f.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, body})
	name := strings.TrimPrefix(r.URL.Path, "/v2/")
	switch r.Method {
	case http.MethodGet:
		current, ok := f.resources[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(current)
	case http.MethodPost:
		for key, values := range r.URL.Query() {
			if strings.HasSuffix(key, "Id") {
				name += "/" + values[0]
			}
		}
		f.store(w, name, body)
	case http.MethodPatch:
		f.store(w, name, body)
	}
}

// store saves a resource as ready at the next generation and replies with a completed operation.
func (f *fakeRun) store(w http.ResponseWriter, name string, body map[string]any) {
	generation := "1"
	if _, ok := f.resources[name]; ok {
		generation = "2"
	}
	body["name"] = name
	body["generation"] = generation
	body["observedGeneration"] = generation
	body["terminalCondition"] = map[string]any{"state": "CONDITION_SUCCEEDED"}
	body["latestReadyRevision"] = "rev-" + generation
	f.resources[name] = body
	_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "response": map[string]any{"generation": generation}})
}

// start serves a fake API holding the given resources.
func start(t *testing.T, existing map[string]map[string]any) (*fakeRun, *runapi.API) {
	fake := &fakeRun{resources: existing}
	if fake.resources == nil {
		fake.resources = map[string]map[string]any{}
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return fake, &runapi.API{Client: server.Client(), BaseURL: server.URL, PollInterval: time.Nanosecond, RetryDelay: time.Nanosecond}
}

// body returns a minimal rendered manifest for kind.
func body(kind string) map[string]any {
	template := map[string]any{"containers": []any{map[string]any{"name": "app", "image": digestImage}}}
	if kind == "job" {
		template = map[string]any{"template": template}
	}
	return map[string]any{"template": template}
}

// options returns a deployment of body(kind) to project p.
func options(kind string, regions ...string) Options {
	return Options{Manifest: body(kind), Kind: kind, Name: "web", Project: "p", Regions: regions}
}

func TestRegionalCreate(t *testing.T) {
	fake, api := start(t, nil)
	var out bytes.Buffer
	require.NoError(t, Run(context.Background(), api, options("service", "us-west1"), &out))
	require.Equal(t, "POST", fake.calls[1].Method)
	require.Equal(t, "/v2/projects/p/locations/us-west1/services", fake.calls[1].Path)
	require.Equal(t, "serviceId=web", fake.calls[1].Query)
	require.NotContains(t, fake.calls[1].Body, "multiRegionSettings")
	require.Equal(t, "projects/p/locations/us-west1/services/web: generation 1, ready revision rev-1\n", out.String())
}

func TestRegionalUpdate(t *testing.T) {
	name := "projects/p/locations/us-west1/services/web"
	fake, api := start(t, map[string]map[string]any{name: {"etag": "old", "description": "stale"}})
	var out bytes.Buffer
	require.NoError(t, Run(context.Background(), api, options("service", "us-west1"), &out))
	require.Equal(t, "PATCH", fake.calls[1].Method)
	require.Equal(t, "/v2/"+name, fake.calls[1].Path)
	require.Contains(t, fake.calls[1].Query, "updateMask=")
	require.Equal(t, "old", fake.calls[1].Body["etag"])
	require.Contains(t, out.String(), "generation 2, ready revision rev-2")
}

func TestMultiRegionService(t *testing.T) {
	fake, api := start(t, nil)
	var out bytes.Buffer
	require.NoError(t, Run(context.Background(), api, options("service", "us-west1", "us-central1"), &out))
	require.Equal(t, "/v2/projects/p/locations/global/services", fake.calls[1].Path)
	require.Equal(t, map[string]any{"regions": []any{"us-west1", "us-central1"}}, fake.calls[1].Body["multiRegionSettings"])
	require.Contains(t, out.String(), "projects/p/locations/global/services/web")
}

func TestValidateOnlyRefusedForMultiRegion(t *testing.T) {
	fake, api := start(t, nil)
	opts := options("service", "us-west1", "us-central1")
	opts.ValidateOnly = true
	err := Run(context.Background(), api, opts, &bytes.Buffer{})
	require.ErrorContains(t, err, "refused for multi-region")
	require.Empty(t, fake.calls, "no request may reach locations/global")
}

func TestValidateOnlyRegional(t *testing.T) {
	fake, api := start(t, nil)
	opts := options("service", "us-west1")
	opts.ValidateOnly = true
	var out bytes.Buffer
	require.NoError(t, Run(context.Background(), api, opts, &out))
	require.Equal(t, "serviceId=web&validateOnly=true", fake.calls[1].Query)
	require.Equal(t, "projects/p/locations/us-west1/services/web: valid\n", out.String())
}

func TestImageOverride(t *testing.T) {
	fake, api := start(t, nil)
	opts := options("service", "us-west1")
	opts.Image = "example.com/app:v1"
	require.ErrorContains(t, Run(context.Background(), api, opts, &bytes.Buffer{}), "sha256 digest")
	require.Empty(t, fake.calls)

	pinned := "example.com/other@sha256:" + strings.Repeat("b", 64)
	opts.Image = pinned
	require.NoError(t, Run(context.Background(), api, opts, &bytes.Buffer{}))
	containers := fake.calls[1].Body["template"].(map[string]any)["containers"].([]any)
	require.Equal(t, pinned, containers[0].(map[string]any)["image"])
	require.Equal(t, digestImage, opts.Manifest["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"], "the source manifest is unchanged")
}

func TestJobDeploysInEachRegion(t *testing.T) {
	fake, api := start(t, nil)
	var out bytes.Buffer
	require.NoError(t, Run(context.Background(), api, options("job", "us-west1", "us-central1"), &out))
	paths := []string{}
	for _, c := range fake.calls {
		if c.Method == "POST" {
			paths = append(paths, c.Path+"?"+c.Query)
		}
	}
	require.Equal(t, []string{"/v2/projects/p/locations/us-west1/jobs?jobId=web", "/v2/projects/p/locations/us-central1/jobs?jobId=web"}, paths)
	require.Contains(t, out.String(), "projects/p/locations/us-central1/jobs/web: generation 1, latest execution none")
}

func TestWorkerPoolPath(t *testing.T) {
	fake, api := start(t, nil)
	require.NoError(t, Run(context.Background(), api, options(Kind("workerpool"), "us-west1"), &bytes.Buffer{}))
	require.Equal(t, "/v2/projects/p/locations/us-west1/workerPools", fake.calls[1].Path)
	require.Equal(t, "workerPoolId=web", fake.calls[1].Query)
}

func TestBindRejectsBadDestinations(t *testing.T) {
	for name, change := range map[string]func(*Options){
		"no region":       func(o *Options) { o.Regions = nil },
		"duplicate":       func(o *Options) { o.Regions = []string{"us-west1", "us-west1"} },
		"bad project":     func(o *Options) { o.Project = "bad/project" },
		"bad name":        func(o *Options) { o.Name = "Web" },
		"bad kind":        func(o *Options) { o.Kind = "function" },
		"job multiregion": func(o *Options) { o.Kind = "job"; o.Manifest = body("job"); o.Manifest["multiRegionSettings"] = map[string]any{} },
		"placeholder": func(o *Options) {
			o.Manifest["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "rules-cloudrun.invalid/override-required:latest"
		},
	} {
		t.Run(name, func(t *testing.T) {
			opts := options("service", "us-west1")
			change(&opts)
			_, err := Bind(opts)
			require.Error(t, err)
		})
	}
}
