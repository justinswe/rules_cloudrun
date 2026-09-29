package runapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/justinswe/std/errors"
	"github.com/justinswe/rules_cloudrun/internal/manifest"
	"github.com/stretchr/testify/require"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (r roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return r(request) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

const ready = `{"etag":"fresh","generation":"2","observedGeneration":"2","reconciling":false,"terminalCondition":{"state":"CONDITION_SUCCEEDED"}}`

// manifestBody returns a minimal unbound resource body.
func manifestBody(kind string) map[string]any {
	template := map[string]any{"containers": []any{map[string]any{"name": "app", "image": "example.com/app@sha256:" + strings.Repeat("a", 64)}}}
	if kind == "job" {
		template = map[string]any{"template": template}
	}
	return map[string]any{"template": template}
}

// boundBody returns a body bound to a single-region destination.
func boundBody(kind string) map[string]any {
	collection, _ := manifest.ResourcePath(kind)
	body := manifestBody(kind)
	body["name"] = "projects/project/locations/us-west1/" + collection + "/testapp"
	return body
}

func TestCreateUpdateAndValidation(t *testing.T) {
	for _, kind := range []string{"service", "job", "worker"} {
		for _, existing := range []bool{false, true} {
			for _, validate := range []bool{false, true} {
				t.Run(kind+"/"+boolName(existing)+"/"+boolName(validate), func(t *testing.T) {
					body := boundBody(kind)
					count := 0
					api := &API{BaseURL: "https://run.googleapis.com", PollInterval: time.Nanosecond, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
						count++
						switch count {
						case 1:
							require.Equal(t, "GET", r.Method)
							if !existing {
								return response(404, `{}`), nil
							}
							return response(200, `{"etag":"old","scaling":{"minInstanceCount":2},"description":"remove me"}`), nil
						case 2:
							require.Equal(t, validate, r.URL.Query().Get("validateOnly") == "true")
							var sent map[string]any
							require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
							require.NotContains(t, sent, "description")
							if existing {
								require.Equal(t, "PATCH", r.Method)
								require.Equal(t, "old", sent["etag"])
								if kind == "job" {
									require.Empty(t, r.URL.Query().Get("updateMask"))
								} else {
									require.Contains(t, r.URL.Query().Get("updateMask"), "description")
									require.Contains(t, r.URL.Query().Get("updateMask"), "scaling")
								}
							} else {
								require.Equal(t, "POST", r.Method)
								require.NotContains(t, sent, "name")
								require.Equal(t, "testapp", r.URL.Query().Get(map[string]string{"service": "serviceId", "job": "jobId", "worker": "workerPoolId"}[kind]))
							}
							return response(200, `{"name":"projects/project/locations/us-west1/operations/op","done":false}`), nil
						case 3:
							require.Contains(t, r.URL.Path, "/operations/op")
							return response(200, `{"done":true,"response":{"generation":"2"}}`), nil
						case 4:
							return response(200, ready), nil
						default:
							t.Fatal("unexpected request")
							return nil, nil
						}
					})}}
					require.NoError(t, api.Apply(context.Background(), kind, body, validate))
					require.NotContains(t, body, "etag")
					if validate {
						require.Equal(t, 2, count)
					} else {
						require.Equal(t, 4, count)
					}
				})
			}
		}
	}
}

