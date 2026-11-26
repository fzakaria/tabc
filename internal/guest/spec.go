// Package guest holds what the host tells the guest about one container:
// the program to run and the file system to run it in. The supervisor
// writes a Spec as JSON, the page puts the file where the guest can read
// it, and tabc-init, the first program the guest runs for a container,
// acts on it.
package guest

// Spec is one container as tabc-init sets it up.
type Spec struct {
	// Args is the program and its arguments. Args[0] is looked up in the
	// PATH that Env names when it has no slash.
	Args []string `json:"args"`

	// Env is the program's environment, each entry "NAME=value".
	Env []string `json:"env"`

	// Cwd is the program's working directory inside the container.
	Cwd string `json:"cwd"`

	// UID, GID and AdditionalGids are who the program runs as.
	UID            uint32   `json:"uid"`
	GID            uint32   `json:"gid"`
	AdditionalGids []uint32 `json:"additionalGids,omitempty"`

	// Hostname is the container's host name, empty to keep the guest's.
	Hostname string `json:"hostname,omitempty"`

	// Files are written into the container's root before the program
	// starts, path to content: the files a container engine binds over
	// the image, such as /etc/hosts.
	Files map[string]string `json:"files,omitempty"`

	// Volumes are directories from outside the image, each already
	// mounted somewhere in the guest by the page and bound from there to
	// where the container expects it.
	Volumes []Volume `json:"volumes,omitempty"`
}

// Volume is one directory to bind into the container's root.
type Volume struct {
	// Source is where the page mounted the directory in the guest, and
	// Target the path in the container.
	Source string `json:"source"`
	Target string `json:"target"`
	// ReadOnly keeps the container from changing the directory.
	ReadOnly bool `json:"readOnly,omitempty"`
}
