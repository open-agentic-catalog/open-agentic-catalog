//go:build minikube

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// verifyMonitoringDiscoveryRecovery qualifies collector failure independently of
// Catalog readiness. Every mutated object belongs to this invocation's cluster.
func (h *harness) verifyMonitoringDiscoveryRecovery(archive, name string, client *http.Client) error {
	checkCatalog, err := h.monitoringCatalogControl(client)
	if err != nil {
		return err
	}
	for _, claim := range []string{"monitoring.prometheus-history", "monitoring.grafana-history"} {
		if err := h.applyObject("create separate retained collector claim", kubernetesObject{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]string{"name": claim, "namespace": monitoringNamespace}, "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": "1Gi"}}}}); err != nil {
			return err
		}
	}
	values, err := h.monitoringValuesIn(true, namespace)
	if err != nil {
		return err
	}
	if _, err := h.helm("attach separate external monitoring claims", "upgrade", monitoringRelease, archive, "-n", monitoringNamespace, "-f", values, "--wait", "--timeout=3m"); err != nil {
		return err
	}
	inputs, err := h.monitoringExternalInputs()
	if err != nil {
		return err
	}
	address, err := h.startPortForwardIn(monitoringNamespace, "service/"+name+"-prometheus", 9090)
	if err != nil {
		return err
	}
	prometheus := monitoringClient{client: client, base: "http://" + address}
	if err := h.monitoringCurrentSamples(prometheus); err != nil {
		return err
	}
	metric := `open_agentic_catalog_go_heap_bytes{catalog_deployment=` + strconv.Quote("mk-"+h.suffix) + `}`
	retainedAt := monitoringTimestamp(time.Now())
	if err := h.monitoringQuery(prometheus, metric, retainedAt, 4); err != nil {
		return err
	}
	raw, err := h.kubectl("save only owned discovery binding", "-n", namespace, "get", "rolebinding/"+name+"-discovery", "-o", "json")
	if err != nil {
		return err
	}
	binding, err := monitoringRestorableBinding(raw, name)
	if err != nil {
		return err
	}
	if _, err := h.kubectl("remove only owned cross-namespace binding", "-n", namespace, "delete", "rolebinding/"+name+"-discovery", "--wait=true"); err != nil {
		return err
	}
	collector := "system:serviceaccount:" + monitoringNamespace + ":" + name + "-prometheus"
	if err := h.monitoringEventually("live collector Pod list denial", func() error {
		_, err := h.kubectl("exercise forbidden collector Pod list", "-n", namespace, "get", "pods", "--as="+collector, "-o", "name")
		if err == nil || !monitoringDiscoveryForbidden(err.Error(), collector) {
			return errors.New("expected explicit forbidden target Pod listing")
		}
		return nil
	}); err != nil {
		return err
	}
	// A restarted reflector has no old watch or discovery cache. External storage
	// deliberately keeps older samples, which cannot establish current collection.
	before, err := h.monitoringCollectorUID(name)
	if err != nil {
		return err
	}
	restartAt := time.Now()
	if _, err := h.kubectl("restart collector without discovery authority", "-n", monitoringNamespace, "rollout", "restart", "deployment/"+name+"-prometheus"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for restarted collector process", "-n", monitoringNamespace, "rollout", "status", "deployment/"+name+"-prometheus", "--timeout=2m"); err != nil {
		return err
	}
	after, err := h.monitoringCollectorUID(name)
	if err != nil {
		return err
	}
	if before == after {
		return errors.New("collector did not replace its discovery process")
	}
	address, err = h.startPortForwardIn(monitoringNamespace, "service/"+name+"-prometheus", 9090)
	if err != nil {
		return err
	}
	prometheus.base = "http://" + address
	if err := h.monitoringEventually("visible fresh discovery failure", func() error {
		logs, err := h.kubectl("inspect collector discovery failure", "-n", monitoringNamespace, "logs", "deployment/"+name+"-prometheus", "--tail=100")
		if err != nil {
			return err
		}
		if !monitoringDiscoveryForbidden(string(logs), collector) {
			return errors.New("collector has no explicit discovery denial")
		}
		targets, err := h.monitoringTargetInventory(prometheus)
		if err != nil {
			return err
		}
		if len(targets) != 0 {
			return errors.New("restarted unauthorized collector still discovers targets")
		}
		return h.monitoringQuery(prometheus, metric+` and (timestamp(`+metric+`) > `+monitoringTimestamp(restartAt)+`)`, "", 0)
	}); err != nil {
		return err
	}
	if err := h.monitoringQuery(prometheus, metric, retainedAt, 4); err != nil {
		return fmt.Errorf("collector lost its historical control: %w", err)
	}
	if err := checkCatalog(); err != nil {
		return err
	}
	restoredAt := time.Now()
	if err := h.applyObject("restore exact owned discovery binding", binding); err != nil {
		return err
	}
	if err := h.monitoringCrossNamespaceAuthority(name); err != nil {
		return err
	}
	if err := h.monitoringCurrentSamplesSince(prometheus, restoredAt); err != nil {
		return err
	}
	if err := checkCatalog(); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "monitoring-cross-namespace-discovery-denial-and-fresh-recovery")
	for _, remove := range []bool{false, true} {
		if remove {
			if _, err := h.helm("uninstall only cross-namespace monitoring release", "uninstall", monitoringRelease, "-n", monitoringNamespace, "--wait", "--timeout=2m"); err != nil {
				return err
			}
		} else if _, err := h.helm("disable only cross-namespace monitoring release", "upgrade", monitoringRelease, archive, "-n", monitoringNamespace, "--reuse-values", "--set", "enabled=false", "--wait", "--timeout=2m"); err != nil {
			return err
		}
		if err := h.monitoringEventually("cross-namespace companion removal", func() error { return h.monitoringCrossNamespaceAbsent(name) }); err != nil {
			return err
		}
		retained, err := h.monitoringExternalInputs()
		if err != nil {
			return err
		}
		if !bytes.Equal(inputs, retained) {
			return errors.New("collector removal changed external Secret or PVC identities")
		}
		if err := checkCatalog(); err != nil {
			return err
		}
		if !remove {
			if _, err := h.helm("re-enable cross-namespace retained monitoring", "upgrade", monitoringRelease, archive, "-n", monitoringNamespace, "-f", values, "--wait", "--timeout=3m"); err != nil {
				return err
			}
			address, err = h.startPortForwardIn(monitoringNamespace, "service/"+name+"-prometheus", 9090)
			if err != nil {
				return err
			}
			prometheus.base = "http://" + address
			if err := h.monitoringCurrentSamples(prometheus); err != nil {
				return err
			}
		}
	}
	h.report.Checks = append(h.report.Checks, "monitoring-cross-namespace-disable-uninstall-and-external-input-retention")
	return nil
}

