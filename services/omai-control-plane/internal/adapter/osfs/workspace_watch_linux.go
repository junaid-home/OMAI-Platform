//go:build linux

package osfs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/omai/backend/internal/domain"
)

type linuxWatchOutput struct {
	prefix string
	self   string
	all    bool
	names  map[string]string
}

func watchWorkspace(ctx context.Context, _ string, targets []workspaceWatchTarget) (<-chan domain.FileChange, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	watches := make(map[int][]linuxWatchOutput)
	watchDescriptors := make(map[string]int)
	addDirectory := func(directory string, output linuxWatchOutput) error {
		wd, ok := watchDescriptors[directory]
		if !ok {
			wd, err = syscall.InotifyAddWatch(fd, directory,
				syscall.IN_ATTRIB|syscall.IN_CLOSE_WRITE|syscall.IN_CREATE|syscall.IN_DELETE|
					syscall.IN_DELETE_SELF|syscall.IN_MODIFY|syscall.IN_MOVE_SELF|syscall.IN_MOVED_FROM|syscall.IN_MOVED_TO,
			)
			if err != nil {
				return err
			}
			watchDescriptors[directory] = wd
		}
		watches[wd] = append(watches[wd], output)
		return nil
	}
	for _, target := range targets {
		if target.directory {
			if err := addDirectory(target.absolute, linuxWatchOutput{prefix: target.relative, self: target.relative, all: true}); err != nil {
				_ = syscall.Close(fd)
				return nil, classifyPathError(err)
			}
			continue
		}
		if err := addDirectory(filepath.Dir(target.absolute), linuxWatchOutput{
			names: map[string]string{filepath.Base(target.absolute): target.relative},
		}); err != nil {
			_ = syscall.Close(fd)
			return nil, classifyPathError(err)
		}
	}

	updates := make(chan domain.FileChange, 512)
	go runInotifyWatch(ctx, fd, watches, updates)
	return updates, nil
}

func runInotifyWatch(ctx context.Context, fd int, watches map[int][]linuxWatchOutput, updates chan<- domain.FileChange) {
	defer close(updates)
	defer syscall.Close(fd)

	buffer := make([]byte, 64*1024)
	var sequence uint64
	pendingResync := false
	emit := func(path string, kind domain.FileChangeKind) {
		if path == "" && kind != domain.FileChangeResync {
			kind = domain.FileChangeResync
		}
		if pendingResync {
			select {
			case updates <- domain.FileChange{Sequence: sequence + 1, Kind: domain.FileChangeResync}:
				sequence++
				pendingResync = false
			default:
				return
			}
		}
		select {
		case updates <- domain.FileChange{Sequence: sequence + 1, Path: path, Kind: kind}:
			sequence++
		default:
			pendingResync = true
		}
	}

	// The first resync is a readiness handshake. A remote caller must not mutate
	// the workspace until it knows every requested inotify watch is installed.
	emit("", domain.FileChangeResync)

	for {
		if err := waitInotify(ctx, fd); err != nil {
			return
		}
		count, err := syscall.Read(fd, buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, syscall.EBADF) || errors.Is(err, syscall.EINVAL) {
				return
			}
			if errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			emit("", domain.FileChangeResync)
			return
		}
		for offset := 0; offset+syscall.SizeofInotifyEvent <= count; {
			// #nosec G103 -- Read returned at least one complete inotify header;
			// offsets advance by the kernel ABI header plus its aligned name field.
			event := (*syscall.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
			offset += syscall.SizeofInotifyEvent + int(event.Len)
			if event.Mask&syscall.IN_Q_OVERFLOW != 0 {
				emit("", domain.FileChangeResync)
				continue
			}
			kind, ok := inotifyChangeKind(event.Mask)
			if !ok {
				continue
			}
			name := ""
			if event.Len > 0 {
				start := offset - int(event.Len)
				name = strings.TrimRight(string(buffer[start:offset]), "\x00")
			}
			outputs := watches[int(event.Wd)]
			for _, output := range outputs {
				path := output.self
				if name != "" {
					if output.all {
						path = filepath.ToSlash(filepath.Join(output.prefix, name))
					} else {
						path = output.names[name]
					}
				}
				if path == "" && name != "" {
					continue
				}
				if ignoredWorkspaceWatchPath(path) {
					continue
				}
				emit(path, kind)
			}
		}
	}
}

func waitInotify(ctx context.Context, fd int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var readable syscall.FdSet
		bits := int(unsafe.Sizeof(readable.Bits[0]) * 8)
		index := fd / bits
		if fd < 0 || index >= len(readable.Bits) {
			return syscall.EINVAL
		}
		readable.Bits[index] |= 1 << (uint(fd) % uint(bits))
		timeout := syscall.Timeval{Usec: int64((250 * time.Millisecond) / time.Microsecond)}
		ready, err := syscall.Select(fd+1, &readable, nil, nil, &timeout)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return err
		}
		if ready > 0 {
			return nil
		}
	}
}

func inotifyChangeKind(mask uint32) (domain.FileChangeKind, bool) {
	switch {
	case mask&(syscall.IN_DELETE|syscall.IN_MOVED_FROM|syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF) != 0:
		return domain.FileChangeUnlink, true
	case mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0:
		return domain.FileChangeAdd, true
	case mask&(syscall.IN_MODIFY|syscall.IN_CLOSE_WRITE|syscall.IN_ATTRIB) != 0:
		return domain.FileChangeChange, true
	default:
		return "", false
	}
}
