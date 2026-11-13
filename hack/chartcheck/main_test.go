package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot is where build/*.Dockerfile are, from this package's directory.
const repoRoot = "../.."

// notRendered is the part of the message that refuses a kind the chart may not render.
const notRendered = "not one this chart may render"

// digest is an invented one, as the chart's ci values use.
const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

// controlDeployment is a Deployment the chart could render for the control service, written out so that each
// refusal below changes one line of it.
const controlDeployment = `apiVersion: workloads.gaon.garam.sh/v1alpha1
kind: Deployment
metadata:
  name: example-control
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: example-control
  template:
    image: "registry.example/control@` + digest + `"
    user: "65532:65532"
    resources:
      requests:
        cpu: "500m"
        memory: "512Mi"
    command:
      - /control
    env:
      - name: CONTROL_DATABASE_URL
        valueFrom:
          secretKeyRef:
            name: "control-database"
            key: "url"
    files:
      - path: /etc/control/tls/tls.key
        mode: 256
        valueFrom:
          secretKeyRef:
            name: "control-tls"
            key: "tls.key"
    ports:
      - name: health
        port: 8081
        protocol: TCP
    livenessProbe:
      httpGet:
        path: /healthz
        port: health
`

const controlService = `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Service
metadata:
  name: example-control
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: example-control
  ports:
    - name: https
      protocol: TCP
      port: 8080
`

func TestValidateObject_AcceptsTheGaonKindsTheChartRenders(t *testing.T) {
	kind, err := validateObject(controlDeployment, repoRoot)
	require.NoError(t, err)
	assert.Equal(t, kindDeployment, kind)

	kind, err = validateObject(controlService, repoRoot)
	require.NoError(t, err)
	assert.Equal(t, kindService, kind)
}

func TestValidateObject_RefusesWhatTheChartMustNotRender(t *testing.T) {
	// The accepted document is the control of every case: each case changes it by one replacement.
	_, err := validateObject(controlDeployment, repoRoot)
	require.NoError(t, err, "the unchanged Deployment must be accepted, or no refusal below is the change's")

	cases := []struct {
		name, from, to, want string
	}{
		{"a Kubernetes Deployment", "workloads.gaon.garam.sh/v1alpha1", "apps/v1", notRendered},
		{"a Secret", "kind: Deployment", "kind: Secret", notRendered},
		{"a ConfigMap", "kind: Deployment", "kind: ConfigMap", notRendered},
		{"an unknown field", "  replicas: 1\n", "  replicas: 1\n  paused: true\n", "unknown field"},
		{"an image with a tag", "control@" + digest, "control:v1", "not <repository>@sha256"},
		{"an image with a short digest", digest, "sha256:abc", "not <repository>@sha256"},
		{"no cpu request", "        cpu: \"500m\"\n", "", "no cpu or no memory"},
		{"no memory request", "        memory: \"512Mi\"\n", "", "no cpu or no memory"},
		{"no replicas", "  replicas: 1\n", "", "spec.replicas"},
		{"an unknown strategy", "type: Recreate", "type: Surge", "spec.strategy.type"},
		{"no selector", "      app.kubernetes.io/name: example-control\n", "", "matchLabels"},
		{"a user that is not the image's", "65532:65532", "10001:10001", "USER"},
		{"a user that is not numeric", "65532:65532", "nobody", "uid:gid"},
		{"a command with no Dockerfile", "- /control", "- /bin/sh", "knows a Dockerfile for"},
		{"a credential in an env value", "      - name: CONTROL_DATABASE_URL\n        valueFrom:\n          secretKeyRef:\n" +
			"            name: \"control-database\"\n            key: \"url\"\n",
			"      - name: CONTROL_DATABASE_URL\n        value: postgres://placeholder\n", "use valueFrom"},
		{"a file with content", "        valueFrom:\n          secretKeyRef:\n            name: \"control-tls\"\n" +
			"            key: \"tls.key\"\n", "        content: placeholder\n", "holds content"},
		{
			"a probe on a port nothing declares",
			"/healthz\n        port: health", "/healthz\n        port: missing", "livenessProbe",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := strings.Replace(controlDeployment, c.from, c.to, 1)
			require.NotEqual(t, controlDeployment, doc, "the replacement must change the document")
			_, err := validateObject(doc, repoRoot)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestValidateObject_RefusesAServiceGaonRefuses(t *testing.T) {
	doc := strings.Replace(controlService, "port: 8080", "port: 0", 1)
	require.NotEqual(t, controlService, doc)
	_, err := validateObject(doc, repoRoot)
	assert.Error(t, err)
}

// fakeHelm writes an executable that prints stderr and exits with code, in place of helm.
func fakeHelm(t *testing.T, stderr string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helm")
	script := "#!/bin/sh\necho '" + stderr + "' >&2\nexit " + string(rune('0'+code)) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// chartWith writes a chart whose values.yaml declares image.control and answers its directory.
func chartWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["Chart.yaml"] = "name: x\n"
	files["values.yaml"] = "image:\n  control: \"\"\n"
	for name, text := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(text), 0o644))
	}
	return dir
}

func TestCheckRefusal_RequiresTheGuardOfTheNamedValue(t *testing.T) {
	chart := chartWith(t, map[string]string{})
	minimal := filepath.Join(chart, "ci", "minimal-values.yaml")
	file := filepath.Join(chart, "ci", "refused", "image.control@tag.yaml")
	refusal := "Error: execution error at (x/templates/a.yaml:1:1): image.control must be a digest"

	// Control: a render that fails with the named value's message is a refusal.
	require.NoError(t, checkRefusal(fakeHelm(t, refusal, 1), chart, minimal, file))

	// A render that succeeds is not.
	err := checkRefusal(fakeHelm(t, "", 0), chart, minimal, file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not refuse image.control")

	// A render that fails for another value's guard is not.
	other := "Error: execution error at (x/templates/a.yaml:1:1): image.operator must be a digest"
	err = checkRefusal(fakeHelm(t, other, 1), chart, minimal, file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not by the guard of image.control")

	// A value that values.yaml does not declare is a misspelling, not a case.
	typo := filepath.Join(chart, "ci", "refused", "image.contrl.yaml")
	err = checkRefusal(fakeHelm(t, "Error: execution error at (x): image.contrl is required", 1), chart, minimal, typo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a value of values.yaml")

	// A helm that never ran refused nothing.
	err = checkRefusal(filepath.Join(t.TempDir(), "absent"), chart, minimal, file)
	require.ErrorIs(t, err, errHelmNotRun)
}

func TestScan_FindsWhatAChartMustNotHold(t *testing.T) {
	forbiddenText := map[string]string{
		"a PEM block":           "-----BEGIN PRIVATE KEY-----",
		"an image digest":       "image: a@" + digest,
		"a 12-digit account id": "account: 123456789012",
		"an AWS host":           "host: x.amazonaws.com",
		"a URL with a password": "url: https://user:pw@host.example",
	}
	clean := chartWith(t, map[string]string{
		"templates/a.yaml": "a: b\n",
		"ci/values.yaml":   forbiddenText["an image digest"],
	})
	n, err := scan(clean)
	require.NoError(t, err, "what ci/ holds is not scanned, and a clean chart passes")
	assert.Equal(t, 3, n, "Chart.yaml, values.yaml and the template are read, ci/ is not")

	for what, text := range forbiddenText {
		chart := chartWith(t, map[string]string{"templates/a.yaml": text})
		_, err := scan(chart)
		require.Error(t, err, what)
		assert.Contains(t, err.Error(), what)
	}
}

func TestDiscover_SelectsEveryChartOutsideCi(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a/Chart.yaml", "b/nested/Chart.yaml", "a/ci/Chart.yaml", "c/values.yaml"} {
		path := filepath.Join(root, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, nil, 0o644))
	}
	charts, err := discover(root)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(root, "a"), filepath.Join(root, "b", "nested")}, charts)
}

func TestSplitDocuments_DropsDocumentsThatAreOnlyComments(t *testing.T) {
	docs := splitDocuments("---\n# Source: a\nkind: A\n---\n# only a comment\n---\nkind: B\n")
	require.Len(t, docs, 2)
	assert.Contains(t, docs[0], "kind: A")
	assert.Contains(t, docs[1], "kind: B")
}

func TestRun_FailsWhenItSelectedNothing(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"-helm", "absent", "-charts", t.TempDir(), "-repo", repoRoot}, &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, out.String(), "charts=0", "the report says what was covered even when it was nothing")
	assert.Contains(t, errOut.String(), "count of charts is zero")
	assert.Contains(t, out.String(), "lib/api v0.0.0-", "the report names the gaon version it validated at")
}

func TestGaonPin_IsTheOneGoModNames(t *testing.T) {
	pin, err := gaonPin()
	require.NoError(t, err)
	mod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	assert.Contains(t, string(mod), gaonModule+" "+pin)
}
