package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/config"
	"homeops-cli/internal/testutil"
)

func TestParseKubeadmCertsJSON(t *testing.T) {
	raw := []byte(`{
  "apiVersion":"output.kubeadm.k8s.io/v1alpha3",
  "kind":"CertificateExpirationInfo",
  "certificateExpirationInfo":[
    {"name":"admin.conf","expirationDate":"2027-07-14T12:00:00Z","residualTime":"8760h0m0s","caName":"ca","externallyManaged":false},
    {"name":"external.conf","expirationDate":"","caName":"external-ca","externallyManaged":true}
  ],
  "caExpirationInfo":[
    {"name":"ca","expirationDate":"2036-07-14T12:00:00Z","externallyManaged":false}
  ]
}`)
	certs, err := parseKubeadmCertsJSON(raw)
	require.NoError(t, err)
	require.Len(t, certs, 3)
	assert.Equal(t, "admin.conf", certs[0].Name)
	assert.Equal(t, "ca", certs[0].CAName)
	assert.True(t, certs[0].HasExpiry)
	assert.True(t, certs[1].ExternallyManaged)
	assert.False(t, certs[1].HasExpiry)
	assert.True(t, certs[2].CertificateAuth)
}

func TestParseKubeadmCertsJSONAcceptsAlternateKeys(t *testing.T) {
	certs, err := parseKubeadmCertsJSON([]byte(`{"certificates":[{"name":"apiserver","expirationDate":"2027-01-01T00:00:00Z","caName":"ca"}],"certificateAuthorities":[{"name":"ca","expirationDate":"2036-01-01T00:00:00Z"}]}`))
	require.NoError(t, err)
	require.Len(t, certs, 2)
	assert.True(t, certs[1].CertificateAuth)
}

func TestParseKubeadmCertsText(t *testing.T) {
	raw := `
CERTIFICATE                EXPIRES                  RESIDUAL TIME   CERTIFICATE AUTHORITY   EXTERNALLY MANAGED
admin.conf                 Jul 14, 2027 12:00 UTC   364d            ca                      no
apiserver                  Jul 20, 2026 12:00 UTC   6d              ca                      yes

CERTIFICATE AUTHORITY      EXPIRES                  RESIDUAL TIME   EXTERNALLY MANAGED
ca                         Jul 12, 2036 12:00 UTC   9y              no
`
	certs, err := parseKubeadmCertsText(raw)
	require.NoError(t, err)
	require.Len(t, certs, 3)
	assert.Equal(t, "admin.conf", certs[0].Name)
	assert.Equal(t, "ca", certs[0].CAName)
	assert.True(t, certs[1].ExternallyManaged)
	assert.True(t, certs[2].CertificateAuth)
	assert.Equal(t, "ca", certs[2].Name)
}

func TestClassifyKubeadmCertThresholds(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		expiry time.Time
		want   certStatus
	}{
		{name: "expired", expiry: now.Add(-time.Second), want: certFail},
		{name: "warning", expiry: now.Add(29 * 24 * time.Hour), want: certWarn},
		{name: "boundary is ok", expiry: now.Add(30 * 24 * time.Hour), want: certOK},
		{name: "healthy", expiry: now.Add(365 * 24 * time.Hour), want: certOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyKubeadmCert("k8s-0", parsedKubeadmCert{Name: "admin.conf", Expiry: tt.expiry, HasExpiry: true}, now, 30)
			assert.Equal(t, tt.want, got.Status)
		})
	}
	external := classifyKubeadmCert("k8s-0", parsedKubeadmCert{Name: "external", ExternallyManaged: true}, now, 30)
	assert.Equal(t, certOK, external.Status)
	assert.Contains(t, external.Detail, "externally managed")
}

func TestCertReportExitCodes(t *testing.T) {
	assert.NoError(t, certReportExitError(certReport{OK: 1, Warn: 1}, false))
	assert.Error(t, certReportExitError(certReport{Warn: 1}, true))
	assert.Error(t, certReportExitError(certReport{Fail: 1}, false))
}

func TestInspectKubeadmCertsFallsBackToText(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	testutil.Swap(t, &certNowFn, func() time.Time { return now })
	var commands []string
	testutil.Swap(t, &certNodeCommandFn, func(_ context.Context, _ config.Node, _ string, command string) (string, error) {
		commands = append(commands, command)
		if strings.Contains(command, "-o json") {
			return "", errors.New("unknown flag: --output")
		}
		return "CERTIFICATE  EXPIRES  RESIDUAL TIME  CERTIFICATE AUTHORITY  EXTERNALLY MANAGED\nadmin.conf  Jul 20, 2026 12:00 UTC  6d  ca  no\n", nil
	})
	report := inspectKubeadmCerts(context.Background(), []config.Node{{Name: "cp0", IP: "10.0.0.1"}}, "core", 30)
	require.Len(t, report.Checks, 1)
	assert.Equal(t, certWarn, report.Checks[0].Status)
	assert.Equal(t, 1, report.Warn)
	assert.Len(t, commands, 2)
}

