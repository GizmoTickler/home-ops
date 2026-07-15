package kubernetes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

func decodeNetDoctorJSON(t *testing.T, raw string, dest any) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(raw), dest))
}

type netDoctorPipeListener struct {
	connections chan net.Conn
	done        chan struct{}
	closeOnce   sync.Once
}

type netDoctorPipeAddr string

func (a netDoctorPipeAddr) Network() string { return "tcp" }
func (a netDoctorPipeAddr) String() string  { return string(a) }

func newNetDoctorPipeListener() *netDoctorPipeListener {
	return &netDoctorPipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
}

func (l *netDoctorPipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.done:
		return nil, io.ErrClosedPipe
	}
}

func (l *netDoctorPipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return nil
}

func (l *netDoctorPipeListener) Addr() net.Addr {
	return netDoctorPipeAddr("127.0.0.1:443")
}

func (l *netDoctorPipeListener) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.connections <- server:
		return client, nil
	case <-l.done:
		_ = client.Close()
		_ = server.Close()
		return nil, io.ErrClosedPipe
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	}
}

func newNetDoctorTLSServer(t *testing.T, hostname string, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	now := time.Now()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(100),
		Subject:               pkix.Name{CommonName: "net-doctor test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	require.NoError(t, err)
	caCertificate, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(101),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(12 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCertificate, serverPublic, caPrivate)
	require.NoError(t, err)

	listener := newNetDoctorPipeListener()
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER, caDER}, PrivateKey: serverPrivate}},
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	testutil.Swap(t, &netDoctorProbeDialContext, listener.dialContext)
	pool := x509.NewCertPool()
	pool.AddCert(caCertificate)
	return server, pool
}

func netDoctorServerTarget(t *testing.T, server *httptest.Server, hostname string) netDoctorProbeTarget {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	require.NoError(t, err)
	var parsedPort int
	_, err = fmt.Sscanf(port, "%d", &parsedPort)
	require.NoError(t, err)
	return netDoctorProbeTarget{
		RouteNamespace: "apps",
		RouteName:      "example",
		Hostname:       hostname,
		Gateway:        "network/kgateway-internal",
		Address:        host,
		Port:           parsedPort,
		ReadyBackends:  true,
	}
}

func TestClassifyGatewayConditions(t *testing.T) {
	tests := []struct {
		name       string
		conditions []conditionJSON
		want       doctorStatus
	}{
		{
			name: "programmed and accepted",
			conditions: []conditionJSON{
				{Type: "Programmed", Status: "True"},
				{Type: "Accepted", Status: "True"},
			},
			want: statusPass,
		},
		{
			name:       "programmed missing",
			conditions: []conditionJSON{{Type: "Accepted", Status: "True"}},
			want:       statusFail,
		},
		{
			name: "not programmed",
			conditions: []conditionJSON{
				{Type: "Programmed", Status: "False", Reason: "Invalid"},
				{Type: "Accepted", Status: "True"},
			},
			want: statusFail,
		},
		{
			name:       "accepted missing is warning",
			conditions: []conditionJSON{{Type: "Programmed", Status: "True"}},
			want:       statusWarn,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := classifyGatewayConditions(tc.conditions)
			assert.Equal(t, tc.want, got)
			assert.NotEmpty(t, detail)
		})
	}
}

func TestAddNetDoctorGatewaysDiscoversAllAndWarnsOnlyWhenEmpty(t *testing.T) {
	var gateway netDoctorGateway
	decodeNetDoctorJSON(t, `{
		"metadata":{"namespace":"network","name":"kgateway-internal"},
		"status":{"conditions":[{"type":"Programmed","status":"True"},{"type":"Accepted","status":"True"}]}
	}`, &gateway)

	var report doctorReport
	addNetDoctorGateways(&report, []netDoctorGateway{gateway})
	require.Len(t, report.Checks, 1)
	assert.Equal(t, statusPass, report.Checks[0].Status)
	assert.Equal(t, "Gateway", report.Checks[0].Kind)
	assert.Equal(t, "network/kgateway-internal", report.Checks[0].Name)

	report = doctorReport{}
	addNetDoctorGateways(&report, nil)
	require.Len(t, report.Checks, 1)
	assert.Equal(t, statusWarn, report.Checks[0].Status)
	assert.Equal(t, "no Gateways found", report.Checks[0].Detail)
}

