// Package runapi applies Cloud Run v2 resources through the native REST API.
package runapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/justinswe/std/errors"
	"github.com/justinswe/std/retry"
	"github.com/justinswe/rules_cloudrun/internal/manifest"
)

const (
	// responseLimit bounds Cloud Run API response bodies.
	responseLimit = 64 << 20
	// diagnosticLimit bounds provider diagnostics copied into errors.
	diagnosticLimit = 1 << 10
	// submitAttempts bounds whole read-and-write attempts for transient and concurrency failures.
	submitAttempts = 6
	// submitBudget bounds the time spent retrying one write, long enough for an in-flight operation to finish.
	submitBudget = 5 * time.Minute
	// maxRetryDelay caps the backoff between write attempts.
	maxRetryDelay = 30 * time.Second
	// requestTimeout bounds one HTTP exchange so a stalled connection cannot hold the whole deploy.
	requestTimeout = time.Minute
	// defaultPollInterval spaces operation and readiness reads.
	defaultPollInterval = 2 * time.Second
)

// ErrMultiRegionValidation reports that a multi-region resource was validated locally only.
// Cloud Run applies requests to locations/global even when validateOnly is set, so they are never sent.
var ErrMultiRegionValidation = errors.New("Cloud Run applies multi-region requests even with validateOnly, so the service was validated locally only")

// API applies desired resources through the native Cloud Run v2 REST API.
type API struct {
	Client  *http.Client
	BaseURL string
	// PollInterval spaces operation and readiness reads; zero means two seconds.
	PollInterval time.Duration
	// RetryDelay is the first jittered retry backoff, doubling per attempt; zero means one second.
	RetryDelay time.Duration
	// Labels are deployer-owned top-level labels set on every write; empty values are skipped.
	Labels map[string]string
}

// Apply creates or updates one resource, waits for its operation, and checks readiness.
func (a *API) Apply(ctx context.Context, kind string, body map[string]any, validateOnly bool) error {
	if err := manifest.Validate(kind, body); err != nil {
		return err
	}
	name, _ := body["name"].(string)
	if name == "" {
		return errors.New("bound resource name is required")
	}
	if validateOnly && strings.Contains(name, "/locations/global/") {
		return ErrMultiRegionValidation
	}
	var last error
	// Every attempt rereads the resource, so a conflict retries against a fresh etag.
	operation, err := retry.DoValue(ctx, func(ctx context.Context) (map[string]any, error) {
		operation, err := a.submit(ctx, kind, name, body, validateOnly)
		last = err
		return operation, err
	}, a.submitOptions()...)
	if err != nil {
		return withLast(err, last, "applying "+name)
	}
	if validateOnly {
		// Cloud Run does not persist validateOnly operations; their names cannot be polled.
		if failure, ok := operation["error"]; ok && failure != nil {
			return errors.New("Cloud Run validation failed" + detail(failure, "code", "message"))
		}
		return nil
	}
	completed, err := a.waitOperation(ctx, name, operation)
	if err != nil {
		return err
	}
	result, _ := completed["response"].(map[string]any)
	generation, ok := result["generation"].(string)
	if !ok || generation == "" {
		return errors.New("completed Cloud Run operation is missing its resource generation")
	}
	return a.waitReady(ctx, name, generation)
}

// Get reads one resource by its full name.
func (a *API) Get(ctx context.Context, name string) (map[string]any, error) {
	current, _, err := a.request(ctx, http.MethodGet, name, nil, nil)
	return current, err
}

// submit reads the current resource and sends one create or update request.
func (a *API) submit(ctx context.Context, kind, name string, body map[string]any, validateOnly bool) (map[string]any, error) {
	current, status, err := a.request(ctx, http.MethodGet, name, nil, nil)
	if err != nil && status != http.StatusNotFound {
		return nil, err
	}
	query := url.Values{}
	if validateOnly {
		query.Set("validateOnly", "true")
	}
	if status == http.StatusNotFound {
		payload := maps.Clone(body)
		delete(payload, "name")
		a.label(payload)
		operation, _, err := a.request(ctx, http.MethodPost, createTarget(kind, name, query), query, payload)
		return operation, err
	}
	payload := a.updatePayload(body, current)
	if kind != "job" {
		// Jobs replace the whole resource; services and worker pools take a mask of fields to write or clear.
		query.Set("updateMask", updateMask(kind, current, payload))
	}
	operation, _, err := a.request(ctx, http.MethodPatch, name, query, payload)
	return operation, err
}

// createTarget returns the collection to create in, setting the ID query parameter from the resource name.
func createTarget(kind, name string, query url.Values) string {
	cut := strings.LastIndex(name, "/")
	collection, _ := manifest.ResourcePath(kind) // Validate accepted the kind.
	query.Set(strings.TrimSuffix(collection, "s")+"Id", name[cut+1:])
	return name[:cut]
}

