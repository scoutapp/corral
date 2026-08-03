//go:build darwin

package container

import "syscall"

// On darwin/BSD the "get terminal attributes" ioctl is TIOCGETA. isInteractive
// uses it as a real-tty probe (it fails on /dev/null / pipes, unlike a
// char-device mode check).
const ioctlReadTermios = syscall.TIOCGETA