func TestResolveHTTPRouteBackends(t *testing.T) {
	var route netDoctorHTTPRoute
	decodeNetDoctorJSON(t, `{
		"metadata":{"namespace":"media","name":"apps"},
		"spec":{"rules":[{"backendRefs":[
			{"name":"ready"},
			{"name":"empty"},
			{"name":"missing"},
			{"group":"example.io","kind":"Widget","name":"ignored"}
		]}]}
	}`, &route)
	var services netDoctorServiceList
	decodeNetDoctorJSON(t, `{"items":[
		{"metadata":{"namespace":"media","name":"ready"}},
		{"metadata":{"namespace":"media","name":"empty"}}
	]}`, &services)
	var slices netDoctorEndpointSliceList
	decodeNetDoctorJSON(t, `{"items":[
		{"metadata":{"namespace":"media","labels":{"kubernetes.io/service-name":"ready"}},"endpoints":[
			{"conditions":{"ready":true}},
			{"conditions":{"ready":false}},
			{"conditions":{}}
		]}
	]}`, &slices)

	broken := resolveHTTPRouteBackends(route, services.Items, slices)
	assert.Equal(t, []string{
		"media/empty (zero ready endpoints)",
		"media/missing (Service missing)",
	}, broken)
}

func TestClassifyHTTPRouteParentsAndBackends(t *testing.T) {
	var noParents netDoctorHTTPRoute
	decodeNetDoctorJSON(t, `{"metadata":{"namespace":"apps","name":"none"}}`, &noParents)
	status, detail := classifyHTTPRoute(noParents, nil, netDoctorEndpointSliceList{}, true)
	assert.Equal(t, statusWarn, status)
	assert.Contains(t, detail, "no parent status")

	var rejected netDoctorHTTPRoute
	decodeNetDoctorJSON(t, `{
		"metadata":{"namespace":"apps","name":"rejected"},
		"status":{"parents":[{"parentRef":{"name":"envoy-internal","namespace":"network"},"conditions":[
			{"type":"Accepted","status":"False"}
		]}]}
	}`, &rejected)
	status, detail = classifyHTTPRoute(rejected, nil, netDoctorEndpointSliceList{}, true)
	assert.Equal(t, statusFail, status)
	assert.Contains(t, detail, "network/envoy-internal Accepted=False")
}

func TestClassifyCertificateExpiry(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		notAfter time.Time
		want     doctorStatus
	}{
		{"expired", now.Add(-time.Second), statusFail},
		{"twenty days", now.Add(20 * 24 * time.Hour), statusWarn},
		{"exactly twenty one days", now.Add(netDoctorCertWarnWindow), statusPass},
		{"healthy", now.Add(90 * 24 * time.Hour), statusPass},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := classifyCertificateExpiry(tc.notAfter, now)
			assert.Equal(t, tc.want, got)
			assert.Contains(t, detail, "expires")
		})
	}
}

