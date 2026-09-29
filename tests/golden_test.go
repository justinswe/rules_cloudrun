package tests

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/justinswe/std/errors"
	"github.com/stretchr/testify/require"
)

// listFlag collects a repeated string flag in order.
type listFlag []string

// String joins the collected values.
func (l *listFlag) String() string { return strings.Join(*l, " ") }

// Set appends one value.
func (l *listFlag) Set(value string) error {
	*l = append(*l, value)
	return nil
}

var (
	goldens       listFlag
	readme        = flag.String("readme", "", "Runfiles path of README.md")
	readmeConfigs listFlag
)

func init() {
	flag.Var(&goldens, "golden", "Runfiles paths `actual,expected` of a rendered manifest and its golden; repeatable")
	flag.Var(&readmeConfigs, "readme_config", "Runfiles path of a configuration the README reproduces, in README order; repeatable")
}

// TestGoldens compares rendered manifests with reviewed goldens by JSON value, scalar type, and array order.
func TestGoldens(t *testing.T) {
	require.NotEmpty(t, goldens)
	for _, pair := range goldens {
		actual, expected, ok := strings.Cut(pair, ",")
		require.True(t, ok, "golden %q must be actual,expected", pair)
		t.Run(expected, func(t *testing.T) {
			got, err := canonical(readRunfile(t, actual))
			require.NoError(t, err)
			want, err := canonical(readRunfile(t, expected))
			require.NoError(t, err)
			// Values are omitted to protect configuration data.
			require.True(t, reflect.DeepEqual(want, got), "manifest JSON differs from its golden")
		})
	}
}

// TestCanonical pins which differences the golden comparison ignores.
func TestCanonical(t *testing.T) {
	a, err := canonical([]byte(`{"b":["x"],"a":1}`))
	require.NoError(t, err)
	b, err := canonical([]byte("{\n \"a\": 1, \"b\": [\n\"x\"\n]}"))
	require.NoError(t, err)
	require.Equal(t, a, b)
	for _, pair := range [][2]string{{`false`, `0`}, {`"0"`, `0`}, {`[1,2]`, `[2,1]`}, {`{}`, `{"x":null}`}} {
		x, err := canonical([]byte(pair[0]))
		require.NoError(t, err)
		y, err := canonical([]byte(pair[1]))
		require.NoError(t, err)
		require.False(t, reflect.DeepEqual(x, y), "%s must differ from %s", pair[0], pair[1])
	}
	for _, data := range []string{`{} {}`, `NaN`, `{"a":`} {
		_, err := canonical([]byte(data))
		require.Error(t, err, data)
	}
}

// TestReadme keeps the README's YAML snippets identical to the configurations Bazel builds.
func TestReadme(t *testing.T) {
	text := string(readRunfile(t, *readme))
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(text, -1)
	require.Len(t, blocks, len(readmeConfigs))
	for i, path := range readmeConfigs {
		require.Equal(t, string(readRunfile(t, path)), blocks[i][1], "README YAML block %d differs from %s", i+1, path)
	}
}

// canonical decodes one JSON value, keeping numbers distinct from strings and rejecting trailing data.
func canonical(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.Wrap(err, "decode JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after JSON value")
	}
	return value, nil
}

// readRunfile reads a file by its runfiles path.
func readRunfile(t *testing.T, path string) []byte {
	t.Helper()
	location, err := runfiles.Rlocation(path)
	require.NoError(t, err)
	data, err := os.ReadFile(location)
	require.NoError(t, err)
	return data
}
