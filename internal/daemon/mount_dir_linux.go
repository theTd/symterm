//go:build linux

package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func ensureMountDirectoryReady(mountDir string) error {
	cleanMountDir := filepath.Clean(mountDir)
	mounted, err := isListedMountpoint(cleanMountDir)
	if err != nil {
		return err
	}
	if mounted {
		if err := detachMountpoint(cleanMountDir); err != nil {
			return err
		}
	}
	if err := removeNonDirectoryMountLeaf(cleanMountDir); err != nil {
		return err
	}
	return os.MkdirAll(cleanMountDir, 0o755)
}

func removeNonDirectoryMountLeaf(mountDir string) error {
	info, err := os.Lstat(mountDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return nil
	}
	return os.Remove(mountDir)
}

func detachMountpoint(mountDir string) error {
	var errs []error

	if err := runFuseUnmountHelper(mountDir); err == nil {
		return nil
	} else if err != nil {
		errs = append(errs, err)
	}

	if err := syscall.Unmount(mountDir, syscall.MNT_DETACH); err == nil || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOENT) {
		return nil
	} else {
		errs = append(errs, err)
	}

	return fmt.Errorf("detach mountpoint %q: %w", mountDir, errors.Join(errs...))
}

func runFuseUnmountHelper(mountDir string) error {
	var errs []error
	for _, name := range []string{"fusermount3", "fusermount"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		output, err := exec.Command(path, "-u", "-z", mountDir).CombinedOutput()
		if err == nil {
			return nil
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			errs = append(errs, fmt.Errorf("%s -u -z %s: %w", name, mountDir, err))
			continue
		}
		errs = append(errs, fmt.Errorf("%s -u -z %s: %w: %s", name, mountDir, err, message))
	}
	if len(errs) == 0 {
		return fmt.Errorf("no fusermount helper found for %q", mountDir)
	}
	return errors.Join(errs...)
}

func isListedMountpoint(target string) (bool, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		mountPoint, ok := parseMountInfoPoint(scanner.Text())
		if !ok {
			continue
		}
		if filepath.Clean(mountPoint) == target {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func isActiveMountpoint(target string) (bool, error) {
	return isListedMountpoint(target)
}

func parseMountInfoPoint(line string) (string, bool) {
	left, _, ok := strings.Cut(line, " - ")
	if !ok {
		return "", false
	}
	fields := strings.Fields(left)
	if len(fields) < 5 {
		return "", false
	}
	return unescapeMountInfoPath(fields[4]), true
}

func unescapeMountInfoPath(value string) string {
	replacer := strings.NewReplacer(
		`\\`, `\`,
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(value)
}
