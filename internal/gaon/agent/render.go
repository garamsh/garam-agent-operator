package agent

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/garamsh/gaon/lib/api"

	"github.com/garamsh/garam-agent-operator/internal/gaon/guest"
	"github.com/garamsh/garam/lib/errorx"
)

// The MicroVM runs as root, since the supervisor drops each program
// itself (ADR-0153); credential files belong to the account of the
// runtime and the adapter. Also where the guest finds what the operator
// gives it.
const (
	guestUser        = "0:0"
	credentialUID    = guest.UID
	credentialsDir   = guest.CredentialsDir
	memoryMountPath  = guest.MemoryDir
	workspaceMount   = guest.WorkspaceDir
	supervisorPort   = "supervisor"
	supervisorNumber = guest.ReadyPort
	fileMode         = 0o400
)

// The Secret keys the operator adds to the agent's credentials.
const (
	KeyGaramRoot   = guest.GaramRootFile
	KeyControlRoot = guest.ControlRootFile
	// KeyPlacementToken is minted by the operator, never by garam.
	KeyPlacementToken = guest.PlacementTokenFile
)

// secretFor returns the Secret of d: the credentials, the two trust
// roots the guest reads and the placement token.
func (s *service) secretFor(d Desired, c Credentials, placement []byte) (*api.Secret, error) {
	data := map[string][]byte{
		KeyGaramRoot: s.opts.GaramRoot, KeyControlRoot: s.opts.ControlRoot,
		KeyPlacementToken: placement,
	}
	for name, value := range c {
		if _, taken := data[name]; taken {
			return nil, errorx.New(errorx.KindInvalidArgument, fmt.Sprintf("credential file %q collides with a trust root", name))
		}
		data[name] = value
	}
	return &api.Secret{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindSecret},
		Metadata: metaFor(namesOf(d.GRN).secret, d),
		Data:     data,
	}, nil
}

// Render returns the Secret and the MicroVM the operator writes for d
// when it creates the agent, given the credentials garam minted. It is
// what a reconcile of a new agent writes and nothing more, so a run of
// the guest image can start from the rendered object rather than from
// a copy of it.
//
// models are the Secrets a model key reference may name; a reference to
// another is refused as it is in a reconcile.
func Render(o Options, d Desired, c Credentials, models []api.Secret) (*api.Secret, *api.MicroVM, error) {
	s := &service{opts: o}
	token, err := mintPlacementToken()
	if err != nil {
		return nil, nil, err
	}
	secret, err := s.secretFor(d, c, token)
	if err != nil {
		return nil, nil, err
	}
	m, err := s.microVMFor(d, secret, modelKeysFor(d.GRN, models))
	if err != nil {
		return nil, nil, err
	}
	return secret, m, nil
}

// volumeFor returns one of d's two Volumes. A Volume outlives its
// MicroVM and only a person removes it, so it carries no owner.
func volumeFor(name string, d Desired, size api.Quantity) *api.Volume {
	return &api.Volume{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume},
		Metadata: metaFor(name, d),
		Spec: api.VolumeSpec{
			StorageClass: api.StorageClassLocal,
			AccessModes:  []string{api.AccessReadWriteOnce},
			Storage:      size,
		},
	}
}

// microVMFor returns d's MicroVM. Every credential is a file sourced
// from the Secret and none is an inline value: a Node may read every
// MicroVM, so the spec is not where a secret lives. What is inline is
// the runtime's configuration and ego, which hold none (ADR 0154).
func (s *service) microVMFor(d Desired, secret *api.Secret, models modelKeys) (*api.MicroVM, error) {
	settings, err := runtimeFor(d.Configuration, models)
	if err != nil {
		return nil, errorx.Wrap(err, "render the configuration")
	}
	n := namesOf(d.GRN)
	keys := make([]string, 0, len(secret.Data))
	for k := range secret.Data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	mode, owner := int32(fileMode), api.FileOwner{UID: credentialUID, GID: credentialUID}
	files := make([]api.MicroVMFile, 0, len(keys))
	for _, k := range keys {
		files = append(files, api.MicroVMFile{
			Path:      credentialsDir + k,
			Mode:      &mode,
			Owner:     &owner,
			ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: n.secret, Key: k}},
		})
	}
	return &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: metaFor(n.microvm, d),
		Spec: api.MicroVMSpec{
			Image:       s.opts.Image,
			Command:     []string{guest.BinaryPath},
			Env:         append(s.envFor(d), settings.env...),
			User:        guestUser,
			Resources:   s.opts.Resources,
			RunStrategy: strategyFor(d),
			Files:       append(files, settings.files...),
			Volumes: []api.MicroVMVolume{
				{Name: n.memory, MountPath: memoryMountPath},
				{Name: n.workspace, MountPath: workspaceMount},
			},
			Ports:          []api.MicroVMPort{{Name: supervisorPort, Port: supervisorNumber, Protocol: api.ProtocolTCP}},
			ReadinessProbe: &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Name: supervisorPort}}},
		},
	}, nil
}

// envFor returns the guest's environment the supervisor reads, each a
// value that is an address or an identifier. The runtime's own, the
// file paths and the keys sourced from Secrets, come from runtimeFor.
func (s *service) envFor(d Desired) []api.EnvVar {
	return []api.EnvVar{
		{Name: guest.EnvAgent, Value: d.GRN.String()},
		{Name: guest.EnvEpoch, Value: strconv.FormatInt(d.Epoch, 10)},
		{Name: guest.EnvMachineURL, Value: s.opts.MachineURL},
		{Name: guest.EnvControlURL, Value: s.opts.ControlURL},
	}
}

// strategyFor is the run strategy the feed asks for: Always restarts an
// exited guest, and Halted is the stop (ADR-0151 §5).
func strategyFor(d Desired) api.RunStrategy {
	if d.Stopped {
		return api.RunHalted
	}
	return api.RunAlways
}
