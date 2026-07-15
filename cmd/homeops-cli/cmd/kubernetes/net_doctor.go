package kubernetes

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/kubeutil"
	"homeops-cli/internal/ui"
)

const (
	netDoctorGatewayResource       = "gateways.gateway.networking.k8s.io"
	netDoctorHTTPRouteResource     = "httproutes.gateway.networking.k8s.io"
	netDoctorEndpointSliceResource = "endpointslices.discovery.k8s.io"
	netDoctorCertWarnWindow        = 21 * 24 * time.Hour
	netDoctorRestartWindow         = time.Hour

	netDoctorGroupGateways     = "GATEWAYS"
	netDoctorGroupHTTPRoutes   = "HTTPROUTES"
	netDoctorGroupTunnel       = "TUNNEL"
	netDoctorGroupDNS          = "DNS"
	netDoctorGroupCertificates = "CERTIFICATES"
	netDoctorGroupProbes       = "PROBES"

	netDoctorDefaultProbeTimeout = 5 * time.Second
	netDoctorProbeConcurrency    = 8
)

var (
	netDoctorNowFn    = time.Now
	netDoctorLookupFn = func(ctx context.Context, serverIP, hostname string) ([]string, error) {
		if net.ParseIP(serverIP) == nil {
			return nil, fmt.Errorf("LoadBalancer address %q is not an IP", serverIP)
		}
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(serverIP, "53"))
			},
		}
		return resolver.LookupHost(ctx, hostname)
	}
	netDoctorProbeRootCAs     *x509.CertPool
	netDoctorProbeFn          = probeNetDoctorHost
	netDoctorProbeDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
)

