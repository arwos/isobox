package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/arwos/isobox/internal/proxy"
)

const proxyUsage = `Usage:
  isobox proxy add --alias=ALIAS --server=HOST:PORT [--auth=none|userpass]
  isobox proxy list
  isobox proxy remove --alias=ALIAS

For --auth=userpass, username and password are requested interactively. They
are not accepted as command-line options and are not shown by the list command.
SOCKS5 does not encrypt credentials or proxied application traffic.
`

func executeProxy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, proxyUsage)
		return 0
	}
	switch args[0] {
	case "add":
		return executeProxyAdd(args[1:], stdout, stderr)
	case "list":
		return executeProxyList(args[1:], stdout, stderr)
	case "remove":
		return executeProxyRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "isobox proxy: unknown action %q\n\n%s", args[0], proxyUsage)
		return 2
	}
}

func executeProxyAdd(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isobox proxy add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var alias, server, authValue string
	flags.StringVar(&alias, "alias", "", "proxy alias referenced by profiles")
	flags.StringVar(&server, "server", "", "SOCKS5 host and port")
	flags.StringVar(&authValue, "auth", string(proxy.AuthNone), "SOCKS5 authentication: none or userpass")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, proxyUsage)
		return 0
	} else if err != nil {
		fmt.Fprintf(stderr, "isobox proxy add: %v\n\n%s", err, proxyUsage)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "isobox proxy add: positional arguments are not accepted\n\n%s", proxyUsage)
		return 2
	}
	if alias == "" || server == "" {
		fmt.Fprintf(stderr, "isobox proxy add: --alias and --server are required\n\n%s", proxyUsage)
		return 2
	}
	auth := proxy.AuthMode(authValue)
	if err := proxy.Validate(alias, server, auth); err != nil {
		fmt.Fprintf(stderr, "isobox proxy add: %v\n", err)
		return 2
	}
	var username, password string
	if auth == proxy.AuthUserPass {
		var err error
		username, password, err = readProxyCredentials()
		if err != nil {
			fmt.Fprintf(stderr, "isobox proxy add: cannot read credentials: %v\n", err)
			return 1
		}
	}
	if err := proxy.Add(alias, server, auth, username, password); err != nil {
		fmt.Fprintf(stderr, "isobox proxy add: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Added SOCKS5 proxy %q at %s (auth: %s).\n", alias, server, auth)
	warnUnencryptedProxy(stderr, alias)
	return 0
}

func executeProxyList(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "isobox proxy list: no options or positional arguments are accepted\n\n%s", proxyUsage)
		return 2
	}
	items, err := proxy.List()
	if err != nil {
		fmt.Fprintf(stderr, "isobox proxy list: %v\n", err)
		return 1
	}
	if len(items) == 0 {
		_, _ = io.WriteString(stdout, "No SOCKS5 proxies configured.\n")
		return 0
	}
	_, _ = io.WriteString(stdout, "ALIAS\tSERVER\tAUTH\n")
	for _, item := range items {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", item.Alias, item.Server, item.Auth)
	}
	return 0
}

func executeProxyRemove(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isobox proxy remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var alias string
	flags.StringVar(&alias, "alias", "", "proxy alias to remove")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, proxyUsage)
		return 0
	} else if err != nil {
		fmt.Fprintf(stderr, "isobox proxy remove: %v\n\n%s", err, proxyUsage)
		return 2
	}
	if flags.NArg() != 0 || alias == "" {
		fmt.Fprintf(stderr, "isobox proxy remove: --alias is required and positional arguments are not accepted\n\n%s", proxyUsage)
		return 2
	}
	if err := proxy.Remove(alias); err != nil {
		fmt.Fprintf(stderr, "isobox proxy remove: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed SOCKS5 proxy %q.\n", alias)
	return 0
}

func warnUnencryptedProxy(stderr io.Writer, alias string) {
	fmt.Fprintf(stderr, "isobox warning: SOCKS5 proxy %q does not encrypt credentials or application traffic; use it only on a trusted network.\n", alias)
}

func inspectAndWarnProxy(alias string, stderr io.Writer) error {
	info, err := proxy.Inspect(alias)
	if err != nil {
		return fmt.Errorf("proxy alias %q is unavailable: %w", alias, err)
	}
	warnUnencryptedProxy(stderr, info.Alias)
	return nil
}
