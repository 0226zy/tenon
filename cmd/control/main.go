package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0226zy/tenon/internal/control/auth"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/idgen"
)

func main() { os.Exit(execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(stderr)
		configPath := fs.String("config", envOr("CONTROL_CONFIG", "deploy/control.local.yaml"), "control YAML configuration")
		if err = fs.Parse(args[1:]); err == nil {
			var cfg *config.Config
			cfg, err = config.Load(*configPath)
			if err == nil {
				serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
				defer stop()
				err = serve(serveCtx, cfg, stdout)
			}
		}
	case "user":
		err = userCommand(ctx, args[1:], stdout, stderr)
	case "usage":
		err = usageCommand(ctx, args[1:], stdout, stderr)
	case "status":
		err = statusCommand(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "control:", err)
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `control - tenon control plane

Usage:
  control serve -config deploy/control.local.yaml
  control user add -config FILE -user ID [-name NAME] (-password PASS | CONTROL_PASSWORD=...) [-max-conv N]
  control user disable -config FILE -user ID
  control user passwd -config FILE -user ID (-password PASS | CONTROL_PASSWORD=...)
  control usage -config FILE [-user ID] [-from YYYY-MM-DD] [-to YYYY-MM-DD]
  control status -config FILE`)
}

func userCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("user subcommand is required: add, disable or passwd")
	}
	action := args[0]
	fs := flag.NewFlagSet("user "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", envOr("CONTROL_CONFIG", "deploy/control.local.yaml"), "control YAML configuration")
	userID := fs.String("user", "", "user id")
	name := fs.String("name", "", "display name")
	password := fs.String("password", "", "password (prefer CONTROL_PASSWORD)")
	maxConv := fs.Int("max-conv", 0, "maximum active conversations")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *userID == "" && fs.NArg() > 0 {
		*userID = fs.Arg(0)
	}
	if *userID == "" {
		return fmt.Errorf("-user is required")
	}
	cfg, st, err := openStore(*configPath)
	if err != nil {
		return err
	}
	defer st.Close()
	users := auth.New(st, idgen.New(), cfg.Auth)
	switch action {
	case "add":
		secret := passwordValue(*password)
		if secret == "" {
			return fmt.Errorf("-password or CONTROL_PASSWORD is required")
		}
		if *name == "" {
			*name = *userID
		}
		if *maxConv <= 0 {
			*maxConv = cfg.User.DefaultMaxConv
		}
		if err := users.CreateUser(ctx, *userID, *name, secret, *maxConv); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "user %s created\n", *userID)
		return nil
	case "disable":
		if err := users.Disable(ctx, *userID); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "user %s disabled; sessions revoked\n", *userID)
		return nil
	case "passwd":
		secret := passwordValue(*password)
		if secret == "" {
			return fmt.Errorf("-password or CONTROL_PASSWORD is required")
		}
		if err := users.SetPassword(ctx, *userID, secret); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "password updated; sessions revoked\n")
		return nil
	default:
		return fmt.Errorf("unknown user subcommand %q", action)
	}
}

func usageCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", envOr("CONTROL_CONFIG", "deploy/control.local.yaml"), "control YAML configuration")
	userID := fs.String("user", "", "optional user id")
	fromText := fs.String("from", "", "inclusive RFC3339 or YYYY-MM-DD")
	toText := fs.String("to", "", "exclusive RFC3339 or YYYY-MM-DD")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, st, err := openStore(*configPath)
	if err != nil {
		return err
	}
	defer st.Close()
	to := time.Now().UTC()
	if *toText != "" {
		to, err = parseCLITime(*toText)
		if err != nil {
			return fmt.Errorf("invalid -to: %w", err)
		}
	}
	from := to.AddDate(0, 0, -30)
	if *fromText != "" {
		from, err = parseCLITime(*fromText)
		if err != nil {
			return fmt.Errorf("invalid -from: %w", err)
		}
	}
	if !from.Before(to) {
		return fmt.Errorf("-from must be before -to")
	}
	rows, err := st.ReportUsage(ctx, *userID, from, to)
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{"from": from, "to": to, "items": rows})
}

func statusCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", envOr("CONTROL_CONFIG", "deploy/control.local.yaml"), "control YAML configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, st, err := openStore(*configPath)
	if err != nil {
		return err
	}
	defer st.Close()
	runs, err := st.ActiveRunCounts(ctx)
	if err != nil {
		return err
	}
	sandboxes, err := st.SandboxCounts(ctx)
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{"mode": cfg.Mode, "sandbox_driver": cfg.Sandbox.Driver, "active_runs": runs, "sandboxes": sandboxes, "time": time.Now().UTC()})
}

func openStore(path string) (*config.Config, *store.Store, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	dialect, dsn := "mysql", cfg.MySQL.DSN
	if cfg.Mode == "local" {
		dialect, dsn = "sqlite", cfg.SQLite.Path
	}
	st, err := store.Open(context.Background(), dialect, dsn)
	if err != nil {
		return nil, nil, err
	}
	return cfg, st, nil
}

func parseCLITime(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02", value)
	return t.UTC(), err
}
func passwordValue(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("CONTROL_PASSWORD")
}
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
