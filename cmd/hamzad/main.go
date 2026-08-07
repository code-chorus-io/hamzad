// Command hamzad manages, launches, and shares Chrome profiles, each
// carrying its own proxy, timezone, and browser fingerprint — a lightweight,
// GoLogin-style anti-detect browser manager driven over the DevTools Protocol.
//
// # Copyright (C) 2026 Parham Alvani
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU General Public License as published by the Free Software
// Foundation, either version 3 of the License, or (at your option) any later
// version. It is distributed WITHOUT ANY WARRANTY; see the GNU General Public
// License in LICENSE for details.
package main

import "github.com/code-chorus-io/hamzad/internal/cmd"

func main() {
	cmd.Execute()
}