type netDoctorGateway struct {
	Metadata metadataJSON `json:"metadata"`
	Spec     struct {
		Listeners []struct {
			Name string `json:"name"`
			TLS  *struct {
				CertificateRefs []struct {
					Group     string `json:"group"`
					Kind      string `json:"kind"`
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"certificateRefs"`
			} `json:"tls"`
		} `json:"listeners"`
	} `json:"spec"`
	Status struct {
		Addresses []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"addresses"`
		Conditions []conditionJSON `json:"conditions"`
		Listeners  []struct {
			Name           string `json:"name"`
			AttachedRoutes int    `json:"attachedRoutes"`
		} `json:"listeners"`
	} `json:"status"`
}

type netDoctorGatewayList struct {
	Items []netDoctorGateway `json:"items"`
}

type netDoctorBackendRef struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type netDoctorHTTPRoute struct {
	Metadata metadataJSON `json:"metadata"`
	Spec     struct {
		Hostnames  []string `json:"hostnames"`
		ParentRefs []struct {
			Group       string `json:"group"`
			Kind        string `json:"kind"`
			Name        string `json:"name"`
			Namespace   string `json:"namespace"`
			SectionName string `json:"sectionName"`
		} `json:"parentRefs"`
		Rules []struct {
			BackendRefs []netDoctorBackendRef `json:"backendRefs"`
		} `json:"rules"`
	} `json:"spec"`
	Status struct {
		Parents []struct {
			ParentRef struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"parentRef"`
			Conditions []conditionJSON `json:"conditions"`
		} `json:"parents"`
	} `json:"status"`
}

type netDoctorHTTPRouteList struct {
	Items []netDoctorHTTPRoute `json:"items"`
}

type netDoctorService struct {
	Metadata metadataJSON `json:"metadata"`
	Spec     struct {
		LoadBalancerIP string            `json:"loadBalancerIP"`
		ClusterIP      string            `json:"clusterIP"`
		Selector       map[string]string `json:"selector"`
		Ports          []struct {
			Name string `json:"name"`
			Port int    `json:"port"`
		} `json:"ports"`
	} `json:"spec"`
	Status struct {
		LoadBalancer struct {
			Ingress []struct {
				IP       string `json:"ip"`
				Hostname string `json:"hostname"`
			} `json:"ingress"`
		} `json:"loadBalancer"`
	} `json:"status"`
}

type netDoctorServiceList struct {
	Items []netDoctorService `json:"items"`
}

type netDoctorEndpointSliceList struct {
	Items []struct {
		Metadata struct {
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Endpoints []struct {
			Conditions struct {
				Ready *bool `json:"ready"`
			} `json:"conditions"`
		} `json:"endpoints"`
	} `json:"items"`
}

type netDoctorContainer struct {
	Image string `json:"image"`
}

type netDoctorPodTemplate struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Containers []netDoctorContainer `json:"containers"`
	} `json:"spec"`
}

type netDoctorDeployment struct {
	Metadata metadataJSON `json:"metadata"`
	Spec     struct {
		Replicas *int `json:"replicas"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template netDoctorPodTemplate `json:"template"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas int `json:"readyReplicas"`
	} `json:"status"`
}

type netDoctorDeploymentList struct {
	Items []netDoctorDeployment `json:"items"`
}

type netDoctorDaemonSet struct {
	Metadata metadataJSON `json:"metadata"`
	Spec     struct {
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template netDoctorPodTemplate `json:"template"`
	} `json:"spec"`
	Status struct {
		DesiredNumberScheduled int `json:"desiredNumberScheduled"`
		NumberReady            int `json:"numberReady"`
	} `json:"status"`
}

type netDoctorDaemonSetList struct {
	Items []netDoctorDaemonSet `json:"items"`
}

type netDoctorWorkload struct {
	Kind           string
	Metadata       metadataJSON
	Desired        int
	Ready          int
	SelectorLabels map[string]string
	TemplateLabels map[string]string
}

type netDoctorPodList struct {
	Items []struct {
		Metadata struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			ContainerStatuses []struct {
				Name         string `json:"name"`
				RestartCount int    `json:"restartCount"`
				State        struct {
					Waiting *struct {
						Reason string `json:"reason"`
					} `json:"waiting"`
				} `json:"state"`
				LastState struct {
					Terminated *struct {
						FinishedAt string `json:"finishedAt"`
					} `json:"terminated"`
				} `json:"lastState"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

type netDoctorSecret struct {
	Metadata metadataJSON      `json:"metadata"`
	Data     map[string][]byte `json:"data"`
}

type netDoctorSecretList struct {
	Items []netDoctorSecret `json:"items"`
}

type netDoctorCertRef struct {
	Namespace string
	Name      string
	Gateway   string
	Listener  string
}

type netDoctorProbeOptions struct {
	Enabled bool
	Timeout time.Duration
}

type netDoctorProbeTarget struct {
	RouteNamespace string
	RouteName      string
	Hostname       string
	Gateway        string
	Address        string
	Port           int
	ReadyBackends  bool
}

type netDoctorProbeResult struct {
	Target         netDoctorProbeTarget
	TCPConnected   bool
	TLSHandshook   bool
	ChainValid     bool
	CertNotAfter   time.Time
	HTTPStatusCode int
	Latency        time.Duration
	Err            error
}

func newNetDoctorCommand() *cobra.Command {
	var output string
	var hostnames []string
	var probe bool
	probeTimeout := netDoctorDefaultProbeTimeout
	cmd := &cobra.Command{
		Use:          "net-doctor",
		Short:        "Run read-only Gateway API, tunnel, DNS, and TLS triage",
		SilenceUsage: true,
		Example: "  homeops-cli k8s net-doctor\n" +
			"  homeops-cli k8s net-doctor --resolve home.example.com --resolve status.example.com\n" +
			"  homeops-cli k8s net-doctor --probe --probe-timeout 5s\n" +
			"  homeops-cli k8s net-doctor --output json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ui.ValidateOutputFormat(output); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kubernetesDefaultCommandTimeout)
			defer cancel()
			if probeTimeout <= 0 {
				return fmt.Errorf("--probe-timeout must be greater than zero")
			}
			report := buildNetDoctorReportWithOptions(ctx, hostnames, netDoctorProbeOptions{Enabled: probe, Timeout: probeTimeout})
			rendered, err := renderDoctorReport(report, output)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			if report.hasFail() {
				return fmt.Errorf("net-doctor found %d failing check(s)", report.Summary.Fail)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().StringSliceVar(&hostnames, "resolve", nil, "resolve a hostname against a Service selecting a discovered DNS controller (repeatable)")
	cmd.Flags().BoolVar(&probe, "probe", false, "actively probe every HTTPRoute hostname through its Gateway Service address")
	cmd.Flags().DurationVar(&probeTimeout, "probe-timeout", netDoctorDefaultProbeTimeout, "timeout for each active Gateway hostname probe")
	return cmd
}

func buildNetDoctorReport(ctx context.Context, hostnames []string) doctorReport {
	return buildNetDoctorReportWithOptions(ctx, hostnames, netDoctorProbeOptions{})
}

func buildNetDoctorReportWithOptions(ctx context.Context, hostnames []string, probeOptions netDoctorProbeOptions) doctorReport {
	var report doctorReport
	var gateways netDoctorGatewayList
	gatewaysOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", netDoctorGatewayResource, &gateways); err != nil {
		report.add(netDoctorGroupGateways, "Gateway", "", "all", statusFail, err.Error())
		gatewaysOK = false
	} else {
		addNetDoctorGateways(&report, gateways.Items)
	}

	var routes netDoctorHTTPRouteList
	routesOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", netDoctorHTTPRouteResource, &routes); err != nil {
		report.add(netDoctorGroupHTTPRoutes, "HTTPRoute", "", "all", statusFail, err.Error())
		routesOK = false
	}
	var services netDoctorServiceList
	servicesOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", "services", &services); err != nil {
		report.add(netDoctorGroupHTTPRoutes, "Service", "", "all", statusFail, err.Error())
		servicesOK = false
	}
	var slices netDoctorEndpointSliceList
	slicesOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", netDoctorEndpointSliceResource, &slices); err != nil {
		report.add(netDoctorGroupHTTPRoutes, "EndpointSlice", "", "all", statusFail, err.Error())
		slicesOK = false
	}
	if routesOK {
		addNetDoctorHTTPRoutes(&report, routes.Items, services.Items, slices, servicesOK && slicesOK)
	}
	if probeOptions.Enabled {
		addNetDoctorProbes(ctx, &report, routes.Items, gateways.Items, services.Items, slices, routesOK, gatewaysOK, servicesOK, probeOptions.Timeout)
	}

	var deployments netDoctorDeploymentList
	deploymentsOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", "deployments", &deployments); err != nil {
		report.add(netDoctorGroupTunnel, "Deployment", "", "all", statusFail, err.Error())
		report.add(netDoctorGroupDNS, "Deployment", "", "all", statusFail, err.Error())
		deploymentsOK = false
	}
	var daemonSets netDoctorDaemonSetList
	daemonSetsOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", "daemonsets", &daemonSets); err != nil {
		report.add(netDoctorGroupTunnel, "DaemonSet", "", "all", statusFail, err.Error())
		daemonSetsOK = false
	}

	tunnelWorkloads := discoverNetDoctorWorkloads(deployments.Items, daemonSets.Items, []string{"cloudflared"})
	if len(tunnelWorkloads) == 0 {
		if deploymentsOK && daemonSetsOK {
			report.add(netDoctorGroupTunnel, "Workload", "", "all", statusPass, "no cloudflared workloads found")
		}
	} else {
		addNetDoctorWorkloadReadiness(&report, netDoctorGroupTunnel, tunnelWorkloads)
		var pods netDoctorPodList
		if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", "pods", &pods); err != nil {
			report.add(netDoctorGroupTunnel, "Pod", "", "all", statusFail, err.Error())
		} else {
			addNetDoctorTunnelPods(&report, pods, tunnelWorkloads, netDoctorNowFn())
		}
	}

	dnsWorkloads := discoverNetDoctorWorkloads(deployments.Items, nil, []string{"external-dns", "k8s-gateway"})
	if len(dnsWorkloads) == 0 {
		if deploymentsOK {
			report.add(netDoctorGroupDNS, "Deployment", "", "all", statusPass, "no DNS controller workloads found")
		}
	} else {
		addNetDoctorWorkloadReadiness(&report, netDoctorGroupDNS, dnsWorkloads)
	}
	if len(hostnames) > 0 {
		addNetDoctorDNSProbes(ctx, &report, services.Items, servicesOK, dnsWorkloads, deploymentsOK, hostnames)
	}

	if gatewaysOK {
		refs := netDoctorCertificateRefs(gateways.Items)
		if len(refs) == 0 {
			report.add(netDoctorGroupCertificates, "Secret", "", "TLS", statusPass, "no Gateway TLS certificate references")
		} else {
			var secrets netDoctorSecretList
			if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", "secrets", &secrets); err != nil {
				report.add(netDoctorGroupCertificates, "Secret", "", "all", statusFail, err.Error())
			} else {
				addNetDoctorCertificates(&report, refs, secrets.Items, netDoctorNowFn())
			}
		}
	}
	report.finalize()
	return report
}

