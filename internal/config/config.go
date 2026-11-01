// Package config loads settings from a litecoin.conf-style file
// (key=value, '#' comments) and command-line flags. Flags win over the file.
package config

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
)

const FileName = "omnilite-standalone.conf"

type Config struct {
	DataDir string
	Chain   chain.Params

	RPCConnect    string
	RPCPort       int
	RPCUser       string
	RPCPassword   string
	RPCCookieFile string

	// PollInterval is how often the node is asked for new blocks once synced.
	PollInterval time.Duration
	// UndoDepth is how many blocks of undo data are kept for reorgs.
	UndoDepth int64

	Check bool
}

// RPCURL returns the node's RPC endpoint.
func (c *Config) RPCURL() string {
	return fmt.Sprintf("http://%s:%d", c.RPCConnect, c.RPCPort)
}

// DBPath returns the database file for the configured network.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, c.Chain.Name, "index.db")
}

// Credentials returns user and password, reading the cookie file when no
// password is configured.
func (c *Config) Credentials() (string, string, error) {
	if c.RPCPassword != "" {
		return c.RPCUser, c.RPCPassword, nil
	}
	path := c.RPCCookieFile
	if path == "" {
		path = defaultCookieFile(c.Chain)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("no rpcpassword set and cookie file not readable: %w", err)
	}
	user, pass, ok := strings.Cut(strings.TrimSpace(string(data)), ":")
	if !ok {
		return "", "", fmt.Errorf("cookie file %s has no user:password", path)
	}
	return user, pass, nil
}

// Load parses args (without the program name).
func Load(args []string) (*Config, error) {
	flags := flag.NewFlagSet("omnilite-standalone", flag.ContinueOnError)
	conf := flags.String("conf", "", "config file (default: <datadir>/"+FileName+")")
	datadir := flags.String("datadir", defaultDataDir(), "data directory")
	check := flags.Bool("check", false, "check the node connection and exit")
	set := map[string]*string{}
	for _, k := range []string{"network", "rpcconnect", "rpcport", "rpcuser", "rpcpassword", "rpccookiefile", "pollinterval", "undodepth"} {
		set[k] = flags.String(k, "", "see "+FileName)
	}
	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	values := map[string]string{}
	path := *conf
	if path == "" {
		path = filepath.Join(*datadir, FileName)
	}
	if err := readFile(path, values); err != nil {
		if *conf != "" || !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	flags.Visit(func(f *flag.Flag) {
		if p, ok := set[f.Name]; ok {
			values[f.Name] = *p
		}
	})

	params, err := chain.ByName(values["network"])
	if err != nil {
		return nil, err
	}
	c := &Config{
		DataDir:       *datadir,
		Chain:         params,
		RPCConnect:    "127.0.0.1",
		RPCPort:       params.RPCPort,
		RPCUser:       values["rpcuser"],
		RPCPassword:   values["rpcpassword"],
		RPCCookieFile: values["rpccookiefile"],
		PollInterval:  5 * time.Second,
		UndoDepth:     100,
		Check:         *check,
	}
	if v := values["rpcconnect"]; v != "" {
		c.RPCConnect = v
	}
	if v := values["rpcport"]; v != "" {
		if c.RPCPort, err = strconv.Atoi(v); err != nil {
			return nil, fmt.Errorf("rpcport: %w", err)
		}
	}
	if v := values["pollinterval"]; v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs < 1 {
			return nil, fmt.Errorf("pollinterval: want whole seconds >= 1, got %q", v)
		}
		c.PollInterval = time.Duration(secs) * time.Second
	}
	if v := values["undodepth"]; v != "" {
		if c.UndoDepth, err = strconv.ParseInt(v, 10, 64); err != nil || c.UndoDepth < 10 {
			return nil, fmt.Errorf("undodepth: want a number >= 10, got %q", v)
		}
	}
	return c, nil
}

func readFile(path string, values map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected key=value", path, n)
		}
		values[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return sc.Err()
}

func defaultDataDir() string {
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, "OmniLiteStandalone")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".omnilite-standalone")
}

// defaultCookieFile mirrors Litecoin Core's default data directory layout.
func defaultCookieFile(p chain.Params) string {
	var base string
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		base = filepath.Join(os.Getenv("APPDATA"), "Litecoin")
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support", "Litecoin")
	default:
		base = filepath.Join(home, ".litecoin")
	}
	switch p.Name {
	case "test":
		base = filepath.Join(base, "testnet4")
	case "regtest":
		base = filepath.Join(base, "regtest")
	}
	return filepath.Join(base, ".cookie")
}