func boolName(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func TestAPIFailures(t *testing.T) {
	cases := []struct {
		name      string
		responses []string
		statuses  []int
		change    func(map[string]any)
		message   string
	}{
		{"permission", []string{`{}`}, []int{403}, nil, "HTTP 403"},
		{"get corrupt", []string{`bad`}, []int{200}, nil, "decode"},
		{"configured etag", []string{}, []int{}, func(b map[string]any) { b["etag"] = "old" }, "resource.etag"},
		{"invalid update", []string{`{"etag":"old"}`, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"template.containers[0].image is invalid"}}`}, []int{200, 400}, nil, "HTTP 400: INVALID_ARGUMENT: template.containers[0].image is invalid"},
		{"non-JSON error", []string{`{"etag":"old"}`, `<html>bad request</html>`}, []int{200, 400}, nil, "failed with HTTP 400: <html>bad request</html>"},
		{"operation fail", []string{`{}`, `{"name":"projects/p/locations/us-west1/operations/op","done":true,"error":{"code":3,"message":"image not found"}}`, `{"latestCreatedRevision":"testapp-00002","terminalCondition":{"state":"CONDITION_FAILED","reason":"ContainerMissing"}}`}, []int{404, 200, 200}, nil, `operation "projects/p/locations/us-west1/operations/op" failed: 3: image not found; Cloud Run resource failed reconciliation: ContainerMissing (latest created revision testapp-00002)`},
		{"missing operation", []string{`{}`, `{}`}, []int{404, 200}, nil, "operation name"},
		{"reconciliation fail", []string{`{}`, `{"done":true,"response":{"generation":"2"}}`, `{"generation":"2","latestCreatedRevision":"testapp-00002","terminalCondition":{"state":"CONDITION_FAILED","reason":"ContainerMissing","message":"image not found"}}`}, []int{404, 200, 200}, nil, "failed reconciliation: ContainerMissing: image not found (latest created revision testapp-00002)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := boundBody("service")
			if tc.change != nil {
				tc.change(body)
			}
			count := 0
			api := &API{BaseURL: "https://run.googleapis.com", PollInterval: time.Nanosecond, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				require.Less(t, count, len(tc.responses))
				result := response(tc.statuses[count], tc.responses[count])
				count++
				return result, nil
			})}}
			require.ErrorContains(t, api.Apply(context.Background(), "service", body, false), tc.message)
			require.Equal(t, len(tc.responses), count)
		})
	}
	attempts := 0
	api := &API{BaseURL: "https://run.googleapis.com", RetryDelay: time.Nanosecond, Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("network unavailable")
	})}}
	require.ErrorContains(t, api.Apply(context.Background(), "service", boundBody("service"), false), "network unavailable")
	require.Equal(t, submitAttempts, attempts)
	_, _, err := api.request(context.Background(), "GET", "../bad", nil, nil)
	require.Error(t, err)
	require.Error(t, api.Apply(context.Background(), "service", manifestBody("service"), false))
	require.Error(t, api.Apply(context.Background(), "unknown", boundBody("service"), false))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, api.pause(ctx))
}

func TestPollingWaitsForObservedGeneration(t *testing.T) {
	calls := 0
	api := &API{BaseURL: "https://run.googleapis.com", PollInterval: time.Nanosecond, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(200, `{"reconciling":false,"generation":"1","observedGeneration":"1","terminalCondition":{"state":"CONDITION_SUCCEEDED"}}`), nil
		}
		return response(200, ready), nil
	})}}
	require.NoError(t, api.waitReady(context.Background(), "projects/project/locations/us-west1/services/testapp", "2"))
	require.Equal(t, 2, calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api.PollInterval = time.Hour
	require.ErrorContains(t, api.waitReady(ctx, "projects/project/locations/us-west1/services/testapp", "2"), "waiting for Cloud Run")
}

type badReader struct{}

func (badReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (badReader) Close() error             { return nil }
func TestResponseReadError(t *testing.T) {
	api := &API{BaseURL: "https://run.googleapis.com", Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: badReader{}}, nil
	})}}
	_, _, err := api.request(context.Background(), "GET", "projects/p/locations/r/services/s", nil, nil)
	require.ErrorContains(t, err, "read failed")
	_, _, err = api.request(context.Background(), "GET", "projects/p/locations/r/services/s", nil, map[string]any{"invalid": bytes.NewBuffer(nil).Write})
	require.Error(t, err)
}

func TestOperationPollingFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api := &API{BaseURL: "https://run.googleapis.com", PollInterval: time.Nanosecond, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return response(403, `{}`), nil })}}
	op := map[string]any{"name": "projects/p/locations/us-west1/operations/op"}
	_, err := (&API{PollInterval: time.Hour}).waitOperation(ctx, "projects/p/locations/us-west1/services/s", op)
	require.ErrorContains(t, err, "waiting for Cloud Run")
	_, err = api.waitOperation(context.Background(), "projects/p/locations/us-west1/services/s", op)
	require.ErrorContains(t, err, "HTTP 403")
	require.ErrorContains(t, api.waitReady(context.Background(), "projects/p/locations/us-west1/services/s", "2"), "HTTP 403")
	api.BaseURL = ":bad"
	_, _, err = api.request(context.Background(), "GET", "projects/p/locations/r/services/s", nil, nil)
	require.Error(t, err)
}

func TestValidateOnlyDoesNotPollAnUnpersistedOperation(t *testing.T) {
	for _, failure := range []bool{false, true} {
		calls := 0
		api := &API{BaseURL: "https://run.googleapis.com", Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return response(404, `{}`), nil
			}
			require.Equal(t, 2, calls, "validateOnly operation names cannot be fetched")
			require.Equal(t, "true", r.URL.Query().Get("validateOnly"))
			if failure {
				return response(200, `{"error":{"code":3,"message":"invalid scaling"}}`), nil
			}
			return response(200, `{"name":"projects/project/locations/us-west1/operations/unpersisted"}`), nil
		})}}
		err := api.Apply(context.Background(), "service", boundBody("service"), true)
		if failure {
			require.ErrorContains(t, err, "validation failed: 3: invalid scaling")
		} else {
			require.NoError(t, err)
		}
		require.Equal(t, 2, calls)
	}
}

func TestPreserveExternalMetadata(t *testing.T) {
	desired := manifestBody("job")
	current := map[string]any{
		"labels":      map[string]any{"owner": "billing", "run.googleapis.com/managed": "internal"},
		"annotations": map[string]any{"example.com/tool": "keep"},
		"template":    map[string]any{"labels": map[string]any{"execution": "batch"}},
	}
	current["template"].(map[string]any)["annotations"] = map[string]any{"run.googleapis.com/client-name": "gcloud", "team": "batch"}
	merged := preserveMetadata(desired, current, nil)
	require.Equal(t, map[string]any{"owner": "billing"}, merged["labels"])
	require.Equal(t, map[string]any{"team": "batch"}, merged["template"].(map[string]any)["annotations"])
	require.Equal(t, map[string]any{"example.com/tool": "keep"}, merged["annotations"])
	require.Equal(t, map[string]any{"execution": "batch"}, merged["template"].(map[string]any)["labels"])
	require.NotContains(t, desired, "labels")
	require.NotContains(t, desired["template"], "labels")
	desired["labels"] = map[string]any{}
	merged = preserveMetadata(desired, current, nil)
	require.Empty(t, merged["labels"])
	require.NoError(t, manifest.Validate("job", merged))
}

func TestCompletedOperationRequiresGeneration(t *testing.T) {
	calls := 0
	api := &API{BaseURL: "https://run.googleapis.com", Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(404, `{}`), nil
		}
		return response(200, `{"done":true,"response":{}}`), nil
	})}}
	require.ErrorContains(t, api.Apply(context.Background(), "service", boundBody("service"), false), "missing its resource generation")
	require.Equal(t, 2, calls)
}

// scripted serves one response per request and records the requests it saw.
type scripted struct {
	t        *testing.T
	steps    []func(*http.Request) (*http.Response, error)
	requests []*http.Request
	bodies   []map[string]any
}

func (s *scripted) api() *API {
	return &API{BaseURL: "https://run.googleapis.com", PollInterval: time.Nanosecond, RetryDelay: time.Nanosecond, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		require.Less(s.t, len(s.requests), len(s.steps), "unexpected %s %s", r.Method, r.URL.Path)
		var body map[string]any
		if r.Method != http.MethodGet {
			require.NoError(s.t, json.NewDecoder(r.Body).Decode(&body))
		}
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, body)
		return s.steps[len(s.requests)-1](r)
	})}}
}

// reply returns a scripted step that answers with a fixed response.
func reply(status int, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) { return response(status, body), nil }
}

func TestApplyRetriesFromAFreshRead(t *testing.T) {
	script := &scripted{t: t, steps: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) { return nil, errors.New("connection reset") },
		reply(200, `{"etag":"first"}`),
		reply(412, `{"error":{"status":"FAILED_PRECONDITION","message":"etag mismatch"}}`),
		reply(200, `{"etag":"second"}`),
		reply(200, `{"name":"projects/project/locations/us-west1/operations/op","done":false}`),
		reply(503, `{"error":{"status":"UNAVAILABLE"}}`),
		reply(200, `{"done":true,"response":{"generation":"2"}}`),
		reply(429, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`),
		reply(200, ready),
	}}
	require.NoError(t, script.api().Apply(context.Background(), "service", boundBody("service"), false))
	require.Len(t, script.requests, len(script.steps))
	require.Equal(t, "PATCH", script.requests[4].Method)
	require.Equal(t, "second", script.bodies[4]["etag"])
}

