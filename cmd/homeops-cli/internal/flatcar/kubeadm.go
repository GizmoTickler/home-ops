package flatcar

import (
	"fmt"
	"regexp"
	"strings"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/ssh"
)

// pkiBlobs are the kubeadm CA/identity files restored onto node0 before
// `kubeadm init`. kubeadm reuses any CA it finds in --cert-dir, so laying these
// down first yields a stable cluster identity across rebuilds. *.key are 0600.
var pkiBlobs = []struct {
	path  string // relative to /etc/kubernetes/pki
	field string // 1Password field label
	opRef string // op:// reference for that field
	mode  string // file mode when restored
}{
	{"ca.crt", "ca_crt", constants.OpPKICACrt, "0644"},
	{"ca.key", "ca_key", constants.OpPKICAKey, "0600"},
	{"sa.pub", "sa_pub", constants.OpPKISAPub, "0644"},
	{"sa.key", "sa_key", constants.OpPKISAKey, "0600"},
	{"front-proxy-ca.crt", "front_proxy_ca_crt", constants.OpPKIFrontProxyCACrt, "0644"},
	{"front-proxy-ca.key", "front_proxy_ca_key", constants.OpPKIFrontProxyCAKey, "0600"},
	{"etcd/ca.crt", "etcd_ca_crt", constants.OpPKIEtcdCACrt, "0644"},
	{"etcd/ca.key", "etcd_ca_key", constants.OpPKIEtcdCAKey, "0600"},
}

// CapturePKI reads the cluster PKI identity set (CA/SA/front-proxy/etcd CA) from a
// control-plane node over SSH and returns it as 1Password-field -> base64 content,
// ready to persist with `flatcar save-pki`. Used to seed/rotate the persisted PKI
// that provisionPKI restores. Leaf certs are intentionally not captured (kubeadm
// regenerates them off the CAs).
func (o *Orchestrator) CapturePKI(node0IP string) (map[string]string, error) {
	runner := o.runnerFor(node0IP)
	if err := runner.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", node0IP, err)
	}
	defer func() { _ = runner.Close() }()

	out := make(map[string]string, len(pkiBlobs))
	for _, b := range pkiBlobs {
		v, err := runner.ExecuteCommand("sudo base64 -w0 /etc/kubernetes/pki/" + b.path)
		if err != nil {
			return nil, fmt.Errorf("read /etc/kubernetes/pki/%s on %s: %w", b.path, node0IP, err)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, fmt.Errorf("PKI file /etc/kubernetes/pki/%s on %s came back empty", b.path, node0IP)
		}
		out[b.field] = v
	}
	return out, nil
}

// pkiSecretFn fetches a base64 PKI blob from 1Password (silent ""-on-miss).
// Swappable for tests.
var pkiSecretFn = common.Get1PasswordSecretSilent

// commandRunner is the minimal SSH surface kubeadm orchestration needs. The real
// implementation is *ssh.SSHClient; tests inject a fake.
type commandRunner interface {
	Connect() error
	Close() error
	ExecuteCommand(command string) (string, error)
}

// newCommandRunnerFn builds a commandRunner for a node. Swappable for tests.
var newCommandRunnerFn = func(config ssh.SSHConfig) commandRunner {
	return ssh.NewSSHClient(config)
}

// writeFileFn writes a rendered config to a remote path over the runner before
// kubeadm consumes it. Kept as a var so tests can assert on it. The default uses a
// heredoc via the runner's shell.
var writeRemoteFileFn = func(runner commandRunner, remotePath, content string) error {
	// Use a quoted heredoc so the content is written verbatim (no shell expansion).
	cmd := fmt.Sprintf("sudo mkdir -p %s && sudo tee %s > /dev/null <<'HOMEOPS_EOF'\n%s\nHOMEOPS_EOF",
		shellDir(remotePath), remotePath, content)
	_, err := runner.ExecuteCommand(cmd)
	return err
}

func shellDir(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return "/"
	}
	return p[:idx]
}

// nodeAlreadyJoinedFn reports whether a node is already a kubeadm cluster member.
// kubeadm writes /etc/kubernetes/kubelet.conf on a successful init/join, so its
// presence means the node has joined. Swappable for tests. On any probe error it
// returns false so the join proceeds and kubeadm's own preflight is the backstop.
var nodeAlreadyJoinedFn = func(runner commandRunner) bool {
	out, err := runner.ExecuteCommand("sudo test -f /etc/kubernetes/kubelet.conf && echo JOINED || echo ABSENT")
	if err != nil {
		return false
	}
	return strings.Contains(out, "JOINED")
}

