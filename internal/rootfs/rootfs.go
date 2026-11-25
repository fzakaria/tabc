// Package rootfs serves a container's root file system to the page over
// HTTP. A page cannot read the host's disk, and the guest's file system
// is a JavaScript object on the page answering the guest's lookups, so
// each lookup becomes a request here: a path's attributes, a
// directory's entries, a link's target, or a range of a file's bytes.
//
// The guest resolves symbolic links itself, one path component at a
// time, so every path that arrives names its target directly. Requests
// go through os.Root, which refuses any path that would leave the root
// file system, a link pointing outside it included.
package rootfs

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// The operations, as the last element of the request's path.
const (
	OpAttr = "attr"
	OpDir  = "dir"
	OpLink = "link"
	OpRead = "read"
)

// The request's parameters: the path in the container, and for a read
// the offset and the length.
const (
	paramPath   = "p"
	paramOffset = "o"
	paramLength = "n"
)

// The kinds of file the guest's file system interface knows. Devices,
// sockets and pipes have no kind there and are left out: a container
// image carries none that matter, and the guest makes its own /dev.
const (
	typeFile    = "file"
	typeDir     = "dir"
	typeSymlink = "symlink"
)

// The error names the page hands on to the guest as errno values.
const (
	codeNotFound = "ENOENT"
	codeAccess   = "EACCES"
	codeNotDir   = "ENOTDIR"
	codeIsDir    = "EISDIR"
	codeInvalid  = "EINVAL"
	codeIO       = "EIO"
)

// maxRead is the most bytes one read returns. The guest's 9p client
// asks for at most 128 KiB at a time, so this only bounds a caller that
// asks for more.
const maxRead = 1 << 20

// permissionBits are the mode bits the guest is shown: the permissions
// with set-user-id, set-group-id and sticky.
const permissionBits = 0o7777

// millisPerSecond and nanosPerMilli convert a file's time to the
// milliseconds the page's interface counts in.
const (
	millisPerSecond = 1000
	nanosPerMilli   = 1_000_000
)

// Attr is a path's attributes as the page's file system returns them.
type Attr struct {
	Type    string `json:"type"`
	Mode    uint32 `json:"mode"`
	Size    int64  `json:"size"`
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
	Nlink   uint64 `json:"nlink"`
	MtimeMs int64  `json:"mtimeMs"`
	Ino     uint64 `json:"ino"`
}

// Entry is one name in a directory.
type Entry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Ino  uint64 `json:"ino"`
}

// failure is the body of an error response.
type failure struct {
	Code string `json:"code"`
}

// Server answers the page's requests for one root file system.
type Server struct {
	root *os.Root
}

// Open returns a server for the directory at path.
func Open(path string) (*Server, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Server{root: root}, nil
}

// Close releases the directory.
func (s *Server) Close() error {
	return s.root.Close()
}

// Serve answers one operation, named by op.
func (s *Server) Serve(w http.ResponseWriter, r *http.Request, op string) {
	query := r.URL.Query()
	name := relative(query.Get(paramPath))

	switch op {
	case OpAttr:
		s.attr(w, name)
	case OpDir:
		s.dir(w, name)
	case OpLink:
		s.link(w, name)
	case OpRead:
		s.read(w, name, query.Get(paramOffset), query.Get(paramLength))
	default:
		fail(w, http.StatusNotFound, codeInvalid)
	}
}

// relative turns the absolute path the guest uses into the path os.Root
// takes, which is relative to the root: "/" is ".".
func relative(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return "."
	}
	return path
}

// attr answers a path's attributes.
func (s *Server) attr(w http.ResponseWriter, name string) {
	info, err := s.root.Lstat(name)
	if err != nil {
		failWith(w, err)
		return
	}
	attr, known := attrOf(info)
	if !known {
		fail(w, http.StatusNotFound, codeNotFound)
		return
	}
	reply(w, attr)
}

