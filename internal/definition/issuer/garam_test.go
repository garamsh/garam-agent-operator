package issuer_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/issuer"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const agent = "grn:acme:default:agent:0a1b"

// request is the certificate request every issuance here sends, and createRef its creation's reference.
var request = definition.CertificateRequest{RequestID: "c1", Epoch: "3", CSRPEM: "csr"}

const createRef = "create-ref"

// issuedAnswer is garam's answer naming every field of an issued certificate.
const issuedAnswer = `{"certificatePem":"cert","issuerPem":"issuer","serverRootPem":"root","notAfter":"2026-11-05T12:00:00Z"}`

// sent is what the stand-in garam last received.
type sent struct {
	path     string
	contract string
	body     map[string]string
}

func issue(t *testing.T, status int, answer string) (definition.IssuedCertificate, sent, error) {
	t.Helper()
	var got sent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.contract = r.URL.EscapedPath(), r.Header.Get("Garam-Contract-Version")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Garam-Contract-Version", garammachine.ManagedEnrollment)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	c, err := issuer.NewGaram(machine).Issue(context.Background(), definition.Issuance{
		Agent:        agent,
		Request:      request,
		OperationRef: createRef,
	})
	return c, got, err
}

func TestGaram_IssuesTheInitialCertificate(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"first issuance", http.StatusCreated},
		{"identical retry", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, got, err := issue(t, tt.status, issuedAnswer)
			require.NoError(t, err)
			assert.Equal(t, definition.IssuedCertificate{
				CertificatePEM: "cert", IssuerPEM: "issuer", ServerRootPEM: "root",
				NotAfter: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
			}, c)
			assert.Equal(t, "/agents/"+agent+"/initial-certificate", got.path)
			assert.Equal(t, garammachine.ManagedEnrollment, got.contract)
			assert.Equal(t, map[string]string{
				"requestId": "c1", "epoch": "3", "certificateRequestPem": "csr", "operationRef": "create-ref",
			}, got.body)
		})
	}
}

func TestGaram_RefusalsCarryGaramsKind(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		refusal definition.Refusal
	}{
		{"403 is forbidden", http.StatusForbidden, definition.RefusalForbidden},
		{"404 is forbidden", http.StatusNotFound, definition.RefusalForbidden},
		{"409 is a conflict", http.StatusConflict, definition.RefusalConflict},
		{"422 is invalid", http.StatusUnprocessableEntity, definition.RefusalInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := issue(t, tt.status, `{"kind":"some_kind","message":"refused"}`)
			var refused *definition.IssuanceRefusedError
			require.ErrorAs(t, err, &refused)
			assert.Equal(t, definition.IssuanceRefusedError{Refusal: tt.refusal, Kind: "some_kind", Message: "refused"}, *refused)
		})
	}

	// Control: 503 on every attempt is undecided, and a 400 is the call's own failure; neither is a refusal.
	for status, undecided := range map[int]bool{http.StatusServiceUnavailable: true, http.StatusBadRequest: false} {
		_, _, err := issue(t, status, `{"kind":"some_kind","message":"no"}`)
		var refused *definition.IssuanceRefusedError
		assert.False(t, errors.As(err, &refused), "status %d", status)
		assert.Equal(t, undecided, errors.Is(err, definition.ErrIssuanceUndecided), "status %d", status)
	}
}

func TestGaram_IncompleteAnswerRefused(t *testing.T) {
	for _, answer := range []string{
		`{"certificatePem":"","issuerPem":"issuer","serverRootPem":"root","notAfter":"2026-11-05T12:00:00Z"}`,
		`{"certificatePem":"cert","issuerPem":"","serverRootPem":"root","notAfter":"2026-11-05T12:00:00Z"}`,
		`{"certificatePem":"cert","issuerPem":"issuer","serverRootPem":"","notAfter":"2026-11-05T12:00:00Z"}`,
		`{"certificatePem":"cert","issuerPem":"issuer","serverRootPem":"root","notAfter":"tomorrow"}`,
	} {
		_, _, err := issue(t, http.StatusCreated, answer)
		require.Error(t, err, answer)
	}

	// Control: the same answer naming every field is read.
	_, _, err := issue(t, http.StatusCreated, issuedAnswer)
	require.NoError(t, err)
}

func TestGaram_AnAnswerUnderAnotherContractOrNoneDecidesNothing(t *testing.T) {
	for name, header := range map[string]*string{
		"the contract asked": ptrTo(garammachine.ManagedEnrollment), "another contract": ptrTo("managed-enrollment.v2"),
		"no contract": nil,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if header != nil {
					w.Header().Set("Garam-Contract-Version", *header)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(issuedAnswer))
			}))
			t.Cleanup(server.Close)
			_, err := issuer.NewGaram(garammachine.New(server.URL, server.Client())).Issue(context.Background(),
				definition.Issuance{Agent: agent, Request: request,
					OperationRef: createRef})
			if header != nil && *header == garammachine.ManagedEnrollment {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, definition.ErrGaramContractUnsupported)
			var refused *definition.IssuanceRefusedError
			assert.NotErrorAs(t, err, &refused, "a refusal would clear the pending request")
		})
	}
}

func ptrTo(s string) *string { return &s }
