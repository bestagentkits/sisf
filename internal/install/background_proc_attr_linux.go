package install

import "syscall"

// backgroundProcAttr starts a background program as the leader of a process
// group of its own, so it and the helpers it starts are signalled together,
// and asks Linux to send it SIGTERM when `sisf up` dies, so a killed
// `sisf up` leaves no unit behind. Every unit is a child of the foreground
// `sisf up` and is meant to live only as long as it does: a service manager
// runs `sisf up` itself, never a unit apart from it. Linux sends the signal
// when the thread that started the program ends, which a Go program's
// threads do not unless a goroutine locked to one exits; the unit then
// stops and the process group starts it again, as after any crash. A unit
// that outlives `sisf up` regardless still holds the run lock and is
// recorded, so `sisf stop` finds it.
func backgroundProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