func conditionByType(conditions []conditionJSON, conditionType string) (conditionJSON, bool) {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition, true
		}
	}
	return conditionJSON{}, false
}

func classifyGatewayConditions(conditions []conditionJSON) (doctorStatus, string) {
	programmed, programmedOK := conditionByType(conditions, "Programmed")
	accepted, acceptedOK := conditionByType(conditions, "Accepted")
	parts := make([]string, 0, 2)
	if programmedOK {
		parts = append(parts, "Programmed="+programmed.Status)
	} else {
		parts = append(parts, "Programmed condition missing")
	}
	if acceptedOK {
		parts = append(parts, "Accepted="+accepted.Status)
	} else {
		parts = append(parts, "Accepted condition missing")
	}
	if !programmedOK || programmed.Status != "True" {
		if programmedOK && (programmed.Reason != "" || programmed.Message != "") {
			parts = append(parts, conditionDetail(programmed))
		}
		return statusFail, strings.Join(parts, "; ")
	}
	if !acceptedOK || accepted.Status != "True" {
		return statusWarn, strings.Join(parts, "; ")
	}
	return statusPass, strings.Join(parts, "; ")
}

func addNetDoctorGateways(report *doctorReport, gateways []netDoctorGateway) {
	if len(gateways) == 0 {
		report.add(netDoctorGroupGateways, "Gateway", "", "all", statusWarn, "no Gateways found")
		return
	}
	for _, gateway := range gateways {
		status, detail := classifyGatewayConditions(gateway.Status.Conditions)
		listeners := make([]string, 0, len(gateway.Status.Listeners))
		for _, listener := range gateway.Status.Listeners {
			listeners = append(listeners, fmt.Sprintf("%s attachedRoutes=%d", listener.Name, listener.AttachedRoutes))
		}
		sort.Strings(listeners)
		if len(listeners) > 0 {
			detail += "; " + strings.Join(listeners, ", ")
		}
		report.add(netDoctorGroupGateways, "Gateway", gateway.Metadata.Namespace, gateway.Metadata.Name, status, detail)
	}
}

func readyEndpointsByService(slices netDoctorEndpointSliceList) map[string]int {
	ready := map[string]int{}
	for _, slice := range slices.Items {
		serviceName := slice.Metadata.Labels["kubernetes.io/service-name"]
		if serviceName == "" {
			continue
		}
		key := namespacedName(slice.Metadata.Namespace, serviceName)
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				ready[key]++
			}
		}
	}
	return ready
}

