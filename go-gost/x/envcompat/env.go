package envcompat

import (
	"os"
	"strings"
)

func Getenv(name string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	if strings.HasPrefix(name, "OVO_") {
		return os.Getenv("FLMX_" + strings.TrimPrefix(name, "OVO_"))
	}
	return ""
}
