package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// systemCopy puts sys0.exe and sys1.exe in %ProgramFiles%\sys1 and returns
// the path the service should run. A service runs as SYSTEM, so its
// binary must not sit somewhere an ordinary account can change (such as
// %LOCALAPPDATA%, where install.ps1 puts it); Windows Security also trusts
// a service under Program Files more.
func systemCopy() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	dest := filepath.Join(pf, "sys1")
	if strings.EqualFold(filepath.Dir(exe), dest) {
		return exe, nil
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	for _, name := range []string{"sys0.exe", "sys1.exe"} {
		src := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, filepath.Join(dest, name)); err != nil {
			return "", fmt.Errorf("copying %s to %s: %w", name, dest, err)
		}
	}
	fmt.Println("Installed to", dest)
	return filepath.Join(dest, filepath.Base(exe)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// A running service holds its binary open; Windows allows renaming it
	// out of the way, not replacing it.
	os.Remove(dst + ".old")
	os.Rename(dst, dst+".old")
	return os.Rename(tmp, dst)
}
