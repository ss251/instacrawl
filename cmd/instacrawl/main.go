// Command instacrawl is a local-first archiver for your own Instagram data.
//
// It implements the crawlkit control contract (`metadata --json` /
// `status --json`) so the crawlctl scheduler can discover and schedule it,
// `auth` to import/verify the session, and `sync` to archive direct messages
// into the local SQLite store.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/openclaw/crawlkit/output"
	"github.com/ss251/instacrawl/internal/appmeta"
)

// version is overridable at build time via
// -ldflags "-X main.version=<v>".
var version = "0.0.0-dev"

// usageError marks an error that should exit with code 2 (bad invocation),
// distinct from a runtime failure (exit 1). It composes with crawlkit's
// output.IsUsage via errors.As on output.UsageError, but we keep our own type
// for CLI-level argument problems.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// Hoist a global --json flag so it may appear before or after the
	// subcommand, matching the family's UX (e.g. both `instacrawl --json status`
	// and `instacrawl status --json` work).
	jsonFlag, rest := hoistJSON(args)

	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, usageText())
		return 2
	}

	cmd, cmdArgs := rest[0], rest[1:]

	var err error
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println(version)
		return 0
	case "help", "--help", "-h":
		fmt.Println(usageText())
		return 0
	case "metadata":
		err = cmdMetadata(jsonFlag, cmdArgs)
	case "status":
		err = cmdStatus(jsonFlag, cmdArgs)
	case "auth":
		err = cmdAuth(jsonFlag, cmdArgs)
	case "sync":
		err = cmdSync(cmdArgs)
	case "carve":
		err = cmdCarve(cmdArgs)
	default:
		err = usageError{msg: fmt.Sprintf("unknown command %q", cmd)}
	}

	return report(err)
}

// errReported is a sentinel returned by a command that has already printed its
// own rich, user-facing output (e.g. a terminal probe verdict with a challenge
// URL). report() maps it to exit 1 without printing anything further.
var errReported = errors.New("reported")

// report maps an error to an exit code and writes it to stderr: 0 on success,
// 2 for usage problems (ours or crawlkit's), 1 for everything else. Success is
// silent; failure is verbose and specific.
func report(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, errReported) {
		return 1 // the command already printed a specific message
	}
	fmt.Fprintf(os.Stderr, "instacrawl: %v\n", err)
	var ue usageError
	if errors.As(err, &ue) || output.IsUsage(err) {
		return 2
	}
	return 1
}

func cmdMetadata(jsonFlag bool, args []string) error {
	format, err := resolveFormat(jsonFlag, args)
	if err != nil {
		return err
	}
	// The manifest is a machine contract; default it to JSON even without the
	// flag, since text-formatting a nested struct is not useful to the scheduler.
	if format == output.Text {
		format = output.JSON
	}
	return output.Write(os.Stdout, format, "metadata", appmeta.BuildManifest())
}

func cmdStatus(jsonFlag bool, args []string) error {
	format, err := resolveFormat(jsonFlag, args)
	if err != nil {
		return err
	}
	if format == output.Text {
		format = output.JSON
	}
	return output.Write(os.Stdout, format, "status", appmeta.BuildStatus())
}

// resolveFormat turns the hoisted --json flag plus any trailing --json into a
// crawlkit output.Format. A stray unrecognized flag is a usage error.
func resolveFormat(jsonFlag bool, args []string) (output.Format, error) {
	for _, a := range args {
		switch a {
		case "--json":
			jsonFlag = true
		default:
			return "", usageError{msg: fmt.Sprintf("unknown flag %q", a)}
		}
	}
	return output.Resolve("", jsonFlag)
}

// hoistJSON pulls a global --json out of the arg list wherever it appears,
// returning the flag value and the remaining args in order.
func hoistJSON(args []string) (bool, []string) {
	jsonFlag := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			jsonFlag = true
			continue
		}
		rest = append(rest, a)
	}
	return jsonFlag, rest
}

func usageText() string {
	return `instacrawl — local-first archiver for your own Instagram data

usage:
  instacrawl <command> [--json]

commands:
  auth       manage your session: import | status | logout
  sync       archive your DMs and own content (posts, reels, saved, stories)
  carve      recover expired media bytes from a local browser/app cache (offline)
  metadata   print the crawlkit control manifest (JSON)
  status     print archive status (JSON)
  version    print the version
  help       print this help

auth:
  instacrawl auth import [--from-file <path> | --stdin] [--no-verify]
                         [--fetcher web|instagrapi]
  instacrawl auth status [--offline] [--json]
  instacrawl auth logout

sync:
  instacrawl sync [--only all|dms|feed] [--full-recrawl] [--no-media]
                  [--include-pending] [--thread <id>]
                  [--include-saved] [--include-collections] [--include-stories]
                  [--include-liked] [--max-feed-pages N]

carve:
  instacrawl carve --dir <cache-path> [--dir <cache-path> ...]
                   [--dry-run] [--format walker|chromium|indexeddb]

  Zero-network: reads only the local cache you point it at, and backfills the
  bytes of media whose Instagram CDN URLs have since expired. --format=walker
  (the default) works on any directory of media files; the chromium/indexeddb
  extractors are stubs until validated against a real cache.

instacrawl archives ONLY your own logged-in account, stores everything locally,
and uploads nothing. See README.md and DECISIONS.md.`
}