func resolveHTTPRouteBackends(route netDoctorHTTPRoute, services []netDoctorService, slices netDoctorEndpointSliceList) []string {
	serviceSet := make(map[string]struct{}, len(services))
	for _, service := range services {
		serviceSet[namespacedName(service.Metadata.Namespace, service.Metadata.Name)] = struct{}{}
	}
	ready := readyEndpointsByService(slices)
	brokenSet := map[string]struct{}{}
	for _, rule := range route.Spec.Rules {
		for _, backend := range rule.BackendRefs {
			if (backend.Group != "" && backend.Group != "core") || (backend.Kind != "" && backend.Kind != "Service") {
				continue
			}
			namespace := backend.Namespace
			if namespace == "" {
				namespace = route.Metadata.Namespace
			}
			key := namespacedName(namespace, backend.Name)
			if _, exists := serviceSet[key]; !exists {
				brokenSet[key+" (Service missing)"] = struct{}{}
			} else if ready[key] == 0 {
				brokenSet[key+" (zero ready endpoints)"] = struct{}{}
			}
		}
	}
	broken := make([]string, 0, len(brokenSet))
	for item := range brokenSet {
		broken = append(broken, item)
	}
	sort.Strings(broken)
	return broken
}

func classifyHTTPRoute(route netDoctorHTTPRoute, services []netDoctorService, slices netDoctorEndpointSliceList, resolveBackends bool) (doctorStatus, string) {
	parts := []string{}
	status := statusPass
	if len(route.Status.Parents) == 0 {
		status = statusWarn
		parts = append(parts, "no parent status")
	} else {
		for _, parent := range route.Status.Parents {
			parentNamespace := parent.ParentRef.Namespace
			if parentNamespace == "" {
				parentNamespace = route.Metadata.Namespace
			}
			parentName := namespacedName(parentNamespace, parent.ParentRef.Name)
			accepted, ok := conditionByType(parent.Conditions, "Accepted")
			if !ok || accepted.Status != "True" {
				status = statusFail
				if !ok {
					parts = append(parts, parentName+" Accepted condition missing")
				} else {
					parts = append(parts, parentName+" Accepted="+accepted.Status)
				}
			} else {
				parts = append(parts, parentName+" Accepted=True")
			}
		}
	}
	if resolveBackends {
		broken := resolveHTTPRouteBackends(route, services, slices)
		if len(broken) > 0 {
			status = statusFail
			parts = append(parts, "broken backends: "+strings.Join(broken, ", "))
		} else {
			parts = append(parts, "all Service backends ready")
		}
	}
	return status, strings.Join(parts, "; ")
}

func addNetDoctorHTTPRoutes(report *doctorReport, routes []netDoctorHTTPRoute, services []netDoctorService, slices netDoctorEndpointSliceList, resolveBackends bool) {
	if len(routes) == 0 {
		report.add(netDoctorGroupHTTPRoutes, "HTTPRoute", "", "all", statusWarn, "no HTTPRoutes found")
		return
	}
	for _, route := range routes {
		status, detail := classifyHTTPRoute(route, services, slices, resolveBackends)
		report.add(netDoctorGroupHTTPRoutes, "HTTPRoute", route.Metadata.Namespace, route.Metadata.Name, status, detail)
	}
}

func netDoctorGatewayKey(namespace, name string) string {
	return namespacedName(namespace, name)
}