func TestApplyRetriesAreBounded(t *testing.T) {
	steps := []func(*http.Request) (*http.Response, error){}
	for range submitAttempts {
		steps = append(steps, reply(200, `{"etag":"old"}`), reply(409, `{"error":{"status":"ABORTED","message":"operation in progress"}}`))
	}
	script := &scripted{t: t, steps: steps}
	require.ErrorContains(t, script.api().Apply(context.Background(), "service", boundBody("service"), false), "HTTP 409: ABORTED: operation in progress")
	require.Len(t, script.requests, len(steps))
}

func TestWaitReadyFailsWhenSuperseded(t *testing.T) {
	script := &scripted{t: t, steps: []func(*http.Request) (*http.Response, error){
		reply(200, `{"generation":"3","reconciling":true}`),
	}}
	require.ErrorContains(t, script.api().waitReady(context.Background(), "projects/p/locations/us-west1/services/s", "2"), "superseded by concurrent update (generation 3)")
	require.False(t, newer("bad", "2"))
	require.False(t, newer("2", "2"))
}

func TestDiagnosticsAreBounded(t *testing.T) {
	message := detail(map[string]any{"status": "INVALID_ARGUMENT", "message": strings.Repeat("é", diagnosticLimit)}, "status", "message")
	require.LessOrEqual(t, len(message), diagnosticLimit+len(": ..."))
	require.True(t, strings.HasSuffix(message, "..."))
	require.True(t, utf8.ValidString(message))
	require.Empty(t, detail(nil, "status", "message"))
	require.Empty(t, detail(map[string]any{"status": ""}, "status"))
}

