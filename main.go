package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/kirill-shtrykov/sam/server"
)

// Lookup environment variable `key` or set string value `defaultVal`
func lookupEnvOrString(key string, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

func main() {
	var (
		addr string // Address to listen. Default to "127.0.0.1:6250"
		dir  string // Wiki root directory. Default to "./"
		base string // HTTP base URL. Default to "/"
	)

	flag.StringVar(&addr, "addr", lookupEnvOrString("SAM_ADDR", "127.0.0.1:6250"), "address to listen")
	flag.StringVar(&dir, "dir", lookupEnvOrString("SAM_DIR", "./"), "root directory")
	flag.StringVar(&base, "base", lookupEnvOrString("SAM_BASE", "/"), "server base URL")
	flag.Parse()

	// Expand Linux home directory
	if strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Error expand user home directory path for %s: %v", dir, err)
		}
		dir = filepath.Join(home, dir[2:])
	}

	server.Run(addr, dir, base)
}