func monitoringTimestamp(at time.Time) string {
	return strconv.FormatFloat(float64(at.UnixNano())/1e9, 'f', 3, 64)
}

func monitoringDiscoveryForbidden(message, collector string) bool {
	// Kubernetes errors can appear directly or inside an escaped log field.
	message = strings.ReplaceAll(message, `\"`, `"`)
	return strings.Contains(strings.ToLower(message), "forbidden") &&
		strings.Contains(message, `cannot list resource "pods"`) &&
		strings.Contains(message, `User `+strconv.Quote(collector)) &&
		strings.Contains(message, `namespace `+strconv.Quote(namespace))
}

// monitoringRestorableBinding preserves chart ownership while removing server
// identity fields that Kubernetes cannot accept when recreating a deleted object.
func monitoringRestorableBinding(raw []byte, name string) (kubernetesObject, error) {
	if err := monitoringAuthorityDocument(raw, "rolebinding", name); err != nil {
		return nil, err
	}
	var binding kubernetesObject
	if err := json.Unmarshal(raw, &binding); err != nil {
		return nil, err
	}
	metadata, ok := binding["metadata"].(map[string]any)
	if !ok || metadata["name"] != name+"-discovery" {
		return nil, errors.New("unexpected discovery binding identity")
	}
	for _, field := range []string{"uid", "resourceVersion", "creationTimestamp", "managedFields", "generation", "selfLink"} {
		delete(metadata, field)
	}
	return binding, nil
}

func (h *harness) monitoringCollectorUID(name string) (string, error) {
	raw, err := h.kubectl("read current collector Pod identity", "-n", monitoringNamespace, "get", "pods", "-l", "app.kubernetes.io/instance="+monitoringRelease+",app.kubernetes.io/component=prometheus", "-o", "jsonpath={.items[*].metadata.uid}")
	if err != nil {
		return "", err
	}
	uids := strings.Fields(string(raw))
	if len(uids) != 1 {
		return "", errors.New("expected one current collector Pod")
	}
	// Assert selection still resolves the expected workload, independent of UID.
	if _, err := h.kubectl("verify selected collector workload", "-n", monitoringNamespace, "get", "deployment/"+name+"-prometheus"); err != nil {
		return "", err
	}
	return uids[0], nil
}

