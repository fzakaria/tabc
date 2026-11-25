// tabc-init is the first program the guest runs for a container. The
// page starts it with the path of a guest.Spec. It builds the
// container's root from the image, which the page serves read-only,
// under a writable layer in memory, enters that root, becomes the
// container's user and replaces itself with the container's program,
// so the program's exit status is this process's.
//
// The guest is a whole Linux machine that holds one container, so
// nothing here makes namespaces or drops capabilities: the machine's
// edge, the browser tab, is the container's edge.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/fzakaria/tabc/internal/guest"
)

// Exit statuses for a container whose program never ran, the ones
// shells and container engines use.
const (
	exitSetupFailed = 125
	exitCannotRun   = 126
	exitNotFound    = 127
)

// Where the page mounts the image, and where the writable layer and the
// merged root go: a tmpfs, since an overlay's upper layer cannot be on
// the file system the page serves.
const (
	lowerDir   = "/tabc/lower"
	scratchDir = "/tabc/rw"
	upperName  = "upper"
	workName   = "work"
	mergedName = "merged"
)

// The file systems mounted into the root, and the guest's /dev, which
// is bound in whole: the program's terminal is a pty on the guest's
// devpts, and the program should find that terminal under its own /dev.
const (
	fsTmpfs   = "tmpfs"
	fsOverlay = "overlay"
	fsProc    = "proc"
	fsSysfs   = "sysfs"
	procDir   = "/proc"
	sysDir    = "/sys"
	devDir    = "/dev"
)

// The permissions of directories and files init makes.
const (
	dirMode  = 0o755
	fileMode = 0o644
)

// The environment variables init reads or sets, the PATH a program gets
// when its environment names none, and the home of a user the image's
// passwd file does not list.
const (
	envPath     = "PATH"
	envHome     = "HOME"
	defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	defaultHome = "/"
)

// passwdFile is where a user's home is looked up, and the positions of
// the uid and the home in one of its lines.
const (
	passwdFile       = "/etc/passwd"
	passwdFields     = 7
	passwdUIDField   = 2
	passwdHomeField  = 5
	executableBits   = 0o111
	pathListSeparate = ":"
)

func main() {
	if len(os.Args) != 2 {
		fail(exitSetupFailed, errors.New("usage: tabc-init <spec.json>"))
	}

	spec, err := readSpec(os.Args[1])
	if err != nil {
		fail(exitSetupFailed, err)
	}

	root, err := buildRoot()
	if err != nil {
		fail(exitSetupFailed, err)
	}
	if err := writeFiles(root, spec.Files); err != nil {
		fail(exitSetupFailed, err)
	}
	if err := enter(root, spec); err != nil {
		fail(exitSetupFailed, err)
	}

	// From here on every path is the container's.
	env := withHome(spec.Env, spec.UID)
	if err := becomeUser(spec); err != nil {
		fail(exitSetupFailed, err)
	}

	program, err := lookPath(spec.Args[0], env)
	if err != nil {
		fail(exitNotFound, err)
	}
	err = syscall.Exec(program, spec.Args, env)
	fail(exitCannotRun, fmt.Errorf("exec %s: %w", program, err))
}

// fail reports why the container's program did not run and exits.
func fail(status int, err error) {
	fmt.Fprintf(os.Stderr, "tabc-init: %v\n", err)
	os.Exit(status)
}

// readSpec reads the container's description.
func readSpec(path string) (guest.Spec, error) {
	var spec guest.Spec
	data, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return spec, fmt.Errorf("%s: %w", path, err)
	}
	if len(spec.Args) == 0 {
		return spec, errors.New("the spec names no program")
	}
	return spec, nil
}

