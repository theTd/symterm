package portablefs

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var windowsReservedNames = map[string]struct{}{
	"CON":  {},
	"PRN":  {},
	"AUX":  {},
	"NUL":  {},
	"COM1": {},
	"COM2": {},
	"COM3": {},
	"COM4": {},
	"COM5": {},
	"COM6": {},
	"COM7": {},
	"COM8": {},
	"COM9": {},
	"LPT1": {},
	"LPT2": {},
	"LPT3": {},
	"LPT4": {},
	"LPT5": {},
	"LPT6": {},
	"LPT7": {},
	"LPT8": {},
	"LPT9": {},
}

func ValidateRelativePath(path string) error {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return fmt.Errorf("workspace path is required")
	}
	if strings.HasPrefix(clean, "/") {
		return fmt.Errorf("workspace path must stay within the shared workspace")
	}

	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("workspace path must stay within the shared workspace")
		}
		if err := validateComponent(part); err != nil {
			return err
		}
	}
	return nil
}

func CollisionKey(path string) string {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	if clean == "" {
		return ""
	}

	parts := strings.Split(clean, "/")
	for idx, part := range parts {
		parts[idx] = strings.ToLower(norm.NFD.String(part))
	}
	return strings.Join(parts, "/")
}

func validateComponent(component string) error {
	if !utf8.ValidString(component) {
		return fmt.Errorf("workspace path contains a filename that is not valid UTF-8")
	}
	if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return fmt.Errorf("workspace path contains a filename that is not portable across Windows and macOS")
	}
	for _, r := range component {
		switch {
		case r == 0:
			return fmt.Errorf("workspace path contains a filename that is not portable across Windows and macOS")
		case r < 0x20:
			return fmt.Errorf("workspace path contains a filename that is not portable across Windows and macOS")
		case strings.ContainsRune(`<>:"\|?*`, r):
			return fmt.Errorf("workspace path contains a filename that is not portable across Windows and macOS")
		}
	}

	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	if _, reserved := windowsReservedNames[strings.ToUpper(base)]; reserved {
		return fmt.Errorf("workspace path contains a Windows-reserved filename")
	}
	return nil
}
