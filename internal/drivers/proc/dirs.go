package proc

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Folders of a job belong to the job's uid, mode 0700; the server has no
// CAP_DAC_OVERRIDE and cannot enter them. DirSize and RemoveDir therefore
// run the shim as that uid (CAP_SETUID, which the server has anyway) and
// let it measure or delete: no chown, no new capability, and the work
// cannot reach anything the job itself could not.
//
//	server ── exec as uid ──► shim --du|--rm <dir>
const (
	modeSize   = "--du"
	modeRemove = "--rm"

	dirOwnerRWX = 0o700

	// keptDir is the shim's answer when it emptied dir but may not
	// unlink it.
	keptDir = "kept"
)

// DirSize returns the size of the files in dir in bytes, measured as uid
// (0 = no user switch). File sizes, not blocks: compression and delayed
// allocation (ZFS) make blocks vary. A missing dir is 0. Symlinks are
// not followed; a file with several links counts once.
func DirSize(shim string, uid uint32, dir string) (int64, error) {
	out, err := asUser(shim, uid, modeSize, dir)
	if err != nil {
		return 0, err
	}

	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// RemoveDir deletes dir as uid (0 = no user switch); a missing dir is no
// error. Symlinks are removed, not followed. If uid may not unlink dir
// itself (its parent is ours, e.g. tmp/run-<id>), the shim empties it and
// we remove the empty dir.
func RemoveDir(shim string, uid uint32, dir string) error {
	out, err := asUser(shim, uid, modeRemove, dir)
	if err != nil || strings.TrimSpace(out) != keptDir {
		return err
	}

	return os.Remove(dir)
}

// RemoveOwned deletes dir as its owner (RemoveDir), for dirs a job left.
func RemoveOwned(shim, dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	// Our own dir (tests, uid 0 runs): no user switch.
	var uid uint32
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		uid = st.Uid
	}

	return RemoveDir(shim, uid, dir)
}

func asUser(shim string, uid uint32, mode, dir string) (string, error) {
	exe, err := shimPath(shim)
	if err != nil {
		return "", err
	}

	var out, errOut bytes.Buffer
	cmd := exec.Command(exe, ShimArg, mode, dir)
	cmd.Env = []string{}
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if uid != 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", mode, dir, err, strings.TrimSpace(errOut.String()))
	}

	return out.String(), nil
}

// dirMain is the shim's side of DirSize and RemoveDir.
func dirMain(mode, dir string) int {
	var err error
	switch mode {
	case modeSize:
		var n int64
		if n, err = sizeOf(dir); err == nil {
			fmt.Println(n)
		}
	case modeRemove:
		var kept bool
		if kept, err = removeAll(dir); err == nil && kept {
			fmt.Println(keptDir)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jidoka-shim:", err)
		return 1
	}

	return 0
}

type inode struct{ dev, ino uint64 }

func sizeOf(dir string) (int64, error) {
	var total int64
	seen := map[inode]bool{}

	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	// uv may delete files while we count (a running job): skip those.
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}

		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}

		key := inode{uint64(st.Dev), uint64(st.Ino)}
		if seen[key] {
			return nil
		}
		seen[key] = true
		if info.Mode().IsRegular() {
			total += info.Size()
		}

		return nil
	})

	return total, err
}

// removeAll first makes every folder writable: uv keeps some read-only,
// and RemoveAll cannot empty those. It empties dir child by child, as
// os.RemoveAll(dir) opens dir's parent, which a job may not read
// (tmp/, 0711); dir itself goes only where uid may unlink it, else
// RemoveDir removes the empty dir (kept).
func removeAll(dir string) (bool, error) {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}

		return os.Chmod(p, dirOwnerRWX)
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}

	children, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, c := range children {
		if err := os.RemoveAll(filepath.Join(dir, c.Name())); err != nil {
			return false, err
		}
	}

	err = os.Remove(dir)
	if errors.Is(err, fs.ErrPermission) {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}
