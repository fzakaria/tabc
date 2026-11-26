// Package users turns a user as a person writes one into the numbers a
// program runs as. Names are looked up in a passwd and a group file,
// which the caller reads: an image's, before a container starts, or the
// running container's own, for a second program in the container.
package users

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The user and group databases, the position of the name, the number
// and the primary group in a line, and the fewest fields a line of each
// may have.
const (
	PasswdPath    = "/etc/passwd"
	GroupPath     = "/etc/group"
	fieldName     = 0
	fieldID       = 2
	fieldGroup    = 3
	passwdMinimum = 4
	groupMinimum  = 3
)

// idBits is the width of a user or group id.
const idBits = 32

// ErrUnknown is a user or a group that neither is a number nor has a
// line in its database.
var ErrUnknown = errors.New("unknown user or group")

// Resolve turns "name", "uid", "name:group" or "uid:gid" into numbers.
// A name is looked up in the files read hands over, PasswdPath and
// GroupPath, and a user found by name brings the primary group of the
// passwd file's line. No user is the superuser.
func Resolve(user string, read func(path string) ([]byte, error)) (uid, gid uint32, err error) {
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
		fields, found := findEntry(read, PasswdPath, userPart, passwdMinimum)
		if !found {
			return 0, 0, fmt.Errorf("%w: no user %q in %s", ErrUnknown, userPart, PasswdPath)
		}
		number, err := strconv.ParseUint(fields[fieldID], 10, idBits)
		if err != nil {
			return 0, 0, fmt.Errorf("user %q in %s: %w", userPart, PasswdPath, err)
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
	fields, found := findEntry(read, GroupPath, groupPart, groupMinimum)
	if !found {
		return 0, 0, fmt.Errorf("%w: no group %q in %s", ErrUnknown, groupPart, GroupPath)
	}
	number, err = strconv.ParseUint(fields[fieldID], 10, idBits)
	if err != nil {
		return 0, 0, fmt.Errorf("group %q in %s: %w", groupPart, GroupPath, err)
	}
	return uid, uint32(number), nil
}

// findEntry returns the fields of the line for name in one of the
// colon-separated databases. A database that cannot be read has no
// line for anyone.
func findEntry(read func(path string) ([]byte, error), path, name string, minimumFields int) ([]string, bool) {
	content, err := read(path)
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