// KubeadmResult carries the join material extracted from `kubeadm init`.
type KubeadmResult struct {
	BootstrapToken string
	CACertHash     string
	CertificateKey string
}

// Orchestrator drives kubeadm over SSH against the Flatcar control-plane nodes.
type Orchestrator struct {
	sshUser    string
	sshItemRef string
	port       string
	freshPKI   bool // when true, skip the persisted-PKI restore (generate a new CA)
	logger     *common.ColorLogger
}

// OrchestratorConfig configures an Orchestrator.
type OrchestratorConfig struct {
	SSHUser    string // username for node SSH
	SSHItemRef string // 1Password SSH item reference (op:// path)
	Port       string // SSH port (default 22)
	FreshPKI   bool   // skip the persisted-PKI restore and let kubeadm mint a new CA
}

// NewOrchestrator builds an Orchestrator.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	port := cfg.Port
	if port == "" {
		port = "22"
	}
	return &Orchestrator{
		sshUser:    cfg.SSHUser,
		sshItemRef: cfg.SSHItemRef,
		port:       port,
		freshPKI:   cfg.FreshPKI,
		logger:     common.NewColorLogger(),
	}
}

// provisionPKI restores the persisted cluster PKI (CA + SA + front-proxy + etcd CA)
// from 1Password onto node0 BEFORE `kubeadm init`, so a rebuilt cluster keeps a
// stable identity (existing kubeconfigs + ServiceAccount tokens stay valid).
// kubeadm reuses any CA present in /etc/kubernetes/pki. No-op when --fresh-pki is
// set or 1Password holds no material (first-ever bootstrap) — kubeadm then mints
// a fresh CA. base64 is decoded on the node.
func (o *Orchestrator) provisionPKI(runner commandRunner) error {
	if o.freshPKI {
		o.logger.Warn("--fresh-pki: skipping PKI restore; kubeadm will generate a NEW cluster CA (existing kubeconfigs will break)")
		return nil
	}
	blobs := make(map[string]string, len(pkiBlobs))
	for _, b := range pkiBlobs {
		if v := strings.TrimSpace(pkiSecretFn(b.opRef)); v != "" {
			blobs[b.path] = v
		}
	}
	if blobs["ca.crt"] == "" || blobs["ca.key"] == "" {
		o.logger.Warn("No persisted cluster PKI in 1Password (%s) — kubeadm will generate a fresh CA", constants.OpPKICACrt)
		return nil
	}
	o.logger.Info("Restoring persisted cluster PKI to /etc/kubernetes/pki (stable identity across rebuilds)")
	if _, err := runner.ExecuteCommand("sudo mkdir -p /etc/kubernetes/pki/etcd"); err != nil {
		return fmt.Errorf("mkdir /etc/kubernetes/pki: %w", err)
	}
	for _, b := range pkiBlobs {
		v, ok := blobs[b.path]
		if !ok {
			continue
		}
		dst := "/etc/kubernetes/pki/" + b.path
		// base64 alphabet has no shell metacharacters; single-quote defensively.
		cmd := fmt.Sprintf("printf '%%s' '%s' | base64 -d | sudo tee %s >/dev/null && sudo chmod %s %s && sudo chown root:root %s",
			v, dst, b.mode, dst, dst)
		if _, err := runner.ExecuteCommand(cmd); err != nil {
			return fmt.Errorf("restore %s: %w", dst, err)
		}
	}
	return nil
}

func (o *Orchestrator) runnerFor(host string) commandRunner {
	return newCommandRunnerFn(ssh.SSHConfig{
		Host:       host,
		Username:   o.sshUser,
		Port:       o.port,
		SSHItemRef: o.sshItemRef,
	})
}

// remoteInitConfigPath / remoteJoinConfigPath are where rendered configs land on nodes.
const (
	remoteInitConfigPath = "/etc/kubernetes/homeops/kubeadm-init.yaml"
	remoteJoinConfigPath = "/etc/kubernetes/homeops/kubeadm-join.yaml"
	remoteAdminConfPath  = "/etc/kubernetes/admin.conf"
)

