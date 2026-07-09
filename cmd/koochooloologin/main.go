// Command koochooloologin manages, launches, and shares Chrome profiles, each
// carrying its own proxy, timezone, and browser fingerprint — a lightweight,
// GoLogin-style anti-detect browser manager driven over the DevTools Protocol.
package main

import "github.com/1995parham/koochooloologin/internal/cmd"

func main() {
	cmd.Execute()
}
