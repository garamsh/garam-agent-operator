package controller

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// referenceEvidence and its canonical form are the vector the digest is checked
// against. The canonical text is written out by hand from RFC 8785 §3.2: object
// members sorted by their names' UTF-16 code units, no insignificant
// whitespace. referenceDigest is its SHA-256 as `sha256sum` printed it, outside
// Go, so neither depends on the code under test.
const (
	referenceCanonical = `{"containers":[{"containerID":"containerd://a","exitCode":143,"finishedAt":"2026-10-05T01:02:03Z","name":"agent"}],` +
		`"observedAt":"2026-10-05T01:02:04Z","podUID":"pod-1","pvcUID":"pvc-1"}`
	referenceDigest = "473d9710615aa2ff2fba8953886482ddfd40c00c5c184d3980d926bee120cd50"
)

func referenceEvidence() *agentv1alpha1.WriterStoppedEvidence {
	return &agentv1alpha1.WriterStoppedEvidence{
		PodUID: "pod-1",
		PVCUID: "pvc-1",
		Containers: []agentv1alpha1.TerminatedContainer{{
			Name: "agent", ContainerID: "containerd://a", ExitCode: 143,
			FinishedAt: metav1.NewTime(time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC)),
		}},
		ObservedAt: metav1.NewTime(time.Date(2026, 10, 5, 1, 2, 4, 0, time.UTC)),
	}
}

func TestWriterStoppedDigestIsTheSHA256OfTheCanonicalJSON(t *testing.T) {
	// The control: the evidence as written to status is not already canonical,
	// so a digest of it is a different digest. Without that, a digest that
	// skipped canonicalization would pass for this vector.
	written, err := json.Marshal(referenceEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if string(written) == referenceCanonical {
		t.Fatalf("the vector is already canonical as written, so it cannot tell a canonical digest apart: %s", written)
	}

	digest, err := writerStoppedDigest(referenceEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if digest != referenceDigest {
		t.Fatalf("digest %s, want %s, the SHA-256 of %s", digest, referenceDigest, referenceCanonical)
	}
}