func TestParseTLSCertificateNotAfter(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "example.test"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(30 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	require.NoError(t, err)
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	notAfter, err := parseTLSCertificateNotAfter(certificatePEM)
	require.NoError(t, err)
	assert.True(t, notAfter.Equal(template.NotAfter))
	_, err = parseTLSCertificateNotAfter([]byte("not a certificate"))
	require.Error(t, err)
}

func TestAddNetDoctorCertificatesMissingSecretFails(t *testing.T) {
	var report doctorReport
	addNetDoctorCertificates(&report, []netDoctorCertRef{{
		Namespace: "network",
		Name:      "gateway-tls",
		Gateway:   "network/envoy-external",
		Listener:  "https",
	}}, nil, time.Now())
	require.Len(t, report.Checks, 1)
	assert.Equal(t, statusFail, report.Checks[0].Status)
	assert.Contains(t, report.Checks[0].Detail, "missing")
}

func TestDiscoverNetDoctorWorkloadsUsesImageSubstrings(t *testing.T) {
	var deployments netDoctorDeploymentList
	decodeNetDoctorJSON(t, `{"items":[
		{"metadata":{"namespace":"network","name":"renamed-tunnel"},"spec":{"replicas":2,"selector":{"matchLabels":{"app":"tunnel"}},"template":{"metadata":{"labels":{"app":"tunnel"}},"spec":{"containers":[{"image":"docker.io/cloudflare/cloudflared:latest"}]}}},"status":{"readyReplicas":2}},
		{"metadata":{"namespace":"dns","name":"external-dns-powerdns"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"powerdns"}},"template":{"metadata":{"labels":{"app":"powerdns"}},"spec":{"containers":[{"image":"registry.k8s.io/external-dns/external-dns:v1"}]}}},"status":{"readyReplicas":1}},
		{"metadata":{"namespace":"apps","name":"unrelated"},"spec":{"template":{"spec":{"containers":[{"image":"example.test/app:v1"}]}}}}
	]}`, &deployments)
	var daemonSets netDoctorDaemonSetList
	decodeNetDoctorJSON(t, `{"items":[
		{"metadata":{"namespace":"edge","name":"tunnel-on-every-node"},"spec":{"selector":{"matchLabels":{"app":"edge-tunnel"}},"template":{"metadata":{"labels":{"app":"edge-tunnel"}},"spec":{"containers":[{"image":"quay.io/example/CLOUDFLARED:2026.7"}]}}},"status":{"desiredNumberScheduled":3,"numberReady":2}}
	]}`, &daemonSets)

	tunnels := discoverNetDoctorWorkloads(deployments.Items, daemonSets.Items, []string{"cloudflared"})
	require.Len(t, tunnels, 2)
	assert.Equal(t, "DaemonSet", tunnels[0].Kind)
	assert.Equal(t, "tunnel-on-every-node", tunnels[0].Metadata.Name)
	assert.Equal(t, "Deployment", tunnels[1].Kind)
	assert.Equal(t, "renamed-tunnel", tunnels[1].Metadata.Name)

	dns := discoverNetDoctorWorkloads(deployments.Items, nil, []string{"external-dns", "k8s-gateway"})
	require.Len(t, dns, 1)
	assert.Equal(t, "external-dns-powerdns", dns[0].Metadata.Name)
}

func TestTunnelPodStatusUsesDiscoveredWorkloadSelector(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	var pods netDoctorPodList
	decodeNetDoctorJSON(t, `{"items":[
		{"metadata":{"namespace":"edge","name":"arbitrary-one","labels":{"app":"tunnel"}},"status":{"containerStatuses":[
			{"name":"tunnel","restartCount":3,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}
		]}},
		{"metadata":{"namespace":"edge","name":"arbitrary-two","labels":{"app":"tunnel"}},"status":{"containerStatuses":[
			{"name":"tunnel","restartCount":1,"state":{},"lastState":{"terminated":{"finishedAt":"2026-07-15T11:30:00Z"}}}
		]}},
		{"metadata":{"namespace":"edge","name":"cloudflared-looking-but-unselected","labels":{"app":"other"}},"status":{"containerStatuses":[
			{"name":"app","restartCount":5,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}
		]}}
	]}`, &pods)
	workloads := []netDoctorWorkload{{
		Kind: "Deployment", Metadata: metadataJSON{Namespace: "edge", Name: "tunnel-controller"},
		SelectorLabels: map[string]string{"app": "tunnel"},
	}}
	var report doctorReport
	addNetDoctorTunnelPods(&report, pods, workloads, now)
	require.Len(t, report.Checks, 2)
	assert.Equal(t, statusFail, report.Checks[0].Status)
	assert.Equal(t, "edge/arbitrary-one", report.Checks[0].Name)
	assert.Equal(t, statusWarn, report.Checks[1].Status)
}

func TestNetDoctorDNSProbeUsesServiceSelectingDiscoveredWorkload(t *testing.T) {
	var services netDoctorServiceList
	decodeNetDoctorJSON(t, `{"items":[{
		"metadata":{"namespace":"dns","name":"powerdns-resolver"},
		"spec":{"selector":{"app":"external-dns-powerdns"},"clusterIP":"10.96.0.53"},
		"status":{"loadBalancer":{"ingress":[{"ip":"192.0.2.53"}]}}
	}]}`, &services)
	workloads := []netDoctorWorkload{{
		Kind: "Deployment", Metadata: metadataJSON{Namespace: "dns", Name: "external-dns-powerdns"},
		TemplateLabels: map[string]string{"app": "external-dns-powerdns"},
	}}
	testutil.Swap(t, &netDoctorLookupFn, func(_ context.Context, serverIP, hostname string) ([]string, error) {
		assert.Equal(t, "192.0.2.53", serverIP)
		assert.Equal(t, "app.example.test", hostname)
		return []string{"192.0.2.20"}, nil
	})
	var report doctorReport
	addNetDoctorDNSProbes(context.Background(), &report, services.Items, true, workloads, true, []string{"app.example.test"})
	require.Len(t, report.Checks, 1)
	assert.Equal(t, statusPass, report.Checks[0].Status)
	assert.Equal(t, "dns/app.example.test", report.Checks[0].Name)
	assert.Contains(t, report.Checks[0].Detail, "dns/powerdns-resolver 192.0.2.53:53")
}

func TestNetDoctorDNSProbeErrorsClearlyWithoutDiscoveredWorkload(t *testing.T) {
	var report doctorReport
	addNetDoctorDNSProbes(context.Background(), &report, nil, true, nil, true, []string{"app.example.test"})
	require.Len(t, report.Checks, 1)
	assert.Equal(t, statusFail, report.Checks[0].Status)
	assert.Contains(t, report.Checks[0].Detail, "no DNS controller workloads were discovered")
}

func TestBuildNetDoctorReportIsReadOnly(t *testing.T) {
	var calls [][]string
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{}, args...))
		switch args[1] {
		case netDoctorGatewayResource:
			return []byte(`{"items":[
				{"metadata":{"namespace":"network","name":"kgateway-internal"},"status":{"conditions":[{"type":"Programmed","status":"True"},{"type":"Accepted","status":"True"}]}},
				{"metadata":{"namespace":"network","name":"kgateway-external"},"status":{"conditions":[{"type":"Programmed","status":"True"},{"type":"Accepted","status":"True"}]}}
			]}`), nil
		case "deployments":
			return []byte(`{"items":[
				{"metadata":{"namespace":"network","name":"cloudflare-tunnel"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"tunnel"}},"template":{"metadata":{"labels":{"app":"tunnel"}},"spec":{"containers":[{"image":"cloudflare/cloudflared:2026.7"}]} }},"status":{"readyReplicas":1}},
				{"metadata":{"namespace":"dns","name":"external-dns-powerdns"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"powerdns"}},"template":{"metadata":{"labels":{"app":"powerdns"}},"spec":{"containers":[{"image":"registry.k8s.io/external-dns/external-dns:v1"}]} }},"status":{"readyReplicas":1}},
				{"metadata":{"namespace":"dns","name":"external-dns-cloudflare"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"cloudflare-dns"}},"template":{"metadata":{"labels":{"app":"cloudflare-dns"}},"spec":{"containers":[{"image":"registry.k8s.io/external-dns/external-dns:v1"}]} }},"status":{"readyReplicas":1}}
			]}`), nil
		case "pods":
			return []byte(`{"items":[{"metadata":{"namespace":"network","name":"cloudflare-tunnel-one","labels":{"app":"tunnel"}},"status":{"containerStatuses":[{"name":"tunnel","state":{}}]}}]}`), nil
		default:
			return []byte(`{"items":[]}`), nil
		}
	})

	report := buildNetDoctorReport(context.Background(), nil)
	assert.Zero(t, report.Summary.Fail)
	assert.NotZero(t, report.Summary.Pass)
	for _, call := range calls {
		assert.Equal(t, "get", call[0])
		assert.Contains(t, call, "-o")
		joined := strings.Join(call, " ")
		assert.NotContains(t, joined, "apply")
		assert.NotContains(t, joined, "patch")
		assert.NotContains(t, joined, "delete")
	}
}

