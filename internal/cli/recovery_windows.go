package cli

import (
	"log/slog"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

// ensureRecovery sets the service to be restarted whenever it exits
// without being asked to stop, including when it exits on purpose to
// start an update. Installing sets this too, but a service registered
// another way, or whose settings were changed, would otherwise stay down
// after its first update, as one real PC did.
func ensureRecovery(name string) {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return
	}
	defer s.Close()
	err = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 60)
	if err == nil {
		err = s.SetRecoveryActionsOnNonCrashFailures(true)
	}
	if err != nil {
		slog.Warn("couldn't set the service to restart after a failure", "err", err)
	}
}
