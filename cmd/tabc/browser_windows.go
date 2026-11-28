package main

import (
	"os"
	"path/filepath"
)

// browserNames are the names a Chromium goes by on a PATH. Windows
// rarely has a browser on one.
var browserNames = []string{"chrome", "chromium", "msedge"}

// installRoots are the variables that name the directories Windows
// installs programs under: for every user, for every user as a 32-bit
// program, and for one user.
var installRoots = []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"}

// browserPrograms are where a Chromium is under an install root, in
// the order tried: Chrome, Chromium, and Edge, which is a Chromium
// that every Windows comes with.
var browserPrograms = []string{
	`Google\Chrome\Application\chrome.exe`,
	`Chromium\Application\chrome.exe`,
	`Microsoft\Edge\Application\msedge.exe`,
}

// browserPaths returns where Windows keeps a browser, which is on no
// PATH: each program under each install root.
func browserPaths() []string {
	var paths []string
	for _, program := range browserPrograms {
		for _, variable := range installRoots {
			root := os.Getenv(variable)
			if root == "" {
				continue
			}
			paths = append(paths, filepath.Join(root, program))
		}
	}
	return paths
}
