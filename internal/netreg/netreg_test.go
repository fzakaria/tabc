package netreg

import (
	"testing"
)

// TestRegisterAndFind checks a network of two containers: each gets an
// address of its own, a name finds its container, an address finds the
// container too, and a name on a network the asker is not on finds
// nothing. The registry is a temporary directory.
func TestRegisterAndFind(t *testing.T) {
	root := t.TempDir()

	webFiles, webIP, err := Register(root, "app", "c-web", "ws://web", []string{"web", "frontend"})
	if err != nil {
		t.Fatal(err)
	}
	_, redisIP, err := Register(root, "app", "c-redis", "ws://redis", []string{"redis"})
	if err != nil {
		t.Fatal(err)
	}
	if webIP == redisIP || webIP == "" {
		t.Fatalf("addresses %q and %q", webIP, redisIP)
	}
	if len(webFiles) != 2 {
		t.Errorf("two names wrote %d files", len(webFiles))
	}

	// By each name, and by address.
	for _, name := range []string{"web", "frontend"} {
		entry, ok := Lookup(root, []string{"app"}, name)
		if !ok || entry.IP != webIP || entry.Relay != "ws://web" || entry.Container != "c-web" {
			t.Errorf("Lookup(%q) = %+v, %v", name, entry, ok)
		}
	}
	entry, ok := ByAddress(root, []string{"app"}, redisIP)
	if !ok || entry.Name != "redis" {
		t.Errorf("ByAddress(%q) = %+v, %v", redisIP, entry, ok)
	}

	// Not from another network, not an unknown name, not a real address.
	if _, ok := Lookup(root, []string{"other"}, "web"); ok {
		t.Error("a name was found from a network it is not on")
	}
	if _, ok := Lookup(root, []string{"app"}, "nobody"); ok {
		t.Error("an unknown name was found")
	}
	if _, ok := Lookup(root, []string{"app"}, "../app/web"); ok {
		t.Error("a name with a path in it was found")
	}
	if _, ok := ByAddress(root, []string{"app"}, "93.184.216.34"); ok {
		t.Error("an address outside the registry's range was found")
	}
}

// TestAddressesAreKeptAndReused checks that a container registering
// again keeps its address, and that an address is free again once its
// container has left.
func TestAddressesAreKeptAndReused(t *testing.T) {
	root := t.TempDir()

	files, first, err := Register(root, "app", "c1", "ws://1", []string{"one"})
	if err != nil {
		t.Fatal(err)
	}
	_, again, err := Register(root, "app", "c1", "ws://1", []string{"uno"})
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("a second name moved the container from %q to %q", first, again)
	}

	_, second, _ := Register(root, "app", "c2", "ws://2", []string{"two"})
	if second == first {
		t.Fatalf("two containers share %q", first)
	}

	// c1 leaves under both names; its address goes to the next arrival.
	Unregister(files)
	Unregister([]string{Dir(root, "app") + "/uno" + entrySuffix})
	_, third, _ := Register(root, "app", "c3", "ws://3", []string{"three"})
	if third != first {
		t.Errorf("the freed address %q was not reused: got %q", first, third)
	}
	if _, ok := Lookup(root, []string{"app"}, "one"); ok {
		t.Error("a name that left is still found")
	}
}

// TestInvalidNames checks that a network whose name would escape the
// registry is refused, and that a container name that would is skipped.
func TestInvalidNames(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Register(root, "../x", "c", "ws://c", []string{"a"}); err == nil {
		t.Error("a network name with a path in it was accepted")
	}
	files, _, err := Register(root, "app", "c", "ws://c", []string{"ok", "../bad", ".hidden", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("wrote %d files, want only the valid name's", len(files))
	}
}
