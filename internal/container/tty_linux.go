//go:build linux

package container

import "syscall"

// On Linux the "get terminal attributes" ioctl is TCGETS. isInteractive uses it
// as a real-tty probe (it fails on /dev/null / pipes, unlike a char-device mode
// check).
const ioctlReadTermios = syscall.TCGETS
