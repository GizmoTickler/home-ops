package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/common"
	"homeops-cli/internal/provider"
)

// This harness runs the REAL request paths (go-proxmox over HTTP) against an
// httptest server speaking the minimal Proxmox API, so the day-2 methods
// (config, resize, reboot, snapshots, clone, guest agent) are tested without
// any fn-field seams.

const testUPID = "UPID:pve:00001234:00000000:00000000:qmconfig:100:root@pam:"

// muxTransport serves the given handler entirely in-process, so the harness
// exercises the real go-proxmox request/response paths without binding a TCP
// port. This keeps the tests hermetic and lets them run in sandboxed or CI
// environments that disallow listening on a loopback socket.
type muxTransport struct{ handler http.Handler }

func (t muxTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	// A real HTTP server never hands a handler a nil Body; mirror that so
	// handlers can call req.Body.Read without a nil-pointer panic.
	if req.Body == nil {
		req.Body = http.NoBody
	}
	t.handler.ServeHTTP(rec, req)
	res := rec.Result()
	res.Request = req
	return res, nil
}

// apiRecorder captures the mutating requests the manager sends.
type apiRecorder struct {
	requests []string // "METHOD path"
	bodies   map[string]string
}

func (r *apiRecorder) record(req *http.Request) {
	key := req.Method + " " + req.URL.Path
	r.requests = append(r.requests, key)
}

func writeData(w http.ResponseWriter, v interface{}) {
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": v})
}

