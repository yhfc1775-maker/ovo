package api

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/registry"
	"github.com/go-gost/x/socket"
	"net/http"
	"strings"
)

// swagger:parameters createServiceRequest
type createServiceRequest struct {
	// in: body
	Data config.ServiceConfig `json:"data"`
}

// successful operation.
// swagger:response createServiceResponse
type createServiceResponse struct {
	Data Response
}

// swagger:parameters createServicesRequest
type createServicesRequest struct {
	// in: body
	Data []config.ServiceConfig `json:"data"`
}

// successful operation.
// swagger:response createServicesResponse
type createServicesResponse struct {
	Data Response
}

func createService(ctx *gin.Context) {
	var req createServiceRequest
	if err := ctx.ShouldBindJSON(&req.Data); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	data := []config.ServiceConfig{req.Data}
	if err := socket.MutateServices("create", data, nil); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

func createServices(ctx *gin.Context) {
	var req createServicesRequest
	if err := ctx.ShouldBindJSON(&req.Data); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	data := req.Data
	if err := socket.MutateServices("create", data, nil); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

// swagger:parameters updateServiceRequest
type updateServiceRequest struct {
	// in: path
	// required: true
	Service string `uri:"service" json:"service"`
	// in: body
	Data config.ServiceConfig `json:"data"`
}

// successful operation.
// swagger:response updateServiceResponse
type updateServiceResponse struct {
	Data Response
}

// swagger:parameters updateServicesRequest
type updateServicesRequest struct {
	// in: body
	Data []config.ServiceConfig `json:"data"`
}

// successful operation.
// swagger:response updateServicesResponse
type updateServicesResponse struct {
	Data Response
}

func updateService(ctx *gin.Context) {
	var req updateServiceRequest
	if err := ctx.ShouldBindUri(&req); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service name"))
		return
	}
	if err := ctx.ShouldBindJSON(&req.Data); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	req.Data.Name = strings.TrimSpace(req.Service)
	data := []config.ServiceConfig{req.Data}
	for _, sc := range data {
		if !registry.ServiceRegistry().IsRegistered(strings.TrimSpace(sc.Name)) {
			writeError(ctx, NewError(400, ErrCodeNotFound, "service not found"))
			return
		}
	}
	if err := socket.MutateServices("update", data, nil); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

func updateServices(ctx *gin.Context) {
	var req updateServicesRequest
	if err := ctx.ShouldBindJSON(&req.Data); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	data := req.Data
	for _, sc := range data {
		if !registry.ServiceRegistry().IsRegistered(strings.TrimSpace(sc.Name)) {
			writeError(ctx, NewError(400, ErrCodeNotFound, "service not found"))
			return
		}
	}
	if err := socket.MutateServices("update", data, nil); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

// swagger:parameters deleteServiceRequest
type deleteServiceRequest struct {
	// in: path
	// required: true
	Service string `uri:"service" json:"service"`
}

// successful operation.
// swagger:response deleteServiceResponse
type deleteServiceResponse struct {
	Data Response
}

// swagger:parameters deleteServicesRequest
type deleteServicesRequest struct {
	// in: body
	Services []string `json:"services"`
}

// successful operation.
// swagger:response deleteServicesResponse
type deleteServicesResponse struct {
	Data Response
}

func deleteService(ctx *gin.Context) {
	var req deleteServiceRequest
	if err := ctx.ShouldBindUri(&req); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	names := []string{strings.TrimSpace(req.Service)}
	for _, name := range names {
		if !registry.ServiceRegistry().IsRegistered(strings.TrimSpace(name)) {
			writeError(ctx, NewError(400, ErrCodeNotFound, "service not found"))
			return
		}
	}
	if err := socket.MutateServices("delete", nil, names); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

func deleteServices(ctx *gin.Context) {
	var req deleteServicesRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	names := req.Services
	for _, name := range names {
		if !registry.ServiceRegistry().IsRegistered(strings.TrimSpace(name)) {
			writeError(ctx, NewError(400, ErrCodeNotFound, "service not found"))
			return
		}
	}
	if err := socket.MutateServices("delete", nil, names); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

// swagger:parameters pauseServiceRequest
type pauseServiceRequest struct {
	// in: path
	// required: true
	Service string `uri:"service" json:"service"`
}

// successful operation.
// swagger:response pauseServiceResponse
type pauseServiceResponse struct {
	Data Response
}

// swagger:parameters resumeServiceRequest
type resumeServiceRequest struct {
	// in: path
	// required: true
	Service string `uri:"service" json:"service"`
}

// successful operation.
// swagger:response resumeServiceResponse
type resumeServiceResponse struct {
	Data Response
}

func pauseService(ctx *gin.Context) {
	var req pauseServiceRequest
	if err := ctx.ShouldBindUri(&req); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	names := []string{strings.TrimSpace(req.Service)}
	for _, name := range names {
		if !registry.ServiceRegistry().IsRegistered(strings.TrimSpace(name)) {
			writeError(ctx, NewError(400, ErrCodeNotFound, "service not found"))
			return
		}
	}
	if err := socket.MutateServices("pause", nil, names); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

func resumeService(ctx *gin.Context) {
	var req resumeServiceRequest
	if err := ctx.ShouldBindUri(&req); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	names := []string{strings.TrimSpace(req.Service)}
	for _, sc := range config.Global().Services {
		if sc != nil && sc.Name == names[0] && sc.Metadata["paused"] != true {
			writeError(ctx, NewError(400, ErrCodeInvalid, "service is not paused"))
			return
		}
	}
	if err := socket.MutateServices("resume", nil, names); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

// swagger:parameters getServiceStatusRequest
type getServiceStatusRequest struct {
	// in: path
	// required: true
	Service string `uri:"service" json:"service"`
}

// successful operation.
// swagger:response getServiceStatusResponse
type getServiceStatusResponse struct {
	Data ServiceStatusInfo
}

type ServiceStatusInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"` // running, paused, stopped
	Paused bool   `json:"paused"`
}

func getServiceStatus(ctx *gin.Context) {
	// swagger:route GET /config/services/{service}/status Service getServiceStatusRequest
	//
	// Get service status by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: getServiceStatusResponse

	var req getServiceStatusRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.Service)

	// 检查服务是否在注册表中
	svc := registry.ServiceRegistry().Get(name)
	if svc == nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("service %s not found", name)))
		return
	}

	// 检查配置中的暂停状态
	var paused bool
	cfg := config.Global()
	for _, s := range cfg.Services {
		// R19-F02: nil 项跳过并记日志，不得 panic。
		if s == nil {
			logger.Default().Warnf("get service status: skipped nil service entry")
			continue
		}
		if s.Name == name && s.Metadata != nil {
			if pausedVal, exists := s.Metadata["paused"]; exists && pausedVal == true {
				paused = true
			}
			break
		}
	}

	status := "running"
	if paused {
		status = "paused"
	}

	ctx.JSON(http.StatusOK, getServiceStatusResponse{
		Data: ServiceStatusInfo{
			Name:   name,
			Status: status,
			Paused: paused,
		},
	})
}

// 回滚已暂停的服务 - 重新启动它们
// swagger:parameters pauseServicesRequest
type pauseServicesRequest struct {
	// in: body
	Services []string `json:"services"`
}

// successful operation.
// swagger:response pauseServicesResponse
type pauseServicesResponse struct {
	Data Response
}

// swagger:parameters resumeServicesRequest
type resumeServicesRequest struct {
	// in: body
	Services []string `json:"services"`
}

// successful operation.
// swagger:response resumeServicesResponse
type resumeServicesResponse struct {
	Data Response
}

func pauseServices(ctx *gin.Context) {
	var req pauseServicesRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	names := req.Services
	for _, name := range names {
		if !registry.ServiceRegistry().IsRegistered(strings.TrimSpace(name)) {
			writeError(ctx, NewError(400, ErrCodeNotFound, "service not found"))
			return
		}
	}
	if err := socket.MutateServices("pause", nil, names); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

func resumeServices(ctx *gin.Context) {
	var req resumeServicesRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		writeError(ctx, NewError(400, ErrCodeInvalid, "invalid service request"))
		return
	}
	names := req.Services
	if err := socket.MutateServices("resume", nil, names); err != nil {
		writeServiceMutationError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}

func writeServiceMutationError(ctx *gin.Context, err error) {
	status, code := http.StatusInternalServerError, ErrCodeFailed
	switch {
	case errors.Is(err, socket.ErrServiceInvalid):
		status, code = http.StatusBadRequest, ErrCodeInvalid
	case errors.Is(err, socket.ErrServiceDuplicate):
		status, code = http.StatusBadRequest, ErrCodeDup
	case errors.Is(err, socket.ErrServiceNotFound):
		status, code = http.StatusBadRequest, ErrCodeNotFound
	}
	writeError(ctx, NewError(status, code, err.Error()))
}
