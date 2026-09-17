//go:build windows

package keys

// keyPermWarning: Windows ACLs are not checked; %APPDATA% is per user.
func keyPermWarning(string) string { return "" }
