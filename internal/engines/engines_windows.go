package engines

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func installed() []Engine {
	var out []Engine
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	for _, a := range []struct{ name, path string }{
		{"Docker Desktop", filepath.Join(pf, `Docker\Docker\Docker Desktop.exe`)},
		{"Rancher Desktop", filepath.Join(pf, `Rancher Desktop\Rancher Desktop.exe`)},
	} {
		if _, err := os.Stat(a.path); err == nil {
			out = append(out, Engine{Name: a.name, Runtime: "docker", start: startDesktopApp(a.path)})
		}
	}
	if podmanMachine() {
		out = append(out, Engine{Name: "Podman", Runtime: "podman", start: command("podman", "machine", "start")})
	}
	return out
}

// startDesktopApp starts a desktop app. A Windows service runs in session
// 0, where desktop apps can't run, so it starts the app in the session of
// whoever is signed in at the screen, as them.
func startDesktopApp(path string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if isSvc, _ := svc.IsWindowsService(); !isSvc {
			return exec.Command(path).Start()
		}
		return startAsConsoleUser(path)
	}
}

func startAsConsoleUser(path string) error {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF {
		return engineError("nobody is signed in to this PC, and Docker Desktop only runs in someone's session")
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return engineError("nobody is signed in to this PC (" + err.Error() + ")")
	}
	defer token.Close()
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, token, false); err != nil {
		return err
	}
	defer windows.DestroyEnvironmentBlock(env)
	desktop, _ := syscall.UTF16PtrFromString(`winsta0\default`)
	cmdline, _ := syscall.UTF16PtrFromString(`"` + path + `"`)
	dir, _ := syscall.UTF16PtrFromString(filepath.Dir(path))
	si := windows.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	err := windows.CreateProcessAsUser(token, nil, cmdline, nil, nil, false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NEW_PROCESS_GROUP, env, dir, &si, &pi)
	if err != nil {
		return err
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return nil
}

type engineError string

func (e engineError) Error() string { return string(e) }
