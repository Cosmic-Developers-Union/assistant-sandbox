// Command assistant-init runs inside the guest root filesystem after the
// parent has created namespaces and chrooted. It finishes isolation before
// executing the requested guest program.
package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "assistant-init: %v\n", err)
		if code == 0 {
			code = 125
		}
	}
	os.Exit(code)
}

func run() (int, error) {
	rootfs, controlFD, directory, mounts, program, err := parseArguments(os.Args[1:])
	if err != nil {
		return 125, err
	}
	control := os.NewFile(uintptr(controlFD), "assistant-control")
	if control == nil {
		return 125, errors.New("invalid control descriptor")
	}
	fail := func(err error) (int, error) {
		_, _ = fmt.Fprintf(control, "ERROR %v\n", err)
		_ = control.Close()
		return 125, err
	}

	if err := isolateRoot(rootfs, mounts); err != nil {
		return fail(err)
	}
	if err := os.Chdir(directory); err != nil {
		return fail(fmt.Errorf("enter guest working directory: %w", err))
	}
	if err := unix.Sethostname([]byte("sandbox")); err != nil {
		return fail(fmt.Errorf("set guest hostname: %w", err))
	}
	if err := dropCapabilities(); err != nil {
		return fail(err)
	}
	if err := installSeccomp(); err != nil {
		return fail(err)
	}
	if _, err := fmt.Fprintf(control, "READY %d\n", os.Getpid()); err != nil {
		return fail(fmt.Errorf("signal readiness: %w", err))
	}
	if err := control.Close(); err != nil {
		return 125, fmt.Errorf("close control descriptor: %w", err)
	}
	if err := unix.Exec(program[0], program, minimalEnvironment()); err != nil {
		return 127, fmt.Errorf("exec guest program %s: %w", program[0], err)
	}
	return 0, nil
}

func parseArguments(arguments []string) (string, int, string, map[string]string, []string, error) {
	if len(arguments) < 8 || arguments[0] != "--rootfs" || arguments[2] != "--control-fd" ||
		arguments[4] != "--chdir" {
		return "", 0, "", nil, nil, errors.New("usage: assistant-init --rootfs PATH --control-fd N --chdir DIR [--mount GUEST=HOST ...] -- PROGRAM [ARG...]")
	}
	descriptor, err := strconv.Atoi(arguments[3])
	if err != nil || descriptor < 3 {
		return "", 0, "", nil, nil, errors.New("invalid control descriptor")
	}
	mounts := map[string]string{}
	position := 6
	for position+1 < len(arguments) && arguments[position] == "--mount" {
		guest, host, ok := strings.Cut(arguments[position+1], "=")
		if !ok || !filepath.IsAbs(guest) || !filepath.IsAbs(host) {
			return "", 0, "", nil, nil, errors.New("mount must use absolute GUEST=HOST paths")
		}
		mounts[filepath.Clean(guest)] = filepath.Clean(host)
		position += 2
	}
	if position >= len(arguments) || arguments[position] != "--" || position+1 >= len(arguments) {
		return "", 0, "", nil, nil, errors.New("missing guest program")
	}
	return arguments[1], descriptor, arguments[5], mounts, arguments[position+1:], nil
}

func isolateRoot(rootfs string, mounts map[string]string) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	// Bind before changing the process root; pivot_root requires the old
	// host root to remain the caller's current root.
	if err := unix.Mount(rootfs, rootfs, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind guest root: %w", err)
	}
	if err := applyMounts(rootfs, mounts); err != nil {
		return err
	}
	if err := unix.Mount("proc", path.Join(rootfs, "proc"), "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount guest proc: %w", err)
	}
	oldRoot := path.Join(rootfs, ".old-root")
	if err := os.Mkdir(oldRoot, 0o750); err != nil && !errors.Is(err, os.ErrExist) { //nolint:gosec // rootfs is the parent-controlled private VM directory
		return fmt.Errorf("create pivot directory: %w", err)
	}
	if err := unix.PivotRoot(rootfs, oldRoot); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir guest root: %w", err)
	}
	if err := unix.Unmount("/.old-root", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	if err := os.Remove("/.old-root"); err != nil {
		return fmt.Errorf("remove pivot directory: %w", err)
	}
	return nil
}

func applyMounts(rootfs string, mounts map[string]string) error {
	guestPaths := make([]string, 0, len(mounts))
	for guest := range mounts {
		guestPaths = append(guestPaths, guest)
	}
	slices.Sort(guestPaths)
	for _, guest := range guestPaths {
		target := filepath.Join(rootfs, filepath.Clean(guest))
		if !withinRoot(rootfs, target) {
			return fmt.Errorf("mount target escapes guest root: %s", guest)
		}
		if err := os.MkdirAll(target, 0o750); err != nil { //nolint:gosec // target is cleaned and checked to remain under the private rootfs
			return fmt.Errorf("create mount target %s: %w", guest, err)
		}
		if err := unix.Mount(mounts[guest], target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("bind mount %s: %w", guest, err)
		}
	}
	return nil
}

func withinRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, "../")
}

func dropCapabilities() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	for capability := 0; capability <= 63; capability++ {
		err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0)
		if err != nil && !errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("drop capability %d from bounding set: %w", capability, err)
		}
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	data := [2]unix.CapUserData{}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return fmt.Errorf("clear effective capabilities: %w", err)
	}
	return nil
}

func installSeccomp() error {
	denied := []int{
		unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT,
		unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_PTRACE,
		unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_KEYCTL,
		unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY, unix.SYS_USERFAULTFD,
		unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
		unix.SYS_REBOOT, unix.SYS_KEXEC_LOAD, unix.SYS_INIT_MODULE,
		unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE, unix.SYS_IOPL,
		unix.SYS_IOPERM, unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_ACCT,
		unix.SYS_SETTIMEOFDAY, unix.SYS_CLOCK_SETTIME, unix.SYS_ADJTIMEX,
		unix.SYS_SYSLOG, unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_NAME_TO_HANDLE_AT,
		unix.SYS_FANOTIFY_INIT, unix.SYS_QUOTACTL, unix.SYS_SECCOMP,
	}
	filters := make([]unix.SockFilter, 0, len(denied)+2)
	filters = append(filters, unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0})
	for _, syscall := range denied {
		filters = append(filters,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: uint32(syscall)}, //nolint:gosec // Linux syscall numbers are positive and bounded
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		)
	}
	filters = append(filters, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}                                                  //nolint:gosec // filter count is bounded by the fixed denylist
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil { //nolint:gosec // prctl requires the kernel ABI pointer to SockFprog
		return fmt.Errorf("install seccomp filter: %w", err)
	}
	return nil
}

func minimalEnvironment() []string {
	values := map[string]string{
		"PATH": "/usr/local/bin:/usr/bin:/bin",
		"HOME": "/root",
		"LANG": "C.UTF-8",
	}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	environment := make([]string, 0, len(values))
	for name, value := range values {
		environment = append(environment, name+"="+value)
	}
	return environment
}
