//go:build !windows

package main

// browserNames are the names Chromium goes by on a PATH.
var browserNames = []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"}

// browserPaths returns where a Mac keeps the application, which is on
// no PATH.
func browserPaths() []string {
	return []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
}