// dir answers a directory's entries.
func (s *Server) dir(w http.ResponseWriter, name string) {
	directory, err := s.root.Open(name)
	if err != nil {
		failWith(w, err)
		return
	}
	defer directory.Close()

	listed, err := directory.ReadDir(-1)
	if err != nil {
		failWith(w, err)
		return
	}

	// Each entry needs its inode number, which only its own attributes
	// carry. An entry that vanished meanwhile, or is of a kind the guest
	// cannot be shown, is left out.
	entries := make([]Entry, 0, len(listed))
	for _, item := range listed {
		info, err := item.Info()
		if err != nil {
			continue
		}
		attr, known := attrOf(info)
		if !known {
			continue
		}
		entries = append(entries, Entry{Name: item.Name(), Type: attr.Type, Ino: attr.Ino})
	}
	reply(w, entries)
}

// link answers a symbolic link's target.
func (s *Server) link(w http.ResponseWriter, name string) {
	target, err := s.root.Readlink(name)
	if err != nil {
		failWith(w, err)
		return
	}
	reply(w, map[string]string{"target": target})
}

// read answers a range of a regular file's bytes. A read past the end
// returns fewer bytes than asked, or none.
func (s *Server) read(w http.ResponseWriter, name, offsetText, lengthText string) {
	offset, err := strconv.ParseInt(offsetText, 10, 64)
	if err != nil || offset < 0 {
		fail(w, http.StatusBadRequest, codeInvalid)
		return
	}
	length, err := strconv.ParseInt(lengthText, 10, 64)
	if err != nil || length < 0 {
		fail(w, http.StatusBadRequest, codeInvalid)
		return
	}
	length = min(length, maxRead)

	// O_NOFOLLOW: the guest opens only what its own lookup found to be a
	// regular file, so a link here is a file replaced under the guest.
	file, err := s.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		failWith(w, err)
		return
	}
	defer file.Close()

	buffer := make([]byte, length)
	count, err := file.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		failWith(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(buffer[:count])
}

// attrOf returns the attributes of a file from its lstat result, and
// false for a kind of file the guest cannot be shown.
func attrOf(info fs.FileInfo) (Attr, bool) {
	var kind string
	switch {
	case info.Mode().IsRegular():
		kind = typeFile
	case info.IsDir():
		kind = typeDir
	case info.Mode()&fs.ModeSymlink != 0:
		kind = typeSymlink
	default:
		return Attr{}, false
	}

	attr := Attr{
		Type:    kind,
		Size:    info.Size(),
		MtimeMs: info.ModTime().UnixNano() / nanosPerMilli,
	}

	// The owner, link count, inode and the mode's special bits are in
	// the raw stat result only.
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		attr.Mode = stat.Mode & permissionBits
		attr.UID = stat.Uid
		attr.GID = stat.Gid
		attr.Nlink = uint64(stat.Nlink)
		attr.Ino = stat.Ino
		attr.MtimeMs = stat.Mtim.Sec*millisPerSecond + stat.Mtim.Nsec/nanosPerMilli
	}
	return attr, true
}

// reply writes a JSON answer.
func reply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}

// fail writes an error answer with the errno name the guest is given.
func fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(failure{Code: code})
}

// failWith writes the error answer for a file system error. A path that
// would leave the root reads as not found: to the guest nothing is
// there.
func failWith(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fail(w, http.StatusNotFound, codeNotFound)
	case errors.Is(err, fs.ErrPermission):
		fail(w, http.StatusForbidden, codeAccess)
	case errors.Is(err, syscall.ENOTDIR):
		fail(w, http.StatusBadRequest, codeNotDir)
	case errors.Is(err, syscall.EISDIR):
		fail(w, http.StatusBadRequest, codeIsDir)
	case errors.Is(err, syscall.EINVAL):
		fail(w, http.StatusBadRequest, codeInvalid)
	case errors.Is(err, syscall.ELOOP):
		fail(w, http.StatusNotFound, codeNotFound)
	default:
		// os.Root reports an escape with an error of its own that names
		// no errno.
		if strings.Contains(err.Error(), "escapes") {
			fail(w, http.StatusNotFound, codeNotFound)
			return
		}
		fail(w, http.StatusInternalServerError, codeIO)
	}
}
