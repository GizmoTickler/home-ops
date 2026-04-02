package talos

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/common"
)

func TestFactoryClientLoadAndValidateSchematic(t *testing.T) {
	client := NewFactoryClient()

	config, err := client.LoadSchematicFromTemplate()
	require.NoError(t, err)
	require.NotNil(t, config)
	require.NotEmpty(t, config.Customization.SystemExtensions.OfficialExtensions)
	require.NoError(t, client.ValidateSchematic(config))

	dir := t.TempDir()
	schematicPath := filepath.Join(dir, "schematic.yaml")
	require.NoError(t, os.WriteFile(schematicPath, []byte("customization:\n  extraKernelArgs:\n    - console=ttyS0\n"), 0o644))

	fileConfig, err := client.LoadSchematicFromFile(schematicPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"console=ttyS0"}, fileConfig.Customization.ExtraKernelArgs)

	require.Error(t, client.ValidateSchematic(nil))
	require.Error(t, client.ValidateSchematic(&SchematicConfig{
		Customization: struct {
			ExtraKernelArgs  []string `yaml:"extraKernelArgs" json:"extraKernelArgs"`
			SystemExtensions struct {
				OfficialExtensions []string `yaml:"officialExtensions" json:"officialExtensions"`
			} `yaml:"systemExtensions" json:"systemExtensions"`
		}{
			SystemExtensions: struct {
				OfficialExtensions []string `yaml:"officialExtensions" json:"officialExtensions"`
			}{OfficialExtensions: []string{""}},
		},
	}))
}

func TestFactoryClientISOValidationAndCache(t *testing.T) {
	client := NewFactoryClient()
	client.cacheDir = t.TempDir()

	req := ISOGenerationRequest{
		SchematicID:  "schematic12345",
		TalosVersion: "v1.2.3",
		Architecture: "amd64",
		Platform:     "metal",
	}

	require.NoError(t, client.validateISORequest(req))
	assert.Equal(t, client.generateHash(req), client.generateHash(req))

	isoInfo := &ISOInfo{
		URL:          "https://factory.talos.dev/image/test",
		SchematicID:  req.SchematicID,
		TalosVersion: req.TalosVersion,
		Hash:         client.generateHash(req),
	}
	require.NoError(t, client.cacheISOInfo(isoInfo))
	assert.FileExists(t, isoInfo.CacheFile)

	cached, found := client.checkCache(req)
	require.True(t, found)
	assert.Equal(t, isoInfo.URL, cached.URL)

	require.NoError(t, client.ClearCache())
	_, found = client.checkCache(req)
	assert.False(t, found)
}

func TestFactoryClientResponseValidation(t *testing.T) {
	client := NewFactoryClient()

	assert.True(t, isValidKernelFlag("quiet"))
	assert.True(t, isValidKernelFlag("pciPassthrough"))
	assert.False(t, isValidKernelFlag("not-valid"))

	require.NoError(t, client.ValidateAPIResponse(&http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, "application/json"))
	require.Error(t, client.ValidateAPIResponse(nil, "application/json"))
	require.Error(t, client.ValidateAPIResponse(&http.Response{
		StatusCode: http.StatusBadRequest,
		Status:     "400 Bad Request",
		Header:     http.Header{},
	}, "application/json"))

	require.NoError(t, client.ValidateSchematicResponse(&SchematicResponse{ID: "1234567890abcdef"}))
	require.Error(t, client.ValidateSchematicResponse(nil))
	require.Error(t, client.ValidateSchematicResponse(&SchematicResponse{ID: "short"}))
}