// newAPIManager builds a VMManager against an httptest Proxmox API with one
// VM (web0, VMID 100). extra handles routes before the defaults.
func newAPIManager(t *testing.T, extra func(w http.ResponseWriter, r *http.Request, rec *apiRecorder) bool) (*VMManager, *apiRecorder) {
	t.Helper()
	rec := &apiRecorder{bodies: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if extra != nil && extra(w, r, rec) {
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		switch {
		case path == "/version":
			writeData(w, map[string]string{"version": "8.4.1", "release": "8.4", "repoid": "test"})
		case path == "/nodes/pve/status":
			writeData(w, map[string]interface{}{"uptime": 1})
		case path == "/nodes/pve/qemu" && r.Method == http.MethodGet:
			writeData(w, []map[string]interface{}{
				{"vmid": 100, "name": "web0", "status": "running", "maxmem": 4294967296, "cpus": 2, "uptime": 42},
			})
		case path == "/nodes/pve/qemu/100/status/current":
			writeData(w, map[string]interface{}{"vmid": 100, "name": "web0", "status": "running", "maxmem": 4294967296, "cpus": 2})
		case path == "/nodes/pve/qemu/100/config" && r.Method == http.MethodGet:
			writeData(w, map[string]interface{}{"name": "web0", "cores": 2, "sockets": 1})
		case strings.HasPrefix(path, "/nodes/pve/tasks/") && strings.HasSuffix(path, "/status"):
			writeData(w, map[string]interface{}{"status": "stopped", "exitstatus": "OK", "upid": testUPID, "node": "pve"})
		default:
			http.NotFound(w, r)
		}
	})

	pmx := proxmox.NewClient("http://proxmox.test/api2/json",
		proxmox.WithHTTPClient(&http.Client{Transport: muxTransport{handler: mux}}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	node, err := pmx.Node(ctx, "pve")
	require.NoError(t, err)

	manager := &VMManager{
		client:   &Client{client: pmx, node: node, ctx: ctx, cancel: cancel, logger: common.NewColorLogger()},
		logger:   common.NewColorLogger(),
		host:     "pve.test",
		nodeName: "pve",
	}
	return manager, rec
}

// taskResponder answers a mutating endpoint with a task UPID and lets the
// task poll complete.
func taskResponder(method, path string) func(w http.ResponseWriter, r *http.Request, rec *apiRecorder) bool {
	return func(w http.ResponseWriter, r *http.Request, rec *apiRecorder) bool {
		p := strings.TrimPrefix(r.URL.Path, "/api2/json")
		if r.Method == method && p == path {
			body := make([]byte, 4096)
			n, _ := r.Body.Read(body)
			rec.bodies[method+" "+p] = string(body[:n])
			writeData(w, testUPID)
			return true
		}
		return false
	}
}

func TestSetVMResourcesOverAPI(t *testing.T) {
	manager, rec := newAPIManager(t, taskResponder(http.MethodPost, "/nodes/pve/qemu/100/config"))
	require.NoError(t, manager.SetVMResources("web0", 8192, 4))

	body := rec.bodies["POST /nodes/pve/qemu/100/config"]
	assert.Contains(t, body, `"memory":8192`)
	assert.Contains(t, body, `"cores":4`)
	assert.Contains(t, strings.Join(rec.requests, "\n"), "GET /api2/json/nodes/pve/tasks/", "must wait for the config task")
}

func TestSetVMResourcesValidation(t *testing.T) {
	manager, _ := newAPIManager(t, nil)
	require.ErrorContains(t, manager.SetVMResources("", 1024, 0), "VM name is required")
	require.ErrorContains(t, manager.SetVMResources("web0", 0, 0), "nothing to change")
	require.ErrorContains(t, manager.SetVMResources("web0", -1024, 0), "memory must not be negative")
	require.ErrorContains(t, manager.SetVMResources("web0", 0, -1), "cores must not be negative")
	require.ErrorContains(t, manager.SetVMResources("ghost", 1024, 0), "not found")
}

func TestDay2Validation(t *testing.T) {
	manager, _ := newAPIManager(t, nil)

	require.ErrorContains(t, manager.ResizeVMDisk("", "", "+20G"), "VM name is required")
	require.ErrorContains(t, manager.ResizeVMDisk("web0", "", ""), "disk size is required")
	require.ErrorContains(t, manager.RestartVM(""), "VM name is required")
	require.ErrorContains(t, manager.SnapshotVM("", "pre-upgrade"), "VM name is required")
	require.ErrorContains(t, manager.SnapshotVM("web0", ""), "snapshot name is required")
	require.ErrorContains(t, manager.RollbackVM("", "pre-upgrade"), "VM name is required")
	require.ErrorContains(t, manager.RollbackVM("web0", ""), "snapshot name is required")
	require.ErrorContains(t, manager.DeleteVMSnapshot("", "pre-upgrade"), "VM name is required")
	require.ErrorContains(t, manager.DeleteVMSnapshot("web0", ""), "snapshot name is required")
	require.ErrorContains(t, manager.CloneVM("", "web1", 0, true), "VM name is required")
	require.ErrorContains(t, manager.CloneVM("web0", "", 0, true), "target VM name is required")
	_, ipErr := manager.VMIPAddresses("")
	require.ErrorContains(t, ipErr, "VM name is required")
}

func TestResizeVMDiskOverAPI(t *testing.T) {
	manager, rec := newAPIManager(t, taskResponder(http.MethodPut, "/nodes/pve/qemu/100/resize"))
	require.NoError(t, manager.ResizeVMDisk("web0", "", "+20G"))

	body := rec.bodies["PUT /nodes/pve/qemu/100/resize"]
	assert.Contains(t, body, `"disk":"scsi0"`, "empty selector must default to the boot disk")
	assert.Contains(t, body, `"size":"+20G"`)
}

func TestRestartVMOverAPI(t *testing.T) {
	manager, rec := newAPIManager(t, taskResponder(http.MethodPost, "/nodes/pve/qemu/100/status/reboot"))
	require.NoError(t, manager.RestartVM("web0"))
	assert.Contains(t, rec.bodies, "POST /nodes/pve/qemu/100/status/reboot")
}

func TestSnapshotLifecycleOverAPI(t *testing.T) {
	handlers := func(w http.ResponseWriter, r *http.Request, rec *apiRecorder) bool {
		p := strings.TrimPrefix(r.URL.Path, "/api2/json")
		switch {
		case r.Method == http.MethodPost && p == "/nodes/pve/qemu/100/snapshot":
			writeData(w, testUPID)
			return true
		case r.Method == http.MethodGet && p == "/nodes/pve/qemu/100/snapshot":
			writeData(w, []map[string]interface{}{
				{"name": "pre-upgrade", "snaptime": 1765500000, "description": "before"},
				{"name": "current", "description": "You are here!"},
			})
			return true
		case r.Method == http.MethodPost && p == "/nodes/pve/qemu/100/snapshot/pre-upgrade/rollback":
			writeData(w, testUPID)
			return true
		case r.Method == http.MethodDelete && p == "/nodes/pve/qemu/100/snapshot/pre-upgrade":
			writeData(w, testUPID)
			return true
		}
		return false
	}

	manager, rec := newAPIManager(t, handlers)
	require.NoError(t, manager.SnapshotVM("web0", "pre-upgrade"))
	require.NoError(t, manager.ListVMSnapshots("web0"))
	require.NoError(t, manager.RollbackVM("web0", "pre-upgrade"))
	require.NoError(t, manager.DeleteVMSnapshot("web0", "pre-upgrade"))

	joined := strings.Join(rec.requests, "\n")
	assert.Contains(t, joined, "POST /api2/json/nodes/pve/qemu/100/snapshot")
	assert.Contains(t, joined, "POST /api2/json/nodes/pve/qemu/100/snapshot/pre-upgrade/rollback")
	assert.Contains(t, joined, "DELETE /api2/json/nodes/pve/qemu/100/snapshot/pre-upgrade")
}

func TestCloneVMOverAPI(t *testing.T) {
	manager, rec := newAPIManager(t, taskResponder(http.MethodPost, "/nodes/pve/qemu/100/clone"))
	require.NoError(t, manager.Clone("web0", "web1", provider.CloneOptions{VMID: 777}))

	body := rec.bodies["POST /nodes/pve/qemu/100/clone"]
	assert.Contains(t, body, `"newid":777`)
	assert.Contains(t, body, `"name":"web1"`)
	assert.Contains(t, body, `"full":1`, "default clone must be a full clone")
}

func TestVMIPAddressesOverAPI(t *testing.T) {
	manager, _ := newAPIManager(t, func(w http.ResponseWriter, r *http.Request, rec *apiRecorder) bool {
		p := strings.TrimPrefix(r.URL.Path, "/api2/json")
		if r.Method == http.MethodGet && p == "/nodes/pve/qemu/100/agent/network-get-interfaces" {
			writeData(w, map[string]interface{}{"result": []map[string]interface{}{
				{"name": "lo", "ip-addresses": []map[string]interface{}{{"ip-address": "127.0.0.1", "ip-address-type": "ipv4"}}},
				{"name": "eth0", "ip-addresses": []map[string]interface{}{
					{"ip-address": "192.168.120.50", "ip-address-type": "ipv4"},
					{"ip-address": "fe80::1", "ip-address-type": "ipv6"},
				}},
			}})
			return true
		}
		return false
	})

	ips, err := manager.VMIPAddresses("web0")
	require.NoError(t, err)
	assert.Equal(t, []string{"192.168.120.50"}, ips)
}

func TestVMSummariesOverAPI(t *testing.T) {
	manager, _ := newAPIManager(t, nil)
	summaries, err := manager.VMSummaries()
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "web0", summaries[0].Name)
	assert.Equal(t, "100", summaries[0].ID)
	assert.Equal(t, "running", summaries[0].Status)
	assert.Equal(t, 4096, summaries[0].MemoryMB)
	assert.Equal(t, 2, summaries[0].CPUs)
}

func TestCapabilitiesProxmoxIsComplete(t *testing.T) {
	manager, _ := newAPIManager(t, nil)
	assert.Empty(t, manager.Capabilities(), "proxmox supports every vm feature")
}

func TestConvertVMToTemplateOverAPI(t *testing.T) {
	manager, rec := newAPIManager(t, func(w http.ResponseWriter, r *http.Request, rec *apiRecorder) bool {
		p := strings.TrimPrefix(r.URL.Path, "/api2/json")
		if r.Method == http.MethodPost && p == "/nodes/pve/qemu/100/template" {
			writeData(w, testUPID)
			return true
		}
		return false
	})
	require.NoError(t, manager.ConvertVMToTemplate("web0"))
	assert.Contains(t, strings.Join(rec.requests, "\n"), "POST /api2/json/nodes/pve/qemu/100/template")
}
