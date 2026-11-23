//go:build linux

package main

// Linux has no inode-to-path lookup for unprivileged users, so a moved folder
// is found by scanning near its old location (see scanResolve).
const scanFallback = true

func platformResolve(e *Entry) string { return "" }