func netDoctorGatewayService(gateway netDoctorGateway, services []netDoctorService) (netDoctorService, bool) {
	var fallback *netDoctorService
	for i := range services {
		service := &services[i]
		if service.Metadata.Namespace != gateway.Metadata.Namespace {
			continue
		}
		labels := service.Metadata.Labels
		if labels["gateway.networking.k8s.io/gateway-name"] == gateway.Metadata.Name ||
			labels["gateway.envoyproxy.io/owning-gateway-name"] == gateway.Metadata.Name ||
			labels["gateway.kgateway.dev/owning-gateway-name"] == gateway.Metadata.Name {
			return *service, true
		}
		if service.Metadata.Name == gateway.Metadata.Name {
			return *service, true
		}
		if fallback == nil && strings.Contains(service.Metadata.Name, gateway.Metadata.Name) {
			fallback = service
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return netDoctorService{}, false
}

func netDoctorServiceAddress(service netDoctorService) string {
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if strings.TrimSpace(ingress.IP) != "" {
			return strings.TrimSpace(ingress.IP)
		}
		if strings.TrimSpace(ingress.Hostname) != "" {
			return strings.TrimSpace(ingress.Hostname)
		}
	}
	if strings.TrimSpace(service.Spec.LoadBalancerIP) != "" {
		return strings.TrimSpace(service.Spec.LoadBalancerIP)
	}
	return strings.TrimSpace(service.Spec.ClusterIP)
}

func netDoctorServiceHTTPSPort(service netDoctorService) int {
	for _, port := range service.Spec.Ports {
		if port.Port == 443 || strings.EqualFold(port.Name, "https") {
			return port.Port
		}
	}
	return 443
}

func netDoctorRouteHasReadyBackends(route netDoctorHTTPRoute, slices netDoctorEndpointSliceList) bool {
	ready := readyEndpointsByService(slices)
	for _, rule := range route.Spec.Rules {
		for _, backend := range rule.BackendRefs {
			if (backend.Group != "" && backend.Group != "core") || (backend.Kind != "" && backend.Kind != "Service") {
				continue
			}
			namespace := backend.Namespace
			if namespace == "" {
				namespace = route.Metadata.Namespace
			}
			if ready[namespacedName(namespace, backend.Name)] > 0 {
				return true
			}
		}
	}
	return false
}

func buildNetDoctorProbeTargets(routes []netDoctorHTTPRoute, gateways []netDoctorGateway, services []netDoctorService, slices netDoctorEndpointSliceList) ([]netDoctorProbeTarget, []doctorCheck) {
	gatewayByName := make(map[string]netDoctorGateway, len(gateways))
	for _, gateway := range gateways {
		gatewayByName[netDoctorGatewayKey(gateway.Metadata.Namespace, gateway.Metadata.Name)] = gateway
	}

	var targets []netDoctorProbeTarget
	var problems []doctorCheck
	seen := map[string]struct{}{}
	for _, route := range routes {
		for _, hostname := range route.Spec.Hostnames {
			hostname = strings.TrimSpace(hostname)
			if hostname == "" {
				continue
			}
			for _, parent := range route.Spec.ParentRefs {
				if (parent.Group != "" && parent.Group != "gateway.networking.k8s.io") || (parent.Kind != "" && parent.Kind != "Gateway") {
					continue
				}
				gatewayNamespace := parent.Namespace
				if gatewayNamespace == "" {
					gatewayNamespace = route.Metadata.Namespace
				}
				gatewayName := netDoctorGatewayKey(gatewayNamespace, parent.Name)
				probeName := namespacedName(route.Metadata.Namespace, route.Metadata.Name) + "/" + hostname + "@" + gatewayName
				if _, duplicate := seen[probeName]; duplicate {
					continue
				}
				seen[probeName] = struct{}{}

				gateway, ok := gatewayByName[gatewayName]
				if !ok {
					problems = append(problems, doctorCheck{Group: netDoctorGroupProbes, Kind: "HTTPSProbe", Name: probeName, Status: statusFail, Detail: "matching Gateway not found"})
					continue
				}
				service, serviceOK := netDoctorGatewayService(gateway, services)
				address := ""
				port := 443
				if serviceOK {
					address = netDoctorServiceAddress(service)
					port = netDoctorServiceHTTPSPort(service)
				}
				if address == "" {
					for _, gatewayAddress := range gateway.Status.Addresses {
						if strings.TrimSpace(gatewayAddress.Value) != "" {
							address = strings.TrimSpace(gatewayAddress.Value)
							break
						}
					}
				}
				if address == "" {
					detail := "matching Gateway Service has no LoadBalancer, loadBalancerIP, or ClusterIP address"
					if !serviceOK {
						detail = "matching Gateway Service not found and Gateway status has no address"
					}
					problems = append(problems, doctorCheck{Group: netDoctorGroupProbes, Kind: "HTTPSProbe", Name: probeName, Status: statusFail, Detail: detail})
					continue
				}
				targets = append(targets, netDoctorProbeTarget{
					RouteNamespace: route.Metadata.Namespace,
					RouteName:      route.Metadata.Name,
					Hostname:       hostname,
					Gateway:        gatewayName,
					Address:        address,
					Port:           port,
					ReadyBackends:  netDoctorRouteHasReadyBackends(route, slices),
				})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		left := namespacedName(targets[i].RouteNamespace, targets[i].RouteName) + "/" + targets[i].Hostname + "@" + targets[i].Gateway
		right := namespacedName(targets[j].RouteNamespace, targets[j].RouteName) + "/" + targets[j].Hostname + "@" + targets[j].Gateway
		return left < right
	})
	sort.Slice(problems, func(i, j int) bool { return problems[i].Name < problems[j].Name })
	return targets, problems
}

func addNetDoctorProbes(ctx context.Context, report *doctorReport, routes []netDoctorHTTPRoute, gateways []netDoctorGateway, services []netDoctorService, slices netDoctorEndpointSliceList, routesOK, gatewaysOK, servicesOK bool, timeout time.Duration) {
	if !routesOK || !gatewaysOK || !servicesOK {
		report.add(netDoctorGroupProbes, "HTTPSProbe", "", "all", statusFail, "active probes require HTTPRoutes, Gateways, and Services to be listed successfully")
		return
	}
	targets, problems := buildNetDoctorProbeTargets(routes, gateways, services, slices)
	report.Checks = append(report.Checks, problems...)
	if len(targets) == 0 {
		if len(problems) == 0 {
			report.add(netDoctorGroupProbes, "HTTPSProbe", "", "all", statusWarn, "no HTTPRoute hostnames with Gateway parents found")
		}
		return
	}

	results := make([]netDoctorProbeResult, len(targets))
	jobs := make(chan int)
	workerCount := netDoctorProbeConcurrency
	if len(targets) < workerCount {
		workerCount = len(targets)
	}
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				probeCtx, cancel := context.WithTimeout(ctx, timeout)
				results[index] = netDoctorProbeFn(probeCtx, targets[index])
				cancel()
			}
		}()
	}
	for i := range targets {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	for _, result := range results {
		status, detail := classifyNetDoctorProbeResult(result, netDoctorNowFn())
		name := namespacedName(result.Target.RouteNamespace, result.Target.RouteName) + "/" + result.Target.Hostname + "@" + result.Target.Gateway
		report.add(netDoctorGroupProbes, "HTTPSProbe", "", name, status, detail)
	}
}

