package runtime

import "os"

// gitIndexLockOwnerUID cannot prove Unix ownership on Windows; keep the lock.
func gitIndexLockOwnerUID(os.FileInfo) (uint32, bool) { return 0, false }