func TestRunCertsWorkflowRenewAndRestartAreSeparatelyGated(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	t.Cleanup(restore)
	jsonOutput := `{"certificateExpirationInfo":[{"name":"admin.conf","expirationDate":"2027-07-14T12:00:00Z","caName":"ca"}]}`
	var commands []string
	testutil.Swap(t, &certNodeCommandFn, func(_ context.Context, _ config.Node, _ string, command string) (string, error) {
		commands = append(commands, command)
		if strings.Contains(command, "check-expiration") {
			return jsonOutput, nil
		}
		return "", nil
	})
	confirmCalls := 0
	testutil.Swap(t, &confirmActionFn, func(message string, _ bool) (bool, error) {
		confirmCalls++
		assert.Contains(t, message, "DESTRUCTIVE")
		return true, nil
	})
	waits := 0
	testutil.Swap(t, &certWaitFn, func(context.Context, time.Duration) error {
		waits++
		return nil
	})

	report, changed, err := runCertsWorkflow(context.Background(), 30, true, true)
	require.NoError(t, err)
	assert.True(t, changed)
	require.NotNil(t, report.After)
	assert.Equal(t, 2, confirmCalls)
	assert.Equal(t, 24, waits) // 3 default control-plane nodes * 4 components * stop/start waits
	joined := strings.Join(commands, "\n")
	assert.Equal(t, 3, strings.Count(joined, "kubeadm certs renew all"))
	assert.Equal(t, 12, strings.Count(joined, "sudo test ! -e"))
	assert.Equal(t, 12, strings.Count(joined, "sudo mv -- '/var/tmp"))
}

func TestRunCertsWorkflowDeclinedRenewDoesNotWrite(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	t.Cleanup(restore)
	testutil.Swap(t, &certNodeCommandFn, func(_ context.Context, _ config.Node, _ string, command string) (string, error) {
		if strings.Contains(command, "check-expiration") {
			return `{"certificateExpirationInfo":[{"name":"admin.conf","expirationDate":"2027-07-14T12:00:00Z"}]}`, nil
		}
		t.Fatalf("unexpected write command %q", command)
		return "", nil
	})
	testutil.Swap(t, &confirmActionFn, func(string, bool) (bool, error) { return false, nil })

	report, changed, err := runCertsWorkflow(context.Background(), 30, true, false)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, report.After)
}

func TestParseKubeadmCertsMissingEntries(t *testing.T) {
	// JSON form: kubeadm >=1.29 marks super-admin.conf missing on joined
	// control-plane nodes.
	certs, err := parseKubeadmCertsJSON([]byte(`{"certificates":[
		{"name":"super-admin.conf","expirationDate":null,"residualTime":0,"externallyManaged":false,"missing":true},
		{"name":"admin.conf","expirationDate":"2027-01-01T00:00:00Z","caName":"ca"}
	],"certificateAuthorities":[{"name":"ca","expirationDate":"2036-01-01T00:00:00Z"}]}`))
	require.NoError(t, err)
	require.Len(t, certs, 3)
	assert.True(t, certs[0].Missing)
	assert.False(t, certs[0].HasExpiry)
	assert.False(t, certs[1].Missing)

	// Text form: "!MISSING! super-admin.conf".
	textCerts, err := parseKubeadmCertsText(`
CERTIFICATE                EXPIRES                  RESIDUAL TIME   CERTIFICATE AUTHORITY   EXTERNALLY MANAGED
!MISSING! super-admin.conf
admin.conf                 Jul 14, 2027 12:00 UTC   364d            ca                      no
`)
	require.NoError(t, err)
	require.Len(t, textCerts, 2)
	assert.Equal(t, "super-admin.conf", textCerts[0].Name)
	assert.True(t, textCerts[0].Missing)
}

func TestClassifyKubeadmCertMissing(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	superAdmin := classifyKubeadmCert("k8s-1", parsedKubeadmCert{Name: "super-admin.conf", Missing: true}, now, 30)
	assert.Equal(t, certOK, superAdmin.Status)
	assert.Contains(t, superAdmin.Detail, "init node")

	other := classifyKubeadmCert("k8s-1", parsedKubeadmCert{Name: "apiserver", Missing: true}, now, 30)
	assert.Equal(t, certFail, other.Status)
	assert.Contains(t, other.Detail, "missing")
}
