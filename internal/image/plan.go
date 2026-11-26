package image

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/fzakaria/tabc/internal/guest"
	"github.com/fzakaria/tabc/internal/rootfs"
)

// What a container gets when neither the image nor the command line
// says: a directory, a PATH, a host name.
const (
	defaultCwd      = "/"
	defaultPath     = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	pathPrefix      = "PATH="
	defaultHostname = "tabc"
)

// The files written over the image, as an engine binds them: the guest's
// loopback by name, the guest's resolver, the host name.
const (
	hostsPath     = "/etc/hosts"
	hostsContent  = "127.0.0.1\tlocalhost\n"
	resolvPath    = "/etc/resolv.conf"
	resolvContent = "nameserver 10.0.2.3\n"
	hostnamePath  = "/etc/hostname"
)

// The image's user and group databases, the position of the name, the
// id and the primary group in a line, and the most of either file that
// is read.
const (
	passwdPath    = "/etc/passwd"
	groupPath     = "/etc/group"
	fieldName     = 0
	fieldID       = 2
	fieldGroup    = 3
	passwdMinimum = 4
	groupMinimum  = 3
	databaseLimit = 1 << 20
)

// idBits is the width of a user or group id.
const idBits = 32

// Overrides are what the command line changes about an image's own
// configuration.
type Overrides struct {
	// Command replaces the image's default arguments when not empty.
	// The image's entry point stays, as with every engine.
	Command []string
	// Entrypoint replaces the image's entry point when ReplaceEntrypoint
	// is set, an empty one included: that is how an entry point is
	// removed. Replacing it drops the image's default arguments too, as
	// with every engine.
	Entrypoint        []string
	ReplaceEntrypoint bool
	// Env is added to the image's environment, and wins.
	Env []string
	// Cwd and User replace the image's when not empty.
	Cwd  string
	User string
}

// Spec returns what the guest's init is told for a container of this
// image. tree is the image's file system, where a user given by name is
// looked up.
func Spec(config v1.Config, overrides Overrides, tree rootfs.Tree) (guest.Spec, error) {
	// The program: the entry point, then the command or the image's
	// default arguments.
	entrypoint, arguments := config.Entrypoint, config.Cmd
	if overrides.ReplaceEntrypoint {
		entrypoint, arguments = overrides.Entrypoint, nil
	}
	if len(overrides.Command) > 0 {
		arguments = overrides.Command
	}
	args := append(append([]string{}, entrypoint...), arguments...)
	if len(args) == 0 {
		return guest.Spec{}, errors.New("the image names no program to run, and none was given")
	}

	// The environment: the image's, a PATH if the image has none, then
	// the command line's.
	env := append([]string{}, config.Env...)
	if !hasPath(env) {
		env = append(env, defaultPath)
	}
	env = append(env, overrides.Env...)

	cwd := config.WorkingDir
	if overrides.Cwd != "" {
		cwd = overrides.Cwd
	}
	if cwd == "" {
		cwd = defaultCwd
	}

	user := config.User
	if overrides.User != "" {
		user = overrides.User
	}
	uid, gid, err := resolveUser(user, tree)
	if err != nil {
		return guest.Spec{}, err
	}

	return guest.Spec{
		Args:     args,
		Env:      env,
		Cwd:      cwd,
		UID:      uid,
		GID:      gid,
		Hostname: defaultHostname,
		Files: map[string]string{
			hostsPath:    hostsContent,
			resolvPath:   resolvContent,
			hostnamePath: defaultHostname + "\n",
		},
	}, nil
}

// hasPath reports whether an environment sets PATH.
func hasPath(env []string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, pathPrefix) {
			return true
		}
	}
	return false
}

// resolveUser turns an image's user, "name", "uid", "name:group" or
// "uid:gid", into numbers. A name is looked up in the image's own
// passwd and group files. No user is the superuser.
func resolveUser(user string, tree rootfs.Tree) (uid, gid uint32, err error) {
	if user == "" {
		return 0, 0, nil
	}
	userPart, groupPart, hasGroup := strings.Cut(user, ":")

	// The user, and the primary group the passwd file gives a user
	// found by name.
	number, parseErr := strconv.ParseUint(userPart, 10, idBits)
	if parseErr == nil {
		uid = uint32(number)
	} else {
		fields, found := findEntry(tree, passwdPath, userPart, passwdMinimum)
		if !found {
			return 0, 0, fmt.Errorf("the image has no user %q", userPart)
		}
		number, err := strconv.ParseUint(fields[fieldID], 10, idBits)
		if err != nil {
			return 0, 0, fmt.Errorf("user %q in %s: %w", userPart, passwdPath, err)
		}
		uid = uint32(number)
		if primary, err := strconv.ParseUint(fields[fieldGroup], 10, idBits); err == nil {
			gid = uint32(primary)
		}
	}
	if !hasGroup {
		return uid, gid, nil
	}

	// The group, when the user names one.
	number, parseErr = strconv.ParseUint(groupPart, 10, idBits)
	if parseErr == nil {
		return uid, uint32(number), nil
	}
	fields, found := findEntry(tree, groupPath, groupPart, groupMinimum)
	if !found {
		return 0, 0, fmt.Errorf("the image has no group %q", groupPart)
	}
	number, err = strconv.ParseUint(fields[fieldID], 10, idBits)
	if err != nil {
		return 0, 0, fmt.Errorf("group %q in %s: %w", groupPart, groupPath, err)
	}
	return uid, uint32(number), nil
}

// findEntry returns the fields of the line for name in one of the
// image's colon-separated databases.
func findEntry(tree rootfs.Tree, path, name string, minimumFields int) ([]string, bool) {
	content, err := tree.Read(path, 0, databaseLimit)
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= minimumFields && fields[fieldName] == name {
			return fields, true
		}
	}
	return nil, false
}
