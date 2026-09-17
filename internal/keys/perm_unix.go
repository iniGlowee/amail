//go:build !windows

package keys

import (
	"fmt"
	"os"
)

// keyPermWarning reports a private key readable by group or others.
func keyPermWarning(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Sprintf("%s is readable by other users (mode %o); run: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	return ""
}
