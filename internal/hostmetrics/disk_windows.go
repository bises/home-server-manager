//go:build windows

package hostmetrics

import (
	"errors"
	"os"
)

func readDiskBytes(path string) (total, free, avail uint64, err error) {
	if _, err := os.Stat(path); err != nil {
		return 0, 0, 0, err
	}
	return 0, 0, 0, errors.New("disk statfs requires Linux host")
}
