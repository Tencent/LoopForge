package runtime

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func filesystemObjectIdentity(path string, _ os.FileInfo) (uint64, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("open workspace filesystem identity: %w", err)
	}
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, 0, fmt.Errorf("read workspace filesystem identity: %w", err)
	}
	return uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), nil
}
