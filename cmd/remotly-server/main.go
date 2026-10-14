// Command remotly-server is the hub every agent and viewer connects to.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/maxhummler-oss/remotly/internal/server"
	"golang.org/x/term"
)

var version = "dev"

const usage = `Remotly server %s

Usage:
  remotly-server [run] [-config FILE]           start the server
  remotly-server user add NAME [--admin] [--display "Vor Nachname"]
  remotly-server user passwd NAME
  remotly-server user del NAME
  remotly-server user list
  remotly-server enroll-key [--rotate]          show (or rotate) the agent enrollment key
  remotly-server version

Global flag: -config FILE (default /etc/remotly/server.json)
`

func main() {
	fs := flag.NewFlagSet("remotly-server", flag.ExitOnError)
	configPath := fs.String("config", "/etc/remotly/server.json", "config file (created with defaults if missing)")
	fs.Usage = func() { fmt.Fprintf(os.Stderr, usage, version) }

	// Allow the flag before or after the subcommand.
	args := os.Args[1:]
	fs.Parse(args)
	rest := fs.Args()
	cmd := "run"
	if len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}
	if cmd == "version" {
		fmt.Println(version)
		return
	}

	cfg, err := server.LoadConfig(*configPath, true)
	if err != nil {
		log.Fatal(err)
	}
	store, err := server.OpenStore(cfg.StatePath())
	if err != nil {
		log.Fatal(err)
	}

	switch cmd {
	case "run":
		cfg.Resolve()
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := server.New(cfg, version, store).Run(ctx); err != nil {
			log.Fatal(err)
		}
	case "user":
		if err := userCmd(store, rest); err != nil {
			fmt.Fprintln(os.Stderr, "Fehler:", err)
			os.Exit(1)
		}
	case "enroll-key":
		if len(rest) > 0 && rest[0] == "--rotate" {
			k, err := store.RotateEnrollKey()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(k)
			return
		}
		fmt.Println(store.EnrollKey())
	default:
		fs.Usage()
		os.Exit(2)
	}
}

func userCmd(store *server.Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("user add|passwd|del|list")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list":
		for _, u := range store.Users() {
			role := "Benutzer"
			if u.Admin {
				role = "Admin"
			}
			state := ""
			if u.Disabled {
				state = " (deaktiviert)"
			}
			fmt.Printf("%-20s %-28s %-8s %d Geräte%s\n", u.Name, u.DisplayName, role, len(u.Devices), state)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("user add", flag.ExitOnError)
		admin := fs.Bool("admin", false, "grant administrator rights")
		display := fs.String("display", "", "display name")
		password := fs.String("password", "", "password (prompted if empty)")
		name, rest := splitName(args)
		fs.Parse(rest)
		if name == "" {
			return fmt.Errorf("Benutzername fehlt")
		}
		pw := *password
		if pw == "" {
			var err error
			if pw, err = promptPassword(); err != nil {
				return err
			}
		}
		if err := store.AddUser(name, *display, pw, *admin); err != nil {
			return err
		}
		fmt.Printf("Benutzer %q angelegt.\n", strings.ToLower(name))
		return nil
	case "passwd":
		name, _ := splitName(args)
		if name == "" {
			return fmt.Errorf("Benutzername fehlt")
		}
		pw, err := promptPassword()
		if err != nil {
			return err
		}
		if err := store.UpdateUser(name, server.UserPatch{Password: &pw}); err != nil {
			return err
		}
		fmt.Println("Passwort geändert. Bestehende Anmeldungen dieses Benutzers enden beim nächsten Server-Neustart.")
		return nil
	case "del":
		name, _ := splitName(args)
		if err := store.DeleteUser(name); err != nil {
			return err
		}
		fmt.Println("Benutzer gelöscht.")
		return nil
	}
	return fmt.Errorf("unbekannter Befehl %q", sub)
}

// splitName takes the first non-flag argument as the user name.
func splitName(args []string) (string, []string) {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
	}
	return "", args
}

func promptPassword() (string, error) {
	read := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}
	pw, err := read("Passwort: ")
	if err != nil {
		return "", err
	}
	if err := server.ValidatePassword(pw); err != nil {
		return "", err
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		again, err := read("Passwort wiederholen: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", fmt.Errorf("Passwörter stimmen nicht überein")
		}
	}
	return pw, nil
}