func TestProvenanceLabels(t *testing.T) {
	provenance := map[string]string{"managed-by": "cloudrun-deploy", "release-id": "v2026-09-28-abc1234", "project-id": ""}
	t.Run("create", func(t *testing.T) {
		body := boundBody("service")
		body["labels"] = map[string]any{"team": "payments"}
		body["template"].(map[string]any)["labels"] = map[string]any{"revision": "keep"}
		script := &scripted{t: t, steps: []func(*http.Request) (*http.Response, error){reply(404, `{}`), reply(200, `{}`)}}
		api := script.api()
		api.Labels = provenance
		require.NoError(t, api.Apply(context.Background(), "service", body, true))
		require.Equal(t, map[string]any{"team": "payments", "managed-by": "cloudrun-deploy", "release-id": "v2026-09-28-abc1234"}, script.bodies[1]["labels"])
		require.Equal(t, map[string]any{"revision": "keep"}, script.bodies[1]["template"].(map[string]any)["labels"])
		require.Equal(t, map[string]any{"team": "payments"}, body["labels"])
	})
	t.Run("update", func(t *testing.T) {
		script := &scripted{t: t, steps: []func(*http.Request) (*http.Response, error){
			reply(200, `{"etag":"old","labels":{"owner":"billing","release-id":"v2026-09-01-0000000","project-id":"stale"}}`),
			reply(200, `{}`),
		}}
		api := script.api()
		api.Labels = provenance
		require.NoError(t, api.Apply(context.Background(), "service", boundBody("service"), true))
		require.Equal(t, map[string]any{"owner": "billing", "managed-by": "cloudrun-deploy", "release-id": "v2026-09-28-abc1234"}, script.bodies[1]["labels"])
		require.Contains(t, strings.Split(script.requests[1].URL.Query().Get("updateMask"), ","), "labels")
	})
}