func probeNetDoctorHost(ctx context.Context, target netDoctorProbeTarget) netDoctorProbeResult {
	result := netDoctorProbeResult{Target: target}
	started := time.Now()
	address := net.JoinHostPort(target.Address, fmt.Sprintf("%d", target.Port))
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialTLSContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			conn, err := netDoctorProbeDialContext(dialCtx, "tcp", address)
			if err != nil {
				return nil, err
			}
			result.TCPConnected = true
			tlsConn := tls.Client(conn, &tls.Config{ServerName: target.Hostname, RootCAs: netDoctorProbeRootCAs, MinVersion: tls.VersionTLS12})
			handshakeErr := tlsConn.HandshakeContext(dialCtx)
			state := tlsConn.ConnectionState()
			if len(state.PeerCertificates) > 0 {
				result.CertNotAfter = state.PeerCertificates[0].NotAfter
			}
			if handshakeErr != nil {
				_ = conn.Close()
				return nil, handshakeErr
			}
			result.TLSHandshook = true
			result.ChainValid = len(state.VerifiedChains) > 0
			return tlsConn, nil
		},
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+target.Hostname+"/", nil)
	if err != nil {
		result.Err = err
		return result
	}
	request.Host = target.Hostname
	response, err := (&http.Client{Transport: transport}).Do(request)
	result.Latency = time.Since(started)
	if err != nil {
		result.Err = err
		return result
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	result.HTTPStatusCode = response.StatusCode
	return result
}

func classifyNetDoctorProbeHTTP(statusCode int, readyBackends bool) doctorStatus {
	if (statusCode >= 200 && statusCode < 400) || statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return statusPass
	}
	if statusCode == http.StatusNotFound {
		return statusWarn
	}
	if statusCode >= 500 && readyBackends {
		return statusWarn
	}
	if statusCode >= 500 {
		return statusFail
	}
	return statusWarn
}

func classifyNetDoctorProbeResult(result netDoctorProbeResult, now time.Time) (doctorStatus, string) {
	address := net.JoinHostPort(result.Target.Address, fmt.Sprintf("%d", result.Target.Port))
	parts := []string{"gateway=" + result.Target.Gateway, "address=" + address}
	if !result.TCPConnected {
		parts = append(parts, "tcp=fail")
		if result.Err != nil {
			parts = append(parts, "error="+result.Err.Error())
		}
		return statusFail, strings.Join(parts, "; ")
	}
	parts = append(parts, "tcp=ok")
	if !result.TLSHandshook || !result.ChainValid {
		parts = append(parts, "tls=fail", "chain=invalid")
		if !result.CertNotAfter.IsZero() {
			days := int(result.CertNotAfter.Sub(now) / (24 * time.Hour))
			parts = append(parts, fmt.Sprintf("cert_expires_in=%dd", days))
		}
		if result.Err != nil {
			parts = append(parts, "error="+result.Err.Error())
		}
		return statusFail, strings.Join(parts, "; ")
	}
	days := int(result.CertNotAfter.Sub(now) / (24 * time.Hour))
	parts = append(parts, "tls=ok", "chain=valid", fmt.Sprintf("cert_expires_in=%dd", days))
	if result.Err != nil || result.HTTPStatusCode == 0 {
		parts = append(parts, "http=fail")
		if result.Err != nil {
			parts = append(parts, "error="+result.Err.Error())
		}
		parts = append(parts, "latency="+result.Latency.Round(time.Millisecond).String())
		return statusFail, strings.Join(parts, "; ")
	}
	status := classifyNetDoctorProbeHTTP(result.HTTPStatusCode, result.Target.ReadyBackends)
	parts = append(parts,
		fmt.Sprintf("http=%d", result.HTTPStatusCode),
		"ready_backends="+fmt.Sprintf("%t", result.Target.ReadyBackends),
		"latency="+result.Latency.Round(time.Millisecond).String())
	return status, strings.Join(parts, "; ")
}

func templateUsesImage(template netDoctorPodTemplate, substrings []string) bool {
	for _, container := range template.Spec.Containers {
		image := strings.ToLower(container.Image)
		for _, substring := range substrings {
			if strings.Contains(image, strings.ToLower(substring)) {
				return true
			}
		}
	}
	return false
}

