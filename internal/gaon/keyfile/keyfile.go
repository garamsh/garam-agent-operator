// Package keyfile reads a file that holds a private key. It is the rule
// of platform/internal/keyfile (ADR-0015), which a separate module
// cannot import: a key file is refused where another user can read it
// or where it belongs to another user.
package keyfile

import (
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/garamsh/garam/lib/errorx"
)

// Read returns the contents of path, refusing a file any group or other
// user can reach and one owned by a user other than the process. The
// mode and owner are read from the open descriptor, so no window lies
// between the check and the read. The error names the path and never
// the contents.
func Read(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errorx.Wrap(err, "open key file "+path, errorx.KindInvalidArgument)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, errorx.Wrap(err, "stat key file "+path, errorx.KindInvalidArgument)
	}
	if info.Mode().Perm()&fs.FileMode(0o077) != 0 {
		return nil, errorx.New(errorx.KindInvalidArgument, "key file "+path+" is readable beyond its owner")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errorx.New(errorx.KindInvalidArgument, "key file "+path+" ownership is unreadable")
	}
	if uint64(stat.Uid) != uint64(os.Getuid()) {
		return nil, errorx.New(errorx.KindInvalidArgument, "key file "+path+" is owned by another user")
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, errorx.Wrap(err, "read key file "+path, errorx.KindInvalidArgument)
	}
	return raw, nil
}