func TestFactoryClientGenerateISOAndGetNodeIPsFallback(t *testing.T) {
	client := NewFactoryClient()
	client.cacheDir = t.TempDir()
	client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
			Body:       http.NoBody,
		}, nil
	})}

	req := ISOGenerationRequest{
		SchematicID:  "schematic12345",
		TalosVersion: "v1.2.3",
		Architecture: "amd64",
		Platform:     "metal",
	}
	isoInfo, err := client.GenerateISO(req)
	require.NoError(t, err)
	assert.Contains(t, isoInfo.URL, req.SchematicID)

	_, err = client.GenerateISOFromSchematic(nil, "v1.2.3", "amd64", "metal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schematic config is nil")

	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)
	_, err = GetNodeIPs()
	require.Error(t, err)
}

func TestFactoryClientCreateSchematic(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var requestBody []byte
		client := NewFactoryClient()
		client.baseURL = "https://factory.test"
		client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/schematics", r.URL.Path)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			requestBody = body
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"1234567890abcdef"}`)),
			}, nil
		})}

		config := &SchematicConfig{}
		config.Customization.ExtraKernelArgs = []string{"console=ttyS0"}
		config.Customization.SystemExtensions.OfficialExtensions = []string{"siderolabs/iscsi-tools"}

		resp, err := client.CreateSchematic(config, "v1.10.0")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "1234567890abcdef", resp.ID)
		assert.Contains(t, string(requestBody), "console=ttyS0")
		assert.Contains(t, string(requestBody), "siderolabs/iscsi-tools")
	})

	t.Run("api validation failure includes response body", func(t *testing.T) {
		client := NewFactoryClient()
		client.baseURL = "https://factory.test"
		client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Status:     "400 Bad Request",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"error":"bad schematic"}`)),
			}, nil
		})}

		config := &SchematicConfig{}
		config.Customization.SystemExtensions.OfficialExtensions = []string{"siderolabs/iscsi-tools"}

		_, err := client.CreateSchematic(config, "v1.10.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "API validation failed")
		assert.Contains(t, err.Error(), "bad schematic")
	})

	t.Run("invalid response payload", func(t *testing.T) {
		client := NewFactoryClient()
		client.baseURL = "https://factory.test"
		client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"short"}`)),
			}, nil
		})}

		config := &SchematicConfig{}
		config.Customization.SystemExtensions.OfficialExtensions = []string{"siderolabs/iscsi-tools"}

		_, err := client.CreateSchematic(config, "v1.10.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "schematic response validation failed")
	})
}

func TestFactoryClientGenerateISOFromSchematic(t *testing.T) {
	var posted bytes.Buffer
	client := NewFactoryClient()
	client.baseURL = "https://factory.test"
	client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/schematics":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			posted.Write(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"1234567890abcdef"}`)),
			}, nil
		case r.Method == http.MethodHead && r.URL.Path == "/image/1234567890abcdef/v1.10.0/metal-amd64.iso":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
				Body:       http.NoBody,
			}, nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})}
	client.cacheDir = t.TempDir()

	config := &SchematicConfig{}
	config.Customization.ExtraKernelArgs = []string{"console=ttyS0"}
	config.Customization.SystemExtensions.OfficialExtensions = []string{"siderolabs/iscsi-tools"}

	isoInfo, err := client.GenerateISOFromSchematic(config, "v1.10.0", "amd64", "metal")
	require.NoError(t, err)
	require.NotNil(t, isoInfo)
	assert.Equal(t, "1234567890abcdef", isoInfo.SchematicID)
	assert.Contains(t, isoInfo.URL, "/image/1234567890abcdef/v1.10.0/metal-amd64.iso")
	assert.FileExists(t, isoInfo.CacheFile)
	assert.Contains(t, posted.String(), "console=ttyS0")
}