func (h *harness) monitoringCrossNamespaceAbsent(name string) error {
	raw, err := h.kubectl("verify collector namespace removal", "-n", monitoringNamespace, "get", "pods,deployments,services,configmaps,serviceaccounts,roles,rolebindings", "-l", "app.kubernetes.io/name=open-agentic-catalog-monitoring,app.kubernetes.io/instance="+monitoringRelease, "-o", "json")
	if err != nil {
		return err
	}
	if err := monitoringEmptyInventory(raw); err != nil {
		return err
	}
	// Select exact names: the earlier same-namespace release owns other bindings.
	raw, err = h.kubectl("verify exact target namespace RBAC removal", "-n", namespace, "get", "roles,rolebindings", "--field-selector=metadata.name="+name+"-discovery", "-o", "json")
	if err != nil {
		return err
	}
	return monitoringEmptyInventory(raw)
}

func monitoringEmptyInventory(raw []byte) error {
	var inventory struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return err
	}
	if inventory.Items == nil || len(inventory.Items) != 0 {
		return errors.New("monitoring resources remain or inventory is missing")
	}
	return nil
}

// Only immutable identities and binding state are retained, never Secret data.
func (h *harness) monitoringExternalInputs() ([]byte, error) {
	var result bytes.Buffer
	for _, resource := range []string{"secret/monitoring.admin", "pvc/monitoring.prometheus-history", "pvc/monitoring.grafana-history"} {
		raw, err := h.kubectl("verify independent monitoring input", "-n", monitoringNamespace, "get", resource, "-o", "jsonpath={.metadata.uid} {.status.phase}")
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(string(raw))
		if len(fields) == 0 || (strings.HasPrefix(resource, "pvc/") && (len(fields) != 2 || fields[1] != "Bound")) {
			return nil, errors.New("external collector input is missing or unbound")
		}
		fmt.Fprintf(&result, "%s %s\n", resource, string(raw))
	}
	return result.Bytes(), nil
}

// monitoringCatalogControl captures useful retained reads and the original four
// process Pods. Later checks require readiness, exact data and unchanged Pods.
func (h *harness) monitoringCatalogControl(client *http.Client) (func() error, error) {
	type probe struct {
		base   string
		search []byte
	}
	var probes []probe
	original := map[string][]string{}
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return nil, err
		}
		if len(pods) != 2 {
			return nil, errors.New("expected two Catalog process Pods")
		}
		for _, pod := range pods {
			if !pod.Ready {
				return nil, errors.New("catalog baseline is unready")
			}
			original[role] = append(original[role], pod.UID)
			port := 8080
			if role == "worker" {
				port = 9090
			}
			address, err := h.startPortForward("pod/"+pod.Name, port)
			if err != nil {
				return nil, err
			}
			p := probe{base: "http://" + address}
			if role == "api" {
				result, err := h.request(http.MethodPost, p.base+"/api/v1/catalog/search", h.tokens["reader"], "", "", map[string]int{"limit": 20}, client)
				if err != nil || !validInstrumentationSearch(result) {
					return nil, errors.New("retained Catalog baseline is invalid")
				}
				p.search = result.Body
			}
			probes = append(probes, p)
		}
	}
	return func() error {
		for role, uids := range original {
			pods, err := h.pods(role)
			if err != nil {
				return err
			}
			if len(pods) != len(uids) {
				return errors.New("catalog replica count changed during collector failure")
			}
			for _, pod := range pods {
				if !pod.Ready || !slices.Contains(uids, pod.UID) {
					return errors.New("catalog Pod changed or became unready during collector failure")
				}
			}
		}
		for _, p := range probes {
			ready, err := h.request(http.MethodGet, p.base+"/readyz", "", "", "", nil, client)
			if err != nil || ready.Status != http.StatusOK {
				return errors.New("catalog readiness failed during collector failure")
			}
			if p.search != nil {
				result, err := h.request(http.MethodPost, p.base+"/api/v1/catalog/search", h.tokens["reader"], "", "", map[string]int{"limit": 20}, client)
				if err != nil || !validInstrumentationSearch(result) || !bytes.Equal(p.search, result.Body) {
					return errors.New("retained Catalog read changed during collector failure")
				}
			}
		}
		return nil
	}, nil
}
