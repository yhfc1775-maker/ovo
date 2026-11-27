package config

import "go-backend/internal/envcompat"

// Getenv reads OVO first, then FLMX only when the current key is absent.
// FLUX and FLVX environment keys have retired.
func Getenv(name string) string { return envcompat.Getenv(name) }
