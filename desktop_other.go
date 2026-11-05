//go:build !windows

package main

import "log"

func runDesktop(dataDir, cfgFile string, port int) {
	log.Fatal("the desktop build is Windows-only")
}