// TestGlobalServiceUpdatesWithoutEtag covers multi-region services, whose global resource has no etag.
func TestGlobalServiceUpdatesWithoutEtag(t *testing.T) {
	body := boundBody("service")
	body["name"] = "projects/project/locations/global/services/testapp"
	body["multiRegionSettings"] = map[string]any{"regions": []any{"us-west1", "us-central1"}}
	script := &scripted{t: t, steps: []func(*http.Request) (*http.Response, error){
		reply(200, `{"generation":"1","multiRegionSettings":{"regions":["us-west1","us-central1"]}}`),
		reply(200, `{"done":true,"response":{"generation":"2"}}`),
		reply(200, ready),
	}}
	require.NoError(t, script.api().Apply(context.Background(), "service", body, false))
	require.Equal(t, http.MethodPatch, script.requests[1].Method)
	require.NotContains(t, script.bodies[1], "etag")
	require.Contains(t, strings.Split(script.requests[1].URL.Query().Get("updateMask"), ","), "multiRegionSettings")
	require.Contains(t, script.requests[2].URL.Path, "/locations/global/services/testapp")
}

// TestValidateOnlyNeverReachesMultiRegionServices guards against Cloud Run applying validateOnly requests at locations/global.
func TestValidateOnlyNeverReachesMultiRegionServices(t *testing.T) {
	body := boundBody("service")
	body["name"] = "projects/project/locations/global/services/testapp"
	script := &scripted{t: t}
	require.ErrorIs(t, script.api().Apply(context.Background(), "service", body, true), ErrMultiRegionValidation)
	require.Empty(t, script.requests)
}

// TestCreateRaceUpdatesTheNewResource covers a create that loses to a concurrent create or a lost response.
func TestCreateRaceUpdatesTheNewResource(t *testing.T) {
	script := &scripted{t: t, steps: []func(*http.Request) (*http.Response, error){
		reply(404, `{}`),
		reply(409, `{"error":{"status":"ALREADY_EXISTS","message":"Resource 'testapp' already exists."}}`),
		reply(200, `{"etag":"new"}`),
		reply(200, `{"done":true,"response":{"generation":"2"}}`),
		reply(200, ready),
	}}
	require.NoError(t, script.api().Apply(context.Background(), "service", boundBody("service"), false))
	require.Equal(t, http.MethodPost, script.requests[1].Method)
	require.Equal(t, http.MethodPatch, script.requests[3].Method)
	require.Equal(t, "new", script.bodies[3]["etag"])
}

// TestDeadlineErrorsKeepTheLastFailure covers polling that never recovers before the deploy deadline.
func TestDeadlineErrorsKeepTheLastFailure(t *testing.T) {
	calls := 0
	api := &API{BaseURL: "https://run.googleapis.com", PollInterval: time.Millisecond, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return response(404, `{}`), nil
		case 2:
			return response(200, `{"name":"projects/project/locations/us-west1/operations/op"}`), nil
		}
		return response(503, `{"error":{"status":"UNAVAILABLE","message":"backend unavailable"}}`), nil
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := api.Apply(ctx, "service", boundBody("service"), false)
	require.ErrorContains(t, err, "waiting for Cloud Run operation")
	require.ErrorContains(t, err, "last error: Cloud Run GET")
	require.ErrorContains(t, err, "UNAVAILABLE: backend unavailable")
	require.Greater(t, calls, 3, "transient polling failures are retried until the deadline")
}