// InitFirstControlPlane stages the init config on node0, runs `kubeadm init
// --upload-certs`, and extracts the bootstrap token, CA cert hash and certificate
// key for subsequent joins. skipPhases is forwarded to --skip-phases when non-empty
// (the templates already skip kube-proxy/coredns, so this is usually empty).
func (o *Orchestrator) InitFirstControlPlane(node0IP, initConfig string, skipPhases []string) (*KubeadmResult, error) {
	runner := o.runnerFor(node0IP)
	if err := runner.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to node0 %s: %w", node0IP, err)
	}
	defer func() { _ = runner.Close() }()

	// Restore the persisted cluster PKI before init so the cluster identity is
	// stable across rebuilds (kubeadm reuses an existing CA). No-op for fresh-pki.
	if err := o.provisionPKI(runner); err != nil {
		return nil, fmt.Errorf("failed to restore cluster PKI on %s: %w", node0IP, err)
	}

	if err := writeRemoteFileFn(runner, remoteInitConfigPath, initConfig); err != nil {
		return nil, fmt.Errorf("failed to stage kubeadm init config on %s: %w", node0IP, err)
	}

	cmd := fmt.Sprintf("sudo kubeadm init --config %s --upload-certs --ignore-preflight-errors=DirAvailable--etc-kubernetes-manifests", remoteInitConfigPath)
	if len(skipPhases) > 0 {
		cmd += " --skip-phases=" + strings.Join(skipPhases, ",")
	}
	o.logger.Info("Running kubeadm init on %s", node0IP)
	out, err := runner.ExecuteCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("kubeadm init failed on %s: %w\n%s", node0IP, err, common.RedactCommandOutput(out))
	}

	result, parseErr := ParseKubeadmInitOutput(out)
	if result == nil {
		result = &KubeadmResult{}
	}

	// Token and CA hash are mandatory and only come from init output.
	if parseErr != nil && (result.BootstrapToken == "" || result.CACertHash == "") {
		return nil, fmt.Errorf("kubeadm init output missing token/CA hash: %w", parseErr)
	}

	// The control-plane certificate key is only printed when --upload-certs runs; if
	// init output didn't surface it (newer/older formats vary), recover it via the
	// dedicated upload-certs phase.
	if result.CertificateKey == "" {
		key, kerr := o.uploadCerts(runner)
		if kerr != nil {
			return nil, fmt.Errorf("failed to obtain control-plane certificate key: %w", kerr)
		}
		result.CertificateKey = key
	}

	return result, nil
}

// uploadCerts re-runs the upload-certs phase to obtain a fresh certificate key.
func (o *Orchestrator) uploadCerts(runner commandRunner) (string, error) {
	out, err := runner.ExecuteCommand("sudo kubeadm init phase upload-certs --upload-certs")
	if err != nil {
		return "", fmt.Errorf("kubeadm upload-certs failed: %w\n%s", err, common.RedactCommandOutput(out))
	}
	key := parseCertificateKey(out)
	if key == "" {
		return "", fmt.Errorf("could not parse certificate key from upload-certs output")
	}
	return key, nil
}

// JoinControlPlane stages the join config on a node and runs `kubeadm join`.
func (o *Orchestrator) JoinControlPlane(nodeIP, joinConfig string) error {
	runner := o.runnerFor(nodeIP)
	if err := runner.Connect(); err != nil {
		return fmt.Errorf("failed to connect to node %s: %w", nodeIP, err)
	}
	defer func() { _ = runner.Close() }()

	// Idempotency: if the node already joined (kubelet.conf present), re-running
	// kubeadm join would fail hard ("file already exists" / "port in use"). Treat an
	// already-joined node as success so the bootstrap is safe to re-run.
	if nodeAlreadyJoinedFn(runner) {
		o.logger.Info("Node %s is already a cluster member (kubelet.conf present); skipping kubeadm join", nodeIP)
		return nil
	}

	if err := writeRemoteFileFn(runner, remoteJoinConfigPath, joinConfig); err != nil {
		return fmt.Errorf("failed to stage kubeadm join config on %s: %w", nodeIP, err)
	}

	cmd := fmt.Sprintf("sudo kubeadm join --config %s", remoteJoinConfigPath)
	o.logger.Info("Running kubeadm join on %s", nodeIP)
	out, err := runner.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("kubeadm join failed on %s: %w\n%s", nodeIP, err, common.RedactCommandOutput(out))
	}
	return nil
}

