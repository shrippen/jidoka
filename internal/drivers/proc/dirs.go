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

	// statBlock is the unit of syscall.Stat_t.Blocks.
	statBlock = 512

	dirOwnerRWX = 0o700
)

// DirSize returns the disk use of dir in bytes, measured as uid (0 = no
// user switch). A missing dir is 0. Symlinks are not followed; a file
// with several links counts once.
func DirSize(shim string, uid uint32, dir string) (int64, error) {
	out, err := asUser(shim, uid, modeSize, dir)
	if err != nil {
		return 0, err
	}

	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// RemoveDir deletes dir as uid (0 = no user switch); a missing dir is no
// error. Symlinks are removed, not followed.
func RemoveDir(shim string, uid uint32, dir string) error {
	_, err := asUser(shim, uid, modeRemove, dir)

	return err
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
		err = removeAll(dir)
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

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := d.Info()
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
		total += st.Blocks * statBlock

		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	return total, err
}

// removeAll first makes every folder writable: uv keeps some read-only,
// and RemoveAll cannot empty those.
func removeAll(dir string) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}

		return os.Chmod(p, dirOwnerRWX)
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return os.RemoveAll(dir)
}
