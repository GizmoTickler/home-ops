package flatcar

import (
	"fmt"
	"strings"
	"testing"

	"homeops-cli/internal/ssh"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner is a scriptable commandRunner for tests.
type fakeRunner struct {
	connectErr error
	// responder returns (output, error) for a given command.
	responder func(cmd string) (string, error)
	commands  []string
	closed    bool
}

func (f *fakeRunner) Connect() error { return f.connectErr }
func (f *fakeRunner) Close() error   { f.closed = true; return nil }
func (f *fakeRunner) ExecuteCommand(cmd string) (string, error) {
	f.commands = append(f.commands, cmd)
	if f.responder != nil {
		return f.responder(cmd)
	}
	return "", nil
}

const sampleInitOutput = `
Your Kubernetes control-plane has initialized successfully!

[upload-certs] Using certificate key:
abc123def4567890abc123def4567890abc123def4567890abc123def4567890

You can now join any number of control-plane nodes by copying certificate authorities
and service account keys on each node and then running the following as root:

  kubeadm join 192.168.123.253:6443 --token abcdef.0123456789abcdef \
    --discovery-token-ca-cert-hash sha256:1111111111111111111111111111111111111111111111111111111111111111 \
    --control-plane --certificate-key abc123def4567890abc123def4567890abc123def4567890abc123def4567890

Then you can join any number of worker nodes by running the following on each as root:

kubeadm join 192.168.123.253:6443 --token abcdef.0123456789abcdef \
    --discovery-token-ca-cert-hash sha256:1111111111111111111111111111111111111111111111111111111111111111
`

func TestParseKubeadmInitOutput(t *testing.T) {
	res, err := ParseKubeadmInitOutput(sampleInitOutput)
	require.NoError(t, err)
	assert.Equal(t, "abcdef.0123456789abcdef", res.BootstrapToken)
	assert.Equal(t, "sha256:"+strings.Repeat("1", 64), res.CACertHash)
	assert.Equal(t, "abc123def4567890abc123def4567890abc123def4567890abc123def4567890", res.CertificateKey)
}

func TestParseKubeadmInitOutputMissing(t *testing.T) {
	_, err := ParseKubeadmInitOutput("nothing useful here")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func withFakeRunner(t *testing.T, r *fakeRunner) func() {
	t.Helper()
	orig := newCommandRunnerFn
	newCommandRunnerFn = func(_ ssh.SSHConfig) commandRunner { return r }
	return func() { newCommandRunnerFn = orig }
}

func TestInitFirstControlPlane(t *testing.T) {
	r := &fakeRunner{
		responder: func(cmd string) (string, error) {
			if strings.Contains(cmd, "kubeadm init --config") {
				return sampleInitOutput, nil
			}
			return "", nil
		},
	}
	defer withFakeRunner(t, r)()

	o := NewOrchestrator(OrchestratorConfig{SSHUser: "core", SSHItemRef: "op://x/y/key"})
	res, err := o.InitFirstControlPlane("192.168.122.10", "kind: InitConfiguration\n", nil)
	require.NoError(t, err)
	assert.Equal(t, "abcdef.0123456789abcdef", res.BootstrapToken)
	assert.NotEmpty(t, res.CertificateKey)

	// init config was staged before kubeadm init ran.
	var stagedIdx, initIdx = -1, -1
	for i, c := range r.commands {
		if strings.Contains(c, remoteInitConfigPath) && strings.Contains(c, "tee") {
			stagedIdx = i
		}
		if strings.Contains(c, "kubeadm init --config") {
			initIdx = i
		}
	}
	require.GreaterOrEqual(t, stagedIdx, 0)
	require.GreaterOrEqual(t, initIdx, 0)
	assert.Less(t, stagedIdx, initIdx, "config must be staged before kubeadm init")
	assert.Contains(t, r.commands[initIdx], "--upload-certs")
	assert.True(t, r.closed)
}

func TestInitFirstControlPlaneReuploadsCertsWhenMissing(t *testing.T) {
	// init output without a cert key forces an upload-certs phase fallback.
	initNoKey := `kubeadm join 192.168.123.253:6443 --token abcdef.0123456789abcdef \
    --discovery-token-ca-cert-hash sha256:2222222222222222222222222222222222222222222222222222222222222222`
	r := &fakeRunner{
		responder: func(cmd string) (string, error) {
			switch {
			case strings.Contains(cmd, "kubeadm init --config"):
				return initNoKey, nil
			case strings.Contains(cmd, "upload-certs"):
				return "[upload-certs] Using certificate key:\n" + strings.Repeat("f", 64) + "\n", nil
			}
			return "", nil
		},
	}
	defer withFakeRunner(t, r)()

	o := NewOrchestrator(OrchestratorConfig{SSHUser: "core"})
	res, err := o.InitFirstControlPlane("192.168.122.10", "cfg", nil)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("f", 64), res.CertificateKey)
	assert.Equal(t, "abcdef.0123456789abcdef", res.BootstrapToken)
}

func TestJoinControlPlane(t *testing.T) {
	r := &fakeRunner{}
	defer withFakeRunner(t, r)()

	o := NewOrchestrator(OrchestratorConfig{SSHUser: "core"})
	err := o.JoinControlPlane("192.168.122.11", "kind: JoinConfiguration\n")
	require.NoError(t, err)

	joined := false
	for _, c := range r.commands {
		if strings.Contains(c, "kubeadm join --config") {
			joined = true
		}
	}
	assert.True(t, joined)
	assert.True(t, r.closed)
}

func TestFetchAdminKubeconfig(t *testing.T) {
	r := &fakeRunner{
		responder: func(cmd string) (string, error) {
			if strings.Contains(cmd, "admin.conf") {
				return "apiVersion: v1\nkind: Config\nclusters: []\n", nil
			}
			return "", nil
		},
	}
	defer withFakeRunner(t, r)()

	o := NewOrchestrator(OrchestratorConfig{SSHUser: "core"})
	kc, err := o.FetchAdminKubeconfig("192.168.122.10")
	require.NoError(t, err)
	assert.Contains(t, kc, "apiVersion")
}

func TestConnectErrorPropagates(t *testing.T) {
	r := &fakeRunner{connectErr: fmt.Errorf("dial fail")}
	defer withFakeRunner(t, r)()

	o := NewOrchestrator(OrchestratorConfig{SSHUser: "core"})
	_, err := o.InitFirstControlPlane("10.0.0.1", "cfg", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect")
}