func discoverNetDoctorWorkloads(deployments []netDoctorDeployment, daemonSets []netDoctorDaemonSet, imageSubstrings []string) []netDoctorWorkload {
	workloads := make([]netDoctorWorkload, 0)
	for _, deployment := range deployments {
		if !templateUsesImage(deployment.Spec.Template, imageSubstrings) {
			continue
		}
		desired := 1
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}
		workloads = append(workloads, netDoctorWorkload{
			Kind:           "Deployment",
			Metadata:       deployment.Metadata,
			Desired:        desired,
			Ready:          deployment.Status.ReadyReplicas,
			SelectorLabels: deployment.Spec.Selector.MatchLabels,
			TemplateLabels: deployment.Spec.Template.Metadata.Labels,
		})
	}
	for _, daemonSet := range daemonSets {
		if !templateUsesImage(daemonSet.Spec.Template, imageSubstrings) {
			continue
		}
		workloads = append(workloads, netDoctorWorkload{
			Kind:           "DaemonSet",
			Metadata:       daemonSet.Metadata,
			Desired:        daemonSet.Status.DesiredNumberScheduled,
			Ready:          daemonSet.Status.NumberReady,
			SelectorLabels: daemonSet.Spec.Selector.MatchLabels,
			TemplateLabels: daemonSet.Spec.Template.Metadata.Labels,
		})
	}
	sort.Slice(workloads, func(i, j int) bool {
		left := workloads[i].Kind + "/" + namespacedName(workloads[i].Metadata.Namespace, workloads[i].Metadata.Name)
		right := workloads[j].Kind + "/" + namespacedName(workloads[j].Metadata.Namespace, workloads[j].Metadata.Name)
		return left < right
	})
	return workloads
}

func addNetDoctorWorkloadReadiness(report *doctorReport, group string, workloads []netDoctorWorkload) {
	for _, workload := range workloads {
		status := statusPass
		if workload.Ready < workload.Desired {
			status = statusFail
		}
		report.add(group, workload.Kind, workload.Metadata.Namespace, workload.Metadata.Name, status,
			fmt.Sprintf("ready=%d/%d", workload.Ready, workload.Desired))
	}
}

