// Command remotly-agent makes this machine remotely controllable through a
// Remotly server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/maxhummler-oss/remotly/internal/agent"
	"github.com/maxhummler-oss/remotly/internal/agent/stream"
	"github.com/maxhummler-oss/remotly/internal/agent/winsvc"
)

var version = "dev"

const usage = `Remotly agent %s

Usage:
  remotly-agent enroll --server https://SERVER --key ENROLL-KEY [--ca-fingerprint HEX] [--name NAME]
  remotly-agent run    [--config FILE] [--log FILE]
  remotly-agent probe  [--config FILE]     test ffmpeg, encoders, capture and audio
  remotly-agent service install|uninstall  (Windows, as administrator)
  remotly-agent version

Config: %s
`

func main() {
	attachConsole()

	if winsvc.IsService() {
		runService()
		return
	}

	cmd := "run"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "enroll":
		err = cmdEnroll(args)
	case "run":
		err = cmdRun(args)
	case "probe":
		err = cmdProbe(args)
	case "service":
		err = cmdService(args)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, usage, version, agent.DefaultConfigPath())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	configPath := fs.String("config", agent.DefaultConfigPath(), "config file")
	serverURL := fs.String("server", "", "server URL, e.g. https://192.168.1.10")
	key := fs.String("key", "", "enrollment key (admin panel or 'remotly-server enroll-key')")
	caFP := fs.String("ca-fingerprint", "", "SHA-256 fingerprint of the server CA to pin")
	name := fs.String("name", "", "device name (default: hostname)")
	fs.Parse(args)
	if *serverURL == "" || *key == "" {
		return errors.New("--server und --key sind erforderlich")
	}

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cfg.Server = strings.TrimRight(*serverURL, "/")
	cfg.CAFingerprint = strings.ToLower(strings.ReplaceAll(*caFP, ":", ""))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, secret, err := agent.Enroll(ctx, cfg.Server, cfg.CAFingerprint, *key, *name)
	if err != nil {
		return fmt.Errorf("Registrierung fehlgeschlagen: %w", err)
	}
	cfg.DeviceID, cfg.Secret = id, secret
	if err := agent.SaveConfig(*configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("Gerät registriert (ID %s). Konfiguration: %s\n", id, *configPath)
	return nil
}

func openLog(path string) (io.Closer, error) {
	if path == "" {
		return io.NopCloser(nil), nil
	}
	// Keep the previous log when it gets large.
	if info, err := os.Stat(path); err == nil && info.Size() > 10<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	return f, nil
}

func loadValid(path string) (agent.Config, error) {
	cfg, err := agent.LoadConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("keine Konfiguration unter %s – zuerst 'remotly-agent enroll' ausführen", path)
		}
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", agent.DefaultConfigPath(), "config file")
	logFile := fs.String("log", "", "append log to this file")
	worker := fs.Bool("worker", false, "internal: started by the Windows service")
	fs.Parse(args)

	f, err := openLog(*logFile)
	if err != nil {
		return err
	}
	defer f.Close()
	log.Printf("Remotly agent %s (%s/%s)%s", version, runtime.GOOS, runtime.GOARCH, map[bool]string{true: " [service worker]"}[*worker])

	cfg, err := loadValid(*configPath)
	if err != nil {
		return err
	}
	a, err := agent.New(cfg, version, *worker)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); errors.Is(err, agent.ErrUpdated) {
		log.Printf("restarting into the updated agent")
		os.Exit(3) // non-zero: the supervisor restarts us with the new binary
	} else if err != nil {
		return err
	}
	return nil
}

func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	configPath := fs.String("config", agent.DefaultConfigPath(), "config file")
	fs.Parse(args)

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	opts := cfg.StreamOptions()
	fmt.Println("ffmpeg:", opts.FFmpeg)
	enc, err := stream.SelectEncoder(opts, stream.H264, func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) })
	if err != nil {
		return err
	}
	if vp8, err := stream.SelectEncoder(opts, stream.VP8, func(string, ...any) {}); err == nil {
		fmt.Println("  Fallback für Browser ohne H.264:", vp8)
	}
	fmt.Println("Aufnahme-Varianten (in dieser Reihenfolge versucht):")
	for _, v := range stream.Variants(opts, enc) {
		fmt.Printf("  %-18s %s %s\n", v.Name, opts.FFmpeg, strings.Join(stream.BuildArgs(enc, v, opts), " "))
	}
	if _, ok := stream.ResolveAudio(opts); ok {
		fmt.Println("Ton: verfügbar")
	} else {
		fmt.Println("Ton: nicht verfügbar")
	}
	return nil
}

func cmdService(args []string) error {
	if len(args) == 0 {
		return errors.New("service install|uninstall")
	}
	switch args[0] {
	case "install":
		return winsvc.Install()
	case "uninstall":
		return winsvc.Uninstall()
	}
	return fmt.Errorf("unbekannter Befehl %q", args[0])
}

// runService is the entry point when started by the Windows SCM.
func runService() {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	if f, err := openLog(filepath.Join(dir, "service.log")); err == nil {
		defer f.Close()
	}
	log.Printf("Remotly service %s starting", version)
	err := winsvc.Run([]string{"run", "--worker",
		"--config", filepath.Join(dir, "agent.json"),
		"--log", filepath.Join(dir, "agent.log")})
	if err != nil {
		log.Printf("service: %v", err)
	}
}