func TestFactoryClientGenerateISOFromSchematicErrors(t *testing.T) {
	t.Run("missing inputs", func(t *testing.T) {
		client := NewFactoryClient()
		config := &SchematicConfig{}
		config.Customization.SystemExtensions.OfficialExtensions = []string{"siderolabs/iscsi-tools"}

		_, err := client.GenerateISOFromSchematic(config, "", "amd64", "metal")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "talos version is required")

		_, err = client.GenerateISOFromSchematic(config, "v1.10.0", "", "metal")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "architecture is required")

		_, err = client.GenerateISOFromSchematic(config, "v1.10.0", "amd64", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "platform is required")
	})

	t.Run("create schematic failure", func(t *testing.T) {
		client := NewFactoryClient()
		client.baseURL = "https://factory.test"
		client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Status:     "500 Internal Server Error",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString("boom")),
			}, nil
		})}

		config := &SchematicConfig{}
		config.Customization.SystemExtensions.OfficialExtensions = []string{"siderolabs/iscsi-tools"}

		_, err := client.GenerateISOFromSchematic(config, "v1.10.0", "amd64", "metal")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create schematic")
	})
}

func TestParseTalosMemberIPs(t *testing.T) {
	t.Run("json lines", func(t *testing.T) {
		output := []byte("{\"spec\":{\"addresses\":[\"10.0.0.2\",\"10.0.0.1\"]}}\n{\"spec\":{\"addresses\":[\"10.0.0.1\",\"10.0.0.3\"]}}\n")
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, parseTalosMemberIPs(output))
	})

	t.Run("json array", func(t *testing.T) {
		output := []byte("[{\"spec\":{\"addresses\":[\"10.0.0.3\"]}},{\"spec\":{\"addresses\":[\"10.0.0.2\",\"10.0.0.2\"]}}]")
		assert.Equal(t, []string{"10.0.0.2", "10.0.0.3"}, parseTalosMemberIPs(output))
	})

	t.Run("invalid", func(t *testing.T) {
		assert.Nil(t, parseTalosMemberIPs([]byte("not-json")))
	})
}

func TestParseTalosConfigEndpoints(t *testing.T) {
	output := []byte(`{"endpoints":["10.0.0.2","10.0.0.1","10.0.0.2"]}`)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, parseTalosConfigEndpoints(output))
	assert.Nil(t, parseTalosConfigEndpoints([]byte("bad-json")))
}

func TestGetNodeIPs(t *testing.T) {
	t.Run("uses members output", func(t *testing.T) {
		restore := common.SetCommandFactoryForTesting(func(name string, args ...string) *exec.Cmd {
			switch {
			case len(args) >= 2 && args[0] == "get" && args[1] == "members":
				return exec.Command("bash", "-c", "printf '{\"spec\":{\"addresses\":[\"10.0.0.2\",\"10.0.0.1\"]}}\\n'")
			case len(args) >= 2 && args[0] == "config" && args[1] == "info":
				return exec.Command("bash", "-c", "printf '{\"endpoints\":[\"10.0.0.9\"]}'")
			default:
				return exec.Command("bash", "-c", "exit 1")
			}
		})
		defer restore()

		ips, err := GetNodeIPs()
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, ips)
	})

	t.Run("falls back to config info", func(t *testing.T) {
		restore := common.SetCommandFactoryForTesting(func(name string, args ...string) *exec.Cmd {
			switch {
			case len(args) >= 2 && args[0] == "get" && args[1] == "members":
				return exec.Command("bash", "-c", "printf 'invalid'")
			case len(args) >= 2 && args[0] == "config" && args[1] == "info":
				return exec.Command("bash", "-c", "printf '{\"endpoints\":[\"10.0.0.5\",\"10.0.0.4\"]}'")
			default:
				return exec.Command("bash", "-c", "exit 1")
			}
		})
		defer restore()

		ips, err := GetNodeIPs()
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.4", "10.0.0.5"}, ips)
	})
}

func TestFactoryClientLoadErrors(t *testing.T) {
	client := NewFactoryClient()
	_, err := client.LoadSchematicFromFile(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)

	dir := t.TempDir()
	file := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(file, []byte("not: [yaml"), 0o644))
	_, err = client.LoadSchematicFromFile(file)
	require.Error(t, err)

	_, err = json.Marshal(SchematicResponse{ID: "abc"})
	require.NoError(t, err)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
