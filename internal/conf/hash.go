package conf

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	shortCommit = 7
	refPrefix   = "ref: "
)

// hashDir hashes the regular files of a folder (path and content, sorted,
// dotfiles skipped) so a reload can tell a changed job (J20).
func hashDir(dir string) (string, error) {
	h := sha256.New()

	// WalkDir visits in lexical order, so the hash is stable.
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)

		// Length prefixes keep "a"+"bc" and "ab"+"c" apart.
		h.Write([]byte(strconv.Itoa(len(rel)) + ":" + filepath.ToSlash(rel) + "\n"))
		h.Write([]byte(strconv.Itoa(len(data)) + ":"))
		h.Write(data)

		return nil
	})
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// readCommit returns the short commit of <dir>/.git/HEAD, "" if unknown.
func readCommit(dir string) string {
	gitDir := filepath.Join(dir, ".git")

	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(head))

	ref, ok := strings.CutPrefix(text, refPrefix)
	if !ok {
		return short(text)
	}

	if b, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		return short(strings.TrimSpace(string(b)))
	}

	return packedRef(gitDir, ref)
}

// packedRef looks a ref up in .git/packed-refs ("<sha> <ref>" lines).
func packedRef(gitDir, ref string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(b), "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && name == ref {
			return short(sha)
		}
	}

	return ""
}

func short(sha string) string {
	if len(sha) < shortCommit {
		return ""
	}

	return sha[:shortCommit]
}