// updatePayload carries external metadata forward, sets deployer labels, and conditions regional updates on the etag.
func (a *API) updatePayload(body, current map[string]any) map[string]any {
	payload := preserveMetadata(body, current, a.Labels)
	a.label(payload)
	// Multi-region services under locations/global carry no etag, so only regional updates are conditional.
	if etag, ok := current["etag"].(string); ok && etag != "" {
		payload["etag"] = etag
	}
	return payload
}

// updateMask names every writable top-level field set now or in the request, so omitted fields are cleared.
func updateMask(kind string, current, payload map[string]any) string {
	fields := []string{}
	for _, field := range manifest.WritableFields(kind) {
		_, old := current[field]
		_, desired := payload[field]
		if old || desired {
			fields = append(fields, field)
		}
	}
	return strings.Join(fields, ",")
}

// request sends one bounded HTTP exchange and decodes a successful JSON response.
func (a *API) request(ctx context.Context, method, name string, query url.Values, body map[string]any) (map[string]any, int, error) {
	if !strings.HasPrefix(name, "projects/") || strings.Contains(name, "..") || strings.ContainsAny(name, "?#") {
		return nil, 0, errors.New("invalid API resource path")
	}
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, errors.Wrap(err, "encode API request")
		}
		input = bytes.NewReader(data)
	}
	endpoint := strings.TrimRight(a.BaseURL, "/") + "/v2/" + name
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, input)
	if err != nil {
		return nil, 0, errors.Wrap(err, "create API request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.Client.Do(request)
	if err != nil {
		return nil, 0, retry.Retryable(errors.Wrap(err, "Cloud Run request"))
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return nil, response.StatusCode, retry.Retryable(errors.Wrap(err, "read Cloud Run response"))
	}
	if len(content) > responseLimit {
		return nil, response.StatusCode, errors.New("Cloud Run response exceeds 64 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, statusError(method, name, response.StatusCode, content)
	}
	var result map[string]any
	if err := manifest.DecodeJSON(content, &result); err != nil {
		return nil, response.StatusCode, errors.Wrap(err, "decode Cloud Run response")
	}
	return result, response.StatusCode, nil
}

// statusError reports a failed response with provider diagnostics, marking transient and concurrency failures retryable.
func statusError(method, name string, status int, content []byte) error {
	var failure struct{ Error map[string]any }
	diagnostics := detail(map[string]any{"body": strings.TrimSpace(string(content))}, "body")
	if json.Unmarshal(content, &failure) == nil && failure.Error != nil {
		diagnostics = detail(failure.Error, "status", "message")
	}
	err := errors.Errorf("Cloud Run %s %s failed with HTTP %d%s", method, name, status, diagnostics)
	if status == http.StatusConflict || status == http.StatusPreconditionFailed || status == http.StatusTooManyRequests || status >= 500 {
		return retry.Retryable(err)
	}
	return err
}

// detail formats the named provider diagnostic fields, bounded to diagnosticLimit bytes.
func detail(failure any, keys ...string) string {
	fields, _ := failure.(map[string]any)
	parts := []string{}
	for _, key := range keys {
		if value := fields[key]; value != nil && value != "" {
			parts = append(parts, fmt.Sprint(value))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	text := strings.Join(parts, ": ")
	if len(text) > diagnosticLimit {
		text = strings.ToValidUTF8(text[:diagnosticLimit], "") + "..."
	}
	return ": " + text
}

// submitOptions retries writes with jittered exponential backoff from RetryDelay, bounded in attempts and time.
func (a *API) submitOptions() []retry.Option {
	delay := a.RetryDelay
	if delay <= 0 {
		delay = time.Second
	}
	return []retry.Option{
		retry.WithMaxAttempts(submitAttempts),
		retry.WithMaxElapsed(submitBudget),
		retry.WithBackoff(retry.ExponentialBackoff(delay, maxRetryDelay)),
		retry.WithJitter(retry.EqualJitter),
	}
}

// withLast adds the last provider failure to a bare context error, which the retry package returns on its own.
func withLast(err, last error, what string) error {
	if last == nil || errors.Is(err, last) {
		return err
	}
	return errors.Wrapf(err, "%s; last error: %v", what, last)
}

// poll reads name until check reports done, riding out transient read failures until the context ends.
func (a *API) poll(ctx context.Context, name, what string, check func(map[string]any) (bool, error)) error {
	var last error
	for {
		if err := a.pause(ctx); err != nil {
			return withLast(errors.Wrap(err, "waiting for "+what), last, what)
		}
		current, _, err := a.request(ctx, http.MethodGet, name, nil, nil)
		if err != nil && !retry.IsRetryable(err) {
			return err
		}
		if err != nil {
			last = err
			continue
		}
		if done, err := check(current); done || err != nil {
			return err
		}
	}
}

// waitOperation waits for an operation to complete and returns the completed operation.
func (a *API) waitOperation(ctx context.Context, resource string, operation map[string]any) (map[string]any, error) {
	if done, err := a.operationDone(ctx, resource, operation); done || err != nil {
		return operation, err
	}
	name, _ := operation["name"].(string)
	if !strings.Contains(name, "/operations/") {
		return nil, errors.New("Cloud Run response is missing an operation name")
	}
	completed := operation
	err := a.poll(ctx, name, "Cloud Run operation "+name, func(current map[string]any) (bool, error) {
		completed = current
		return a.operationDone(ctx, resource, current)
	})
	return completed, err
}

// operationDone reports whether an operation finished, turning an operation error into a diagnostic.
func (a *API) operationDone(ctx context.Context, resource string, operation map[string]any) (bool, error) {
	failure, ok := operation["error"]
	if !ok || failure == nil {
		return operation["done"] == true, nil
	}
	name, _ := operation["name"].(string)
	message := fmt.Sprintf("Cloud Run operation %q failed%s", name, detail(failure, "code", "message"))
	// A failed rollout leaves its reason and revision on the resource; add them when they can be read.
	if current, _, err := a.request(ctx, http.MethodGet, resource, nil, nil); err == nil {
		if condition, _ := current["terminalCondition"].(map[string]any); condition["state"] == "CONDITION_FAILED" {
			message += "; " + reconciliationError(current, condition).Error()
		}
	}
	return true, errors.New(message)
}

// waitReady waits until the resource has reconciled exactly the given generation.
func (a *API) waitReady(ctx context.Context, name, generation string) error {
	state := ""
	err := a.poll(ctx, name, "Cloud Run resource "+name+" to become ready", func(current map[string]any) (bool, error) {
		observed, _ := current["generation"].(string)
		if newer(observed, generation) {
			return true, errors.Errorf("Cloud Run resource %s superseded by concurrent update (generation %s)", name, observed)
		}
		condition, _ := current["terminalCondition"].(map[string]any)
		state = detail(condition, "state", "reason", "message")
		if current["reconciling"] == true || observed != generation {
			return false, nil
		}
		if condition["state"] == "CONDITION_FAILED" {
			return true, reconciliationError(current, condition)
		}
		return condition["state"] == "CONDITION_SUCCEEDED" && current["observedGeneration"] == generation, nil
	})
	if err != nil && ctx.Err() != nil && state != "" {
		return errors.Wrap(err, "last condition"+state)
	}
	return err
}

// newer reports whether an observed generation is later than ours.
func newer(observed, ours string) bool {
	have, haveErr := strconv.ParseInt(observed, 10, 64)
	want, wantErr := strconv.ParseInt(ours, 10, 64)
	return haveErr == nil && wantErr == nil && have > want
}

// reconciliationError describes a failed terminal condition and the revision it created.
func reconciliationError(current, condition map[string]any) error {
	message := "Cloud Run resource failed reconciliation" + detail(condition, "reason", "message")
	if revision, _ := current["latestCreatedRevision"].(string); revision != "" {
		message += " (latest created revision " + revision + ")"
	}
	return errors.New(message)
}

// pause waits one poll interval or until the context ends.
func (a *API) pause(ctx context.Context) error {
	interval := a.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "Cloud Run")
	case <-timer.C:
		return nil
	}
}

// label sets deployer-owned top-level labels over manifest and carried-forward values.
func (a *API) label(request map[string]any) {
	labels := map[string]any{}
	maps.Copy(labels, objectAt(request, "labels"))
	for key, value := range a.Labels {
		if value != "" {
			labels[key] = value
		}
	}
	if len(labels) > 0 {
		request["labels"] = labels
	}
}

// preserveMetadata retains external tool metadata unless the manifest manages that map, dropping system and deployer-owned keys.
func preserveMetadata(desired, current map[string]any, owned map[string]string) map[string]any {
	result := maps.Clone(desired)
	for _, field := range []string{"labels", "annotations"} {
		if _, managed := desired[field]; managed {
			continue
		}
		values := map[string]any{}
		for key, value := range objectAt(current, field) {
			_, deployerOwned := owned[key]
			reserved := slices.ContainsFunc(manifest.ReservedMetadataPrefixes, func(prefix string) bool { return strings.HasPrefix(key, prefix) })
			if !reserved && !(field == "labels" && deployerOwned) {
				values[key] = value
			}
		}
		if len(values) > 0 {
			result[field] = values
		}
	}
	if template, ok := desired["template"].(map[string]any); ok {
		result["template"] = preserveMetadata(template, objectAt(current, "template"), owned)
	}
	return result
}

// objectAt returns m[key] as an object, or nil.
func objectAt(m map[string]any, key string) map[string]any {
	value, _ := m[key].(map[string]any)
	return value
}