func TestBuildNetDoctorReportZeroDiscoveryPathsNeverFailForMissingNames(t *testing.T) {
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"items":[]}`), nil
	})

	report := buildNetDoctorReport(context.Background(), nil)
	assert.Zero(t, report.Summary.Fail)
	checks := make(map[string]doctorCheck)
	for _, check := range report.Checks {
		checks[check.Group+"/"+check.Detail] = check
		assert.NotContains(t, check.Detail, "expected")
		assert.NotContains(t, check.Detail, "not found")
	}
	assert.Equal(t, statusWarn, checks[netDoctorGroupGateways+"/no Gateways found"].Status)
	assert.Equal(t, statusPass, checks[netDoctorGroupTunnel+"/no cloudflared workloads found"].Status)
	assert.Equal(t, statusPass, checks[netDoctorGroupDNS+"/no DNS controller workloads found"].Status)
}

func TestNetDoctorCommandOutputValidation(t *testing.T) {
	cmd := newNetDoctorCommand()
	cmd.SetArgs([]string{"--output", "yaml"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported output")
}

func TestNetDoctorCommandIsRegistered(t *testing.T) {
	command, _, err := NewCommand().Find([]string{"net-doctor"})
	require.NoError(t, err)
	assert.Equal(t, "net-doctor", command.Name())
}

func TestClassifyNetDoctorProbeHTTPMatrix(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		readyBackends bool
		want          doctorStatus
	}{
		{name: "success", statusCode: http.StatusOK, readyBackends: true, want: statusPass},
		{name: "redirect", statusCode: http.StatusTemporaryRedirect, readyBackends: true, want: statusPass},
		{name: "auth required", statusCode: http.StatusUnauthorized, readyBackends: true, want: statusPass},
		{name: "auth forbidden", statusCode: http.StatusForbidden, readyBackends: true, want: statusPass},
		{name: "route miss", statusCode: http.StatusNotFound, readyBackends: true, want: statusWarn},
		{name: "server error with ready backend", statusCode: http.StatusBadGateway, readyBackends: true, want: statusWarn},
		{name: "server error without ready backend", statusCode: http.StatusServiceUnavailable, readyBackends: false, want: statusFail},
		{name: "other client response", statusCode: http.StatusBadRequest, readyBackends: true, want: statusWarn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyNetDoctorProbeHTTP(tc.statusCode, tc.readyBackends))
		})
	}
}

func TestClassifyNetDoctorProbeTransportFailures(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	target := netDoctorProbeTarget{Gateway: "network/gateway", Address: "192.0.2.10", Port: 443}
	tests := []struct {
		name   string
		result netDoctorProbeResult
		match  string
	}{
		{name: "tcp connect", result: netDoctorProbeResult{Target: target, Err: errors.New("connection refused")}, match: "tcp=fail"},
		{name: "tls handshake", result: netDoctorProbeResult{Target: target, TCPConnected: true, Err: errors.New("certificate unknown")}, match: "tls=fail"},
		{name: "http timeout", result: netDoctorProbeResult{Target: target, TCPConnected: true, TLSHandshook: true, ChainValid: true, CertNotAfter: now.Add(10 * 24 * time.Hour), Err: context.DeadlineExceeded}, match: "http=fail"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, detail := classifyNetDoctorProbeResult(tc.result, now)
			assert.Equal(t, statusFail, status)
			assert.Contains(t, detail, tc.match)
		})
	}
}

func TestProbeNetDoctorHostUsesDirectAddressWithSNIAndHost(t *testing.T) {
	const hostname = "route.example.test"
	var gotHost string
	var gotSNI string
	server, roots := newNetDoctorTLSServer(t, hostname, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		gotHost = request.Host
		w.WriteHeader(http.StatusUnauthorized)
	}))
	server.TLS.GetConfigForClient = func(info *tls.ClientHelloInfo) (*tls.Config, error) {
		gotSNI = info.ServerName
		return nil, nil
	}
	testutil.Swap(t, &netDoctorProbeRootCAs, roots)

	target := netDoctorServerTarget(t, server, hostname)
	result := probeNetDoctorHost(context.Background(), target)
	require.NoError(t, result.Err)
	assert.True(t, result.TCPConnected)
	assert.True(t, result.TLSHandshook)
	assert.True(t, result.ChainValid)
	assert.Equal(t, http.StatusUnauthorized, result.HTTPStatusCode)
	assert.Equal(t, hostname, gotSNI)
	assert.Equal(t, hostname, gotHost)
	status, detail := classifyNetDoctorProbeResult(result, time.Now())
	assert.Equal(t, statusPass, status)
	assert.Contains(t, detail, "tcp=ok")
	assert.Contains(t, detail, "tls=ok")
	assert.Contains(t, detail, "chain=valid")
	assert.Contains(t, detail, "http=401")
}

func TestProbeNetDoctorHostTimeout(t *testing.T) {
	const hostname = "slow.example.test"
	server, roots := newNetDoctorTLSServer(t, hostname, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	testutil.Swap(t, &netDoctorProbeRootCAs, roots)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	result := probeNetDoctorHost(ctx, netDoctorServerTarget(t, server, hostname))
	require.Error(t, result.Err)
	assert.ErrorIs(t, result.Err, context.DeadlineExceeded)
	status, detail := classifyNetDoctorProbeResult(result, time.Now())
	assert.Equal(t, statusFail, status)
	assert.Contains(t, detail, "http=fail")
}

func TestBuildNetDoctorProbeTargetsMatchesGatewayService(t *testing.T) {
	var route netDoctorHTTPRoute
	decodeNetDoctorJSON(t, `{
		"metadata":{"namespace":"apps","name":"echo"},
		"spec":{"hostnames":["echo.example.test"],"parentRefs":[{"name":"kgateway-external","namespace":"network","sectionName":"https"}],"rules":[{"backendRefs":[{"name":"echo"}]}]}
	}`, &route)
	var gateways netDoctorGatewayList
	decodeNetDoctorJSON(t, `{"items":[{"metadata":{"namespace":"network","name":"kgateway-external"}}]}`, &gateways)
	var services netDoctorServiceList
	decodeNetDoctorJSON(t, `{"items":[
		{"metadata":{"namespace":"network","name":"generated-proxy","labels":{"gateway.networking.k8s.io/gateway-name":"kgateway-external"}},"spec":{"clusterIP":"10.96.0.20","ports":[{"name":"https","port":8443}]},"status":{"loadBalancer":{"ingress":[{"ip":"192.0.2.20"}]}}},
		{"metadata":{"namespace":"apps","name":"echo"}}
	]}`, &services)
	var slices netDoctorEndpointSliceList
	decodeNetDoctorJSON(t, `{"items":[{"metadata":{"namespace":"apps","labels":{"kubernetes.io/service-name":"echo"}},"endpoints":[{"conditions":{"ready":true}}]}]}`, &slices)

	targets, problems := buildNetDoctorProbeTargets([]netDoctorHTTPRoute{route}, gateways.Items, services.Items, slices)
	require.Empty(t, problems)
	require.Len(t, targets, 1)
	assert.Equal(t, "echo.example.test", targets[0].Hostname)
	assert.Equal(t, "network/kgateway-external", targets[0].Gateway)
	assert.Equal(t, "192.0.2.20", targets[0].Address)
	assert.Equal(t, 8443, targets[0].Port)
	assert.True(t, targets[0].ReadyBackends)
}

func TestNetDoctorWithoutProbeJSONCharacterization(t *testing.T) {
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"items":[]}`), nil
	})
	report := buildNetDoctorReportWithOptions(context.Background(), nil, netDoctorProbeOptions{Enabled: false, Timeout: time.Nanosecond})
	rendered, err := renderDoctorReport(report, "json")
	require.NoError(t, err)
	const expected = `{
  "summary": {
    "pass": 3,
    "warn": 2,
    "fail": 0
  },
  "checks": [
    {
      "group": "GATEWAYS",
      "name": "all",
      "status": "WARN",
      "detail": "no Gateways found",
      "kind": "Gateway"
    },
    {
      "group": "HTTPROUTES",
      "name": "all",
      "status": "WARN",
      "detail": "no HTTPRoutes found",
      "kind": "HTTPRoute"
    },
    {
      "group": "TUNNEL",
      "name": "all",
      "status": "PASS",
      "detail": "no cloudflared workloads found",
      "kind": "Workload"
    },
    {
      "group": "DNS",
      "name": "all",
      "status": "PASS",
      "detail": "no DNS controller workloads found",
      "kind": "Deployment"
    },
    {
      "group": "CERTIFICATES",
      "name": "TLS",
      "status": "PASS",
      "detail": "no Gateway TLS certificate references",
      "kind": "Secret"
    }
  ]
}`
	assert.Equal(t, expected, rendered)
	assert.NotContains(t, rendered, netDoctorGroupProbes)
}
