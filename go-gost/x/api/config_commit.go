package api

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	reg "github.com/go-gost/core/registry"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/registry"
)

func commitNamedConfig[T any](ctx *gin.Context, r reg.Registry[T], name string, value T, remove bool, update func(*config.Config) error) bool {
	if err := registry.CommitNamed(r, name, value, remove, func() error { return config.OnUpdate(update) }); err != nil {
		if !remove {
			if closer, ok := any(value).(io.Closer); ok {
				_ = closer.Close()
			}
		}
		writeError(ctx, NewError(http.StatusInternalServerError, ErrCodeFailed, "configuration commit failed"))
		return false
	}
	return true
}
