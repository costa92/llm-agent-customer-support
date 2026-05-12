package compose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDemoAssetsExistAndDocumentObservability(t *testing.T) {
	t.Helper()

	root := filepath.Dir(filepath.Dir(mustCallerFile(t)))

	composePath := filepath.Join(root, "compose", "compose.yaml")
	collectorPath := filepath.Join(root, "compose", "otel-collector.yaml")
	dashboardPath := filepath.Join(root, "dashboards", "customer-support-observability.json")
	readmePath := filepath.Join(root, "README.md")

	composeBody := mustReadFile(t, composePath)
	assertContainsAll(t, composeBody,
		"otel/opentelemetry-collector-contrib",
		"grafana/grafana",
		"ollama/ollama",
		"depends_on:",
		"OTEL_EXPORTER_OTLP_ENDPOINT: http://otel-collector:4318",
		`"name":"llama3.1:8b"`,
		`"name":"nomic-embed-text"`,
	)

	collectorBody := mustReadFile(t, collectorPath)
	assertContainsAll(t, collectorBody,
		"tail_sampling",
		"endpoint: 0.0.0.0:4317",
		"endpoint: 0.0.0.0:4318",
		"decision_wait: 30s",
		"- name: errors",
		"- name: high-latency",
		"- name: probabilistic-baseline",
		"status_code:",
		"latency:",
		"probabilistic:",
	)

	dashboardBody := mustReadFile(t, dashboardPath)
	var dashboard map[string]any
	if err := json.Unmarshal([]byte(dashboardBody), &dashboard); err != nil {
		t.Fatalf("unmarshal dashboard json: %v", err)
	}
	assertDashboardHasPanelTitles(t, dashboardBody,
		"Request Latency",
		"Token Usage",
		"Estimated Cost",
		"Error Rate",
		"Tool Success Ratio",
	)

	readmeBody := mustReadFile(t, readmePath)
	assertContainsAll(t, readmeBody,
		"Demo only",
		"docker compose -f compose/compose.yaml up --build",
		"curl http://localhost:8080/readyz",
		`curl -X POST http://localhost:8080/chat -H 'Content-Type: application/json' -d '{"message":"hello"}'`,
		"tail-sampling",
		"Grafana",
	)
}

func mustCallerFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return file
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func assertContainsAll(t *testing.T, body string, substrings ...string) {
	t.Helper()
	for _, needle := range substrings {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected %q in asset", needle)
		}
	}
}

func assertDashboardHasPanelTitles(t *testing.T, body string, titles ...string) {
	t.Helper()
	for _, title := range titles {
		if !strings.Contains(body, `"`+title+`"`) {
			t.Fatalf("expected dashboard panel title %q", title)
		}
	}
}