func labelsMatch(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func workloadSelectsPod(workload netDoctorWorkload, podNamespace string, podLabels map[string]string) bool {
	selector := workload.SelectorLabels
	if len(selector) == 0 {
		selector = workload.TemplateLabels
	}
	return workload.Metadata.Namespace == podNamespace && labelsMatch(selector, podLabels)
}

func addNetDoctorTunnelPods(report *doctorReport, pods netDoctorPodList, workloads []netDoctorWorkload, now time.Time) {
	for _, pod := range pods.Items {
		matched := false
		for _, workload := range workloads {
			if workloadSelectsPod(workload, pod.Metadata.Namespace, pod.Metadata.Labels) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		status := statusPass
		details := make([]string, 0)
		for _, container := range pod.Status.ContainerStatuses {
			if container.State.Waiting != nil && container.State.Waiting.Reason == "CrashLoopBackOff" {
				status = statusFail
				details = append(details, fmt.Sprintf("container %s is CrashLoopBackOff", container.Name))
				continue
			}
			if container.RestartCount == 0 || container.LastState.Terminated == nil {
				continue
			}
			finished, err := time.Parse(time.RFC3339, container.LastState.Terminated.FinishedAt)
			if err == nil && !finished.After(now) && now.Sub(finished) <= netDoctorRestartWindow {
				if status != statusFail {
					status = statusWarn
				}
				details = append(details, fmt.Sprintf("container %s restarted %d time(s); last restart %s", container.Name, container.RestartCount, finished.Format(time.RFC3339)))
			}
		}
		if len(details) == 0 {
			details = append(details, "no CrashLoopBackOff or restart in the last hour")
		}
		report.add(netDoctorGroupTunnel, "Pod", pod.Metadata.Namespace, pod.Metadata.Name, status, strings.Join(details, "; "))
	}
}

func serviceAddress(service netDoctorService) string {
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if net.ParseIP(ingress.IP) != nil {
			return ingress.IP
		}
	}
	if net.ParseIP(service.Spec.LoadBalancerIP) != nil {
		return service.Spec.LoadBalancerIP
	}
	if net.ParseIP(service.Spec.ClusterIP) != nil {
		return service.Spec.ClusterIP
	}
	return ""
}

func dnsResolverService(services []netDoctorService, workloads []netDoctorWorkload) (netDoctorService, string, bool) {
	sortedServices := append([]netDoctorService(nil), services...)
	sort.Slice(sortedServices, func(i, j int) bool {
		return namespacedName(sortedServices[i].Metadata.Namespace, sortedServices[i].Metadata.Name) <
			namespacedName(sortedServices[j].Metadata.Namespace, sortedServices[j].Metadata.Name)
	})
	for _, service := range sortedServices {
		address := serviceAddress(service)
		if address == "" || len(service.Spec.Selector) == 0 {
			continue
		}
		for _, workload := range workloads {
			if workload.Metadata.Namespace == service.Metadata.Namespace && labelsMatch(service.Spec.Selector, workload.TemplateLabels) {
				return service, address, true
			}
		}
	}
	return netDoctorService{}, "", false
}

func addNetDoctorDNSProbes(ctx context.Context, report *doctorReport, services []netDoctorService, servicesOK bool, workloads []netDoctorWorkload, workloadsOK bool, hostnames []string) {
	if !workloadsOK {
		report.add(netDoctorGroupDNS, "DNSProbe", "", "all", statusFail, "cannot resolve because DNS controller workload discovery failed")
		return
	}
	if len(workloads) == 0 {
		report.add(netDoctorGroupDNS, "DNSProbe", "", "all", statusFail, "cannot resolve because no DNS controller workloads were discovered")
		return
	}
	if !servicesOK {
		report.add(netDoctorGroupDNS, "DNSProbe", "", "all", statusFail, "cannot resolve because Service discovery failed")
		return
	}
	service, serverIP, ok := dnsResolverService(services, workloads)
	if !ok {
		report.add(netDoctorGroupDNS, "Service", "", "all", statusFail, "no Service with a LoadBalancer or ClusterIP selects a discovered DNS controller workload")
		return
	}
	for _, hostname := range hostnames {
		hostname = strings.TrimSpace(hostname)
		if hostname == "" {
			report.add(netDoctorGroupDNS, "DNSProbe", "", "empty", statusFail, "hostname is empty")
			continue
		}
		addresses, err := netDoctorLookupFn(ctx, serverIP, hostname)
		if err != nil {
			report.add(netDoctorGroupDNS, "DNSProbe", "", hostname, statusFail, fmt.Sprintf("query %s:53: %v", serverIP, err))
			continue
		}
		sort.Strings(addresses)
		report.add(netDoctorGroupDNS, "DNSProbe", service.Metadata.Namespace, hostname, statusPass,
			fmt.Sprintf("%s via %s %s:53", strings.Join(addresses, ", "), namespacedName(service.Metadata.Namespace, service.Metadata.Name), serverIP))
	}
}

func netDoctorCertificateRefs(gateways []netDoctorGateway) []netDoctorCertRef {
	seen := map[string]bool{}
	var refs []netDoctorCertRef
	for _, gateway := range gateways {
		for _, listener := range gateway.Spec.Listeners {
			if listener.TLS == nil {
				continue
			}
			for _, ref := range listener.TLS.CertificateRefs {
				if (ref.Group != "" && ref.Group != "core") || (ref.Kind != "" && ref.Kind != "Secret") {
					continue
				}
				namespace := ref.Namespace
				if namespace == "" {
					namespace = gateway.Metadata.Namespace
				}
				key := namespacedName(namespace, ref.Name)
				if seen[key] {
					continue
				}
				seen[key] = true
				refs = append(refs, netDoctorCertRef{
					Namespace: namespace,
					Name:      ref.Name,
					Gateway:   namespacedName(gateway.Metadata.Namespace, gateway.Metadata.Name),
					Listener:  listener.Name,
				})
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		return namespacedName(refs[i].Namespace, refs[i].Name) < namespacedName(refs[j].Namespace, refs[j].Name)
	})
	return refs
}

func parseTLSCertificateNotAfter(raw []byte) (time.Time, error) {
	block, _ := pem.Decode(raw)
	if block != nil {
		raw = block.Bytes
	}
	certificate, err := x509.ParseCertificate(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse tls.crt: %w", err)
	}
	return certificate.NotAfter, nil
}

func classifyCertificateExpiry(notAfter, now time.Time) (doctorStatus, string) {
	remaining := notAfter.Sub(now)
	detail := fmt.Sprintf("expires %s (%s remaining)", notAfter.UTC().Format(time.RFC3339), remaining.Round(time.Hour))
	if !notAfter.After(now) {
		return statusFail, detail
	}
	if remaining < netDoctorCertWarnWindow {
		return statusWarn, detail
	}
	return statusPass, detail
}

func addNetDoctorCertificates(report *doctorReport, refs []netDoctorCertRef, secrets []netDoctorSecret, now time.Time) {
	secretMap := make(map[string]netDoctorSecret, len(secrets))
	for _, secret := range secrets {
		secretMap[namespacedName(secret.Metadata.Namespace, secret.Metadata.Name)] = secret
	}
	for _, ref := range refs {
		key := namespacedName(ref.Namespace, ref.Name)
		secret, ok := secretMap[key]
		if !ok {
			report.add(netDoctorGroupCertificates, "Secret", ref.Namespace, ref.Name, statusFail,
				fmt.Sprintf("missing; referenced by %s listener %s", ref.Gateway, ref.Listener))
			continue
		}
		raw := secret.Data["tls.crt"]
		if len(raw) == 0 {
			report.add(netDoctorGroupCertificates, "Secret", ref.Namespace, ref.Name, statusFail, "tls.crt is missing or empty")
			continue
		}
		notAfter, err := parseTLSCertificateNotAfter(raw)
		if err != nil {
			report.add(netDoctorGroupCertificates, "Secret", ref.Namespace, ref.Name, statusFail, err.Error())
			continue
		}
		status, detail := classifyCertificateExpiry(notAfter, now)
		report.add(netDoctorGroupCertificates, "Secret", ref.Namespace, ref.Name, status, detail)
	}
}
