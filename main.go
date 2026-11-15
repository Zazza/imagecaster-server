package main

import (
	"flag"
	"fmt"
	"imagecaster-server/attach"
	"imagecaster-server/config"
	"imagecaster-server/i18n"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/term"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "attach" {
		if err := attach.Main(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "attach:", err)
			os.Exit(1)
		}
		return
	}

	dataDir := flag.String("data", "", "data directory (default: ~/imagecaster-server)")
	configPath := flag.String("config", "", "config file path (default: {data-dir}/server-config.yaml)")
	port := flag.Int("port", 0, "override server port")
	headless := flag.Bool("headless", false, "run without TUI (log output only)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if desktopBuild && !*headless {
		dir := *dataDir
		if dir != "" {
			dir, _ = filepath.Abs(dir)
		}
		runDesktop(dir, *configPath, *port)
		return
	}

	dir := *dataDir
	if dir == "" {
		dir = config.DefaultDataDir()
	}
	dir, _ = filepath.Abs(dir)

	cfgFile := *configPath
	if cfgFile == "" {
		cfgFile = filepath.Join(dir, configName)
	}

	_, configErr := os.Stat(cfgFile)
	firstRun := configErr != nil

	isTerminal := term.IsTerminal(int(os.Stdin.Fd()))
	useTUI := !*headless && isTerminal

	if useTUI && firstRun {
		runTUI(&config.Config{DataDir: dir}, cfgFile, true, *port)
		return
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("Failed to create data directory %s: %v", dir, err)
	}

	cfg, err := config.LoadWithDir(cfgFile, dir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	i18n.Set(cfg.Lang)

	if *port != 0 {
		cfg.Port = *port
	}

	if useTUI {
		runTUI(cfg, cfgFile, false, *port)
	} else {
		runHeadless(cfg)
	}
}