// buildRoot mounts the container's root, the image under a writable
// layer in memory, and the kernel's file systems inside it. It returns
// the root's path.
func buildRoot() (string, error) {
	upper := filepath.Join(scratchDir, upperName)
	work := filepath.Join(scratchDir, workName)
	merged := filepath.Join(scratchDir, mergedName)

	// The writable layer's home.
	if err := os.MkdirAll(scratchDir, dirMode); err != nil {
		return "", err
	}
	if err := syscall.Mount(fsTmpfs, scratchDir, fsTmpfs, 0, ""); err != nil {
		return "", fmt.Errorf("mounting a tmpfs at %s: %w", scratchDir, err)
	}
	for _, dir := range []string{upper, work, merged} {
		if err := os.Mkdir(dir, dirMode); err != nil {
			return "", err
		}
	}

	// The image below, the writable layer above.
	options := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerDir, upper, work)
	if err := syscall.Mount(fsOverlay, merged, fsOverlay, 0, options); err != nil {
		return "", fmt.Errorf("mounting the overlay root: %w", err)
	}

	// What a program expects of any Linux root.
	mounts := []struct {
		source, target, fstype string
		flags                  uintptr
	}{
		{fsProc, procDir, fsProc, 0},
		{fsSysfs, sysDir, fsSysfs, syscall.MS_RDONLY},
		{devDir, devDir, "", syscall.MS_BIND | syscall.MS_REC},
	}
	for _, mount := range mounts {
		target := filepath.Join(merged, mount.target)
		if err := os.MkdirAll(target, dirMode); err != nil {
			return "", err
		}
		if err := syscall.Mount(mount.source, target, mount.fstype, mount.flags, ""); err != nil {
			return "", fmt.Errorf("mounting %s: %w", mount.target, err)
		}
	}

	return merged, nil
}

// writeFiles writes the files the engine bound over the image into the
// root's writable layer. A file whose directory the image lacks gets
// the directory too.
func writeFiles(root string, files map[string]string) error {
	for path, content := range files {
		target := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(target), dirMode); err != nil {
			return err
		}

		// An image may ship the path as a link, /etc/resolv.conf to a
		// file under /run for one. Writing through the link would land
		// outside the root, so the link is replaced.
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.WriteFile(target, []byte(content), fileMode); err != nil {
			return err
		}
	}
	return nil
}

// enter makes root the process's root, names the machine, and moves to
// the program's directory, which is made if the image has none, as runc
// does.
func enter(root string, spec guest.Spec) error {
	if spec.Hostname != "" {
		if err := syscall.Sethostname([]byte(spec.Hostname)); err != nil {
			return fmt.Errorf("setting the host name: %w", err)
		}
	}

	if err := syscall.Chroot(root); err != nil {
		return fmt.Errorf("entering the root: %w", err)
	}
	if err := os.MkdirAll(spec.Cwd, dirMode); err != nil {
		return err
	}
	if err := os.Chdir(spec.Cwd); err != nil {
		return err
	}
	return nil
}

// becomeUser drops to the container's user: groups first, since
// changing them needs the superuser the last step gives up.
func becomeUser(spec guest.Spec) error {
	groups := make([]int, 0, len(spec.AdditionalGids))
	for _, gid := range spec.AdditionalGids {
		groups = append(groups, int(gid))
	}
	if err := syscall.Setgroups(groups); err != nil {
		return fmt.Errorf("setting groups: %w", err)
	}
	if err := syscall.Setgid(int(spec.GID)); err != nil {
		return fmt.Errorf("setting gid %d: %w", spec.GID, err)
	}
	if err := syscall.Setuid(int(spec.UID)); err != nil {
		return fmt.Errorf("setting uid %d: %w", spec.UID, err)
	}
	return nil
}

// withHome returns env with HOME set, if env has none, to the home the
// root's passwd file gives the user. Engines leave HOME for the runtime
// to fill in.
func withHome(env []string, uid uint32) []string {
	if _, has := lookupEnv(env, envHome); has {
		return env
	}
	return append(env, envHome+"="+homeOf(uid))
}

// homeOf returns a user's home directory from the passwd file, or the
// root directory for a user the file does not list.
func homeOf(uid uint32) string {
	file, err := os.Open(passwdFile)
	if err != nil {
		return defaultHome
	}
	defer file.Close()

	want := strconv.FormatUint(uint64(uid), 10)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != passwdFields || fields[passwdUIDField] != want {
			continue
		}
		if fields[passwdHomeField] != "" {
			return fields[passwdHomeField]
		}
	}
	return defaultHome
}

// lookupEnv returns a variable's value from an environment list.
func lookupEnv(env []string, name string) (string, bool) {
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if found && key == name {
			return value, true
		}
	}
	return "", false
}

// lookPath finds the program: a name with a slash is the path itself,
// and any other name is searched for in the environment's PATH.
func lookPath(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		if !executable(name) {
			return "", fmt.Errorf("%s: not found or not executable", name)
		}
		return name, nil
	}

	path, has := lookupEnv(env, envPath)
	if !has {
		path = defaultPath
	}
	for _, dir := range strings.Split(path, pathListSeparate) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if executable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: executable file not found in $PATH", name)
}

// executable reports whether path is a regular file someone may run.
func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode()&executableBits != 0
}
