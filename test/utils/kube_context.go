package utils

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// KindCluster is the kind cluster this run owns — the one the e2e entry point
// created, and the only one the suite may act on.
func KindCluster() string {
	cluster := defaultKindCluster
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	return cluster
}

// CheckContext accepts current only as the context kind writes for the named
// cluster, and otherwise names what the run would have acted on instead.
func CheckContext(current, cluster string) error {
	want := "kind-" + cluster
	if current != want {
		return fmt.Errorf("kubectl resolves context %q, so every command this run makes would act on it; "+
			"this run owns kind cluster %q, whose context is %q", current, cluster, want)
	}
	return nil
}

// CheckKindContext reads the context kubectl resolves and accepts only the kind
// cluster this run owns. The suite calls it before its first write: kubectl
// takes its destination from ambient state any other process can move, and a
// destructive command that reaches a cluster the run did not create destroys it.
func CheckKindContext() error {
	current, err := Run(exec.Command("kubectl", "config", "current-context"))
	if err != nil {
		return fmt.Errorf("read the context kubectl resolves: %v", err)
	}
	return CheckContext(strings.TrimSpace(current), KindCluster())
}
