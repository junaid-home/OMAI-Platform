//go:build linux

package process

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ownedListenerPorts maps listening socket inodes back to the complete process
// group. npm/pnpm may spawn the actual dev server, so inspecting only the root
// PID would incorrectly miss the listener.
func ownedListenerPorts(rootPID int) ([]uint32, error) {
	pids, err := processGroupPIDs(rootPID)
	if err != nil {
		return nil, err
	}
	inodes := map[string]struct{}{}
	for _, pid := range pids {
		entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, entry.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if inode != "" {
				inodes[inode] = struct{}{}
			}
		}
	}
	if len(inodes) == 0 {
		return nil, nil
	}
	seen := map[uint32]struct{}{}
	var result []uint32
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		ports, _ := portsFromProcNet(table, inodes)
		for _, portNumber := range ports {
			if _, exists := seen[portNumber]; exists {
				continue
			}
			seen[portNumber] = struct{}{}
			result = append(result, portNumber)
		}
	}
	return result, nil
}

func processGroupPIDs(rootPID int) ([]int, error) {
	if rootPID <= 0 {
		return nil, fmt.Errorf("invalid preview process id")
	}
	visibleRootPID, err := visibleProcPID(rootPID)
	if err != nil {
		return nil, err
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", visibleRootPID))
	if err != nil {
		return nil, err
	}
	group, err := procGroup(stat)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var result []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		candidateGroup, err := procGroup(stat)
		if err == nil && candidateGroup == group {
			result = append(result, pid)
		}
	}
	return result, nil
}

// visibleProcPID maps a PID returned inside the current namespace to the
// numeric /proc directory. They normally match; nested CI/microVM launchers
// may expose an ancestor procfs, where NSpid is required for the translation.
func visibleProcPID(namespacePID int) (int, error) {
	if namespacePID <= 0 {
		return 0, fmt.Errorf("invalid process id")
	}
	if selfTarget, err := os.Readlink("/proc/self"); err == nil {
		visibleSelf, parseErr := strconv.Atoi(filepath.Base(selfTarget))
		if parseErr == nil && visibleSelf == os.Getpid() {
			if _, statErr := os.Stat(fmt.Sprintf("/proc/%d/stat", namespacePID)); statErr == nil {
				return namespacePID, nil
			}
		}
	}
	currentNamespace, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		visiblePID, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil {
			continue
		}
		status, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if readErr != nil || !statusMapsNamespacePID(status, namespacePID) {
			continue
		}
		processNamespace, linkErr := os.Readlink(filepath.Join("/proc", entry.Name(), "ns", "pid"))
		if linkErr == nil && processNamespace == currentNamespace {
			return visiblePID, nil
		}
	}
	return 0, os.ErrNotExist
}

func statusMapsNamespacePID(status []byte, namespacePID int) bool {
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "NSpid:"))
		if len(fields) == 0 {
			return false
		}
		value, err := strconv.Atoi(fields[len(fields)-1])
		return err == nil && value == namespacePID
	}
	return false
}

func procGroup(stat []byte) (int, error) {
	text := string(stat)
	end := strings.LastIndex(text, ")")
	if end < 0 || end+2 >= len(text) {
		return 0, fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 3 {
		return 0, fmt.Errorf("invalid proc stat fields")
	}
	return strconv.Atoi(fields[2])
}

func portsFromProcNet(path string, inodes map[string]struct{}) ([]uint32, error) {
	// #nosec G304 -- path is selected internally from the fixed /proc/net/tcp
	// and /proc/net/tcp6 constants; it is never derived from request data.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	first := true
	var result []uint32
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		if _, owned := inodes[fields[9]]; !owned {
			continue
		}
		_, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		portNumber, err := strconv.ParseUint(portHex, 16, 16)
		if err == nil && portNumber > 0 {
			result = append(result, uint32(portNumber))
		}
	}
	return result, scanner.Err()
}

func fallbackPreviewPort([]uint32) uint32 { return 0 }
