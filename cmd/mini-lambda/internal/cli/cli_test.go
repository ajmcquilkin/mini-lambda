package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
)

func TestParseEnv(t *testing.T) {
	tests := []struct {
		name    string
		pairs   []string
		want    map[string]string
		wantErr string
	}{
		{
			name:  "nil input yields empty map",
			pairs: nil,
			want:  map[string]string{},
		},
		{
			name:  "valid pairs",
			pairs: []string{"FOO=1", "BAR=baz"},
			want:  map[string]string{"FOO": "1", "BAR": "baz"},
		},
		{
			name:  "empty value is allowed",
			pairs: []string{"FOO="},
			want:  map[string]string{"FOO": ""},
		},
		{
			name:  "value may contain equals signs",
			pairs: []string{"URL=a=b=c"},
			want:  map[string]string{"URL": "a=b=c"},
		},
		{
			name:  "duplicate keys: last wins",
			pairs: []string{"FOO=1", "FOO=2"},
			want:  map[string]string{"FOO": "2"},
		},
		{
			name:    "missing equals is rejected",
			pairs:   []string{"FOO"},
			wantErr: `invalid --env "FOO": expected K=V`,
		},
		{
			name:    "empty key is rejected",
			pairs:   []string{"=value"},
			wantErr: `invalid --env "=value": expected K=V`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEnv(tt.pairs)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.EqualError(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultHost(t *testing.T) {
	t.Run("falls back to built-in default", func(t *testing.T) {
		t.Setenv("MINI_LAMBDA_HOST", "")
		assert.Equal(t, "127.0.0.1:9000", DefaultHost())
	})

	t.Run("uses MINI_LAMBDA_HOST when set", func(t *testing.T) {
		t.Setenv("MINI_LAMBDA_HOST", "10.0.0.1:1234")
		assert.Equal(t, "10.0.0.1:1234", DefaultHost())
	})
}

func TestResolveHostPrecedence(t *testing.T) {
	t.Run("explicit flag wins over env and default", func(t *testing.T) {
		t.Setenv("MINI_LAMBDA_HOST", "10.0.0.1:1234")
		cmd := &cobra.Command{}
		AddHostFlag(cmd)
		require.NoError(t, cmd.Flags().Set(HostFlag, "explicit:9999"))
		assert.Equal(t, "explicit:9999", ResolveHost(cmd))
	})

	t.Run("env wins over default when flag unset", func(t *testing.T) {
		t.Setenv("MINI_LAMBDA_HOST", "10.0.0.1:1234")
		cmd := &cobra.Command{}
		AddHostFlag(cmd)
		assert.Equal(t, "10.0.0.1:1234", ResolveHost(cmd))
	})

	t.Run("default when neither flag nor env set", func(t *testing.T) {
		t.Setenv("MINI_LAMBDA_HOST", "")
		cmd := &cobra.Command{}
		AddHostFlag(cmd)
		assert.Equal(t, "127.0.0.1:9000", ResolveHost(cmd))
	})
}

func renderCfg() *api.FunctionConfiguration {
	ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	return &api.FunctionConfiguration{
		FunctionName: "hello",
		FunctionArn:  "arn:aws:lambda:us-east-1:000000000000:function:hello",
		Code:         api.Code{ImageUri: "public.ecr.aws/img:latest"},
		Environment:  &api.Environment{Variables: map[string]string{"FOO": "1", "BAR": "2"}},
		MemorySize:   128,
		Timeout:      3,
		CreatedAt:    ts,
		LastModified: ts,
	}
}

func TestRenderFunctionTable(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	require.NoError(t, RenderFunction(cmd, "table", renderCfg()))

	// tabwriter pads the first column to the longest label ("LAST MODIFIED",
	// 13 chars) plus 2 padding = width 15. ENV rows are sorted by key.
	pad := func(label string) string { return label + strings.Repeat(" ", 15-len(label)) }
	want := pad("NAME") + "hello\n" +
		pad("ARN") + "arn:aws:lambda:us-east-1:000000000000:function:hello\n" +
		pad("IMAGE") + "public.ecr.aws/img:latest\n" +
		pad("MEMORY") + "128MB\n" +
		pad("TIMEOUT") + "3s\n" +
		pad("ENV") + "BAR=2\n" +
		pad("ENV") + "FOO=1\n" +
		pad("LAST MODIFIED") + "2024-01-02T03:04:05Z\n"

	assert.Equal(t, want, buf.String())
}

func TestRenderFunctionJSON(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	require.NoError(t, RenderFunction(cmd, "json", renderCfg()))

	want := `{
  "FunctionName": "hello",
  "FunctionArn": "arn:aws:lambda:us-east-1:000000000000:function:hello",
  "Code": {
    "ImageUri": "public.ecr.aws/img:latest"
  },
  "Environment": {
    "Variables": {
      "BAR": "2",
      "FOO": "1"
    }
  },
  "MemorySize": 128,
  "Timeout": 3,
  "CreatedAt": "2024-01-02T03:04:05Z",
  "LastModified": "2024-01-02T03:04:05Z"
}
`
	assert.Equal(t, want, buf.String())
}
