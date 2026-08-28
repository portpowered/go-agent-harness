//go:build !windows

package testtimeout

import (
	"os"
	"syscall"
)

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if _, err := os.Stat("/proc/" + itoa(pid) + "/stat"); err == nil {
		data, readErr := os.ReadFile("/proc/" + itoa(pid) + "/stat")
		if readErr == nil && isZombie(data) {
			return false
		}
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func isZombie(stat []byte) bool {
	lastClose := -1
	for index, value := range stat {
		if value == ')' {
			lastClose = index
		}
	}
	return lastClose >= 0 && lastClose+2 < len(stat) && stat[lastClose+2] == 'Z'
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}
