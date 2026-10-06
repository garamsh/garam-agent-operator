// Package secretref states and parses a reference to one data key of a Kubernetes Secret.
package secretref

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ErrInvalid is returned for a reference that is not "<secret-name>/<key>".
var ErrInvalid = errors.New("reference is not <secret-name>/<key>")

// Parse splits ref, of the form "<secret-name>/<key>", into a Secret's name and one of its data
// keys, in the namespace the reference is read in. The name is a DNS subdomain, as Kubernetes
// requires of a Secret's name (k8s.io/apimachinery@v0.36.0 pkg/util/validation IsDNS1123Subdomain),
// and the key matches [-._a-zA-Z0-9]+ and is neither "." nor "..", as Kubernetes requires of a
// Secret's data key (IsConfigMapKey). Neither can hold a "/", so the split is unambiguous. This is
// the one statement of the form: the control service and the manager both parse with it.
func Parse(ref string) (name, key string, err error) {
	// A reference with no "/" leaves the key empty, which IsConfigMapKey refuses.
	name, key, _ = strings.Cut(ref, "/")
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return "", "", fmt.Errorf("%w: secret name %q: %s", ErrInvalid, name, strings.Join(problems, "; "))
	}
	if problems := validation.IsConfigMapKey(key); len(problems) > 0 {
		return "", "", fmt.Errorf("%w: key %q: %s", ErrInvalid, key, strings.Join(problems, "; "))
	}

	return name, key, nil
}