// FetchAdminKubeconfig reads /etc/kubernetes/admin.conf from node0.
func (o *Orchestrator) FetchAdminKubeconfig(node0IP string) (string, error) {
	runner := o.runnerFor(node0IP)
	if err := runner.Connect(); err != nil {
		return "", fmt.Errorf("failed to connect to node0 %s: %w", node0IP, err)
	}
	defer func() { _ = runner.Close() }()

	out, err := runner.ExecuteCommand(fmt.Sprintf("sudo cat %s", remoteAdminConfPath))
	if err != nil {
		return "", fmt.Errorf("failed to read admin.conf from %s: %w", node0IP, err)
	}
	if !strings.Contains(out, "apiVersion") || !strings.Contains(out, "clusters") {
		return "", fmt.Errorf("admin.conf from %s does not look like a kubeconfig", node0IP)
	}
	return out, nil
}

// ResetNode runs `kubeadm reset -f` on a node over SSH, tearing down its
// cluster state (removes /etc/kubernetes, including the PKI). DESTRUCTIVE — the
// caller is responsible for confirmation.
func (o *Orchestrator) ResetNode(nodeIP string) error {
	runner := o.runnerFor(nodeIP)
	if err := runner.Connect(); err != nil {
		return fmt.Errorf("failed to connect to %s: %w", nodeIP, err)
	}
	defer func() { _ = runner.Close() }()

	out, err := runner.ExecuteCommand("sudo kubeadm reset -f")
	if err != nil {
		return fmt.Errorf("kubeadm reset failed on %s: %w\n%s", nodeIP, err, common.RedactCommandOutput(out))
	}
	return nil
}

// --- output parsing ---

var (
	// Matches: --discovery-token-ca-cert-hash sha256:<hex>
	caCertHashRe = regexp.MustCompile(`--discovery-token-ca-cert-hash\s+(sha256:[0-9a-f]{64})`)
	// Matches: --token <token>  (kubeadm token format: [a-z0-9]{6}\.[a-z0-9]{16})
	tokenRe = regexp.MustCompile(`--token\s+([a-z0-9]{6}\.[a-z0-9]{16})`)
	// The certificate key is printed on its own line after the upload-certs notice.
	certKeyRe = regexp.MustCompile(`(?m)^\s*([0-9a-f]{64})\s*$`)

	// Strict whole-string forms of the above, for validating user-supplied
	// join material before it is rendered into a join configuration.
	strictTokenRe      = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)
	strictCACertHashRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	strictCertKeyRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidateJoinMaterial checks user-supplied kubeadm join material against the
// formats kubeadm emits. token and caCertHash are required; certKey is
// optional (worker joins) but must be well-formed when present.
func ValidateJoinMaterial(token, caCertHash, certKey string) error {
	if !strictTokenRe.MatchString(token) {
		return fmt.Errorf("invalid --token: want kubeadm format like abcdef.0123456789abcdef")
	}
	if !strictCACertHashRe.MatchString(caCertHash) {
		return fmt.Errorf("invalid --ca-cert-hash: want sha256:<64 hex chars>")
	}
	if certKey != "" && !strictCertKeyRe.MatchString(certKey) {
		return fmt.Errorf("invalid --certificate-key: want 64 hex chars")
	}
	return nil
}

// ParseKubeadmInitOutput extracts the bootstrap token, CA cert hash and (if
// present) the control-plane certificate key from `kubeadm init` stdout.
func ParseKubeadmInitOutput(output string) (*KubeadmResult, error) {
	res := &KubeadmResult{}

	if m := tokenRe.FindStringSubmatch(output); len(m) == 2 {
		res.BootstrapToken = m[1]
	}
	if m := caCertHashRe.FindStringSubmatch(output); len(m) == 2 {
		res.CACertHash = m[1]
	}
	res.CertificateKey = parseCertificateKey(output)

	var missing []string
	if res.BootstrapToken == "" {
		missing = append(missing, "bootstrap token")
	}
	if res.CACertHash == "" {
		missing = append(missing, "CA cert hash")
	}
	if len(missing) > 0 {
		return res, fmt.Errorf("kubeadm init output missing: %s", strings.Join(missing, ", "))
	}
	return res, nil
}

// parseCertificateKey pulls the control-plane certificate key. kubeadm prints it
// in a stanza like:
//
//	[upload-certs] Using certificate key:
//	<64-hex>
//
// We anchor on that stanza first, then fall back to the first bare 64-hex line.
func parseCertificateKey(output string) string {
	if idx := strings.Index(output, "Using certificate key:"); idx >= 0 {
		tail := output[idx:]
		if m := certKeyRe.FindStringSubmatch(tail); len(m) == 2 {
			return m[1]
		}
	}
	if m := certKeyRe.FindStringSubmatch(output); len(m) == 2 {
		return m[1]
	}
	return ""
}
