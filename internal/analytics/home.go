package analytics

import "os"

var cachedHome, _ = os.UserHomeDir()

func homeDir() string { return cachedHome }
