// Package apperr 定义跨层传递的业务/校验错误。
package apperr

import (
	"errors"
	"net/http"
)

// Error 是可以安全展示给用户的错误。
type Error struct {
	Status  int               // HTTP 状态码
	Code    string            // 机器可读代码
	Message string            // 展示给用户的消息
	Fields  map[string]string // 字段级错误（验证器使用）
}

func (e *Error) Error() string { return e.Message }

// New 业务错误（422）。
func New(msg string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "business_error", Message: msg}
}

func NotFound(msg string) *Error {
	if msg == "" {
		msg = "没有找到对应的记录。"
	}
	return &Error{Status: http.StatusNotFound, Code: "not_found", Message: msg}
}

func Forbidden(msg string) *Error {
	if msg == "" {
		msg = "无权访问该内容。"
	}
	return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: msg}
}

func BadRequest(msg string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "bad_request", Message: msg}
}

func Unavailable(msg string) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable", Message: msg}
}

func Status(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// Validation 字段校验错误。
func Validation(fields map[string]string) *Error {
	msg := "提交的内容有误，请检查后重试。"
	if len(fields) == 1 {
		for _, m := range fields {
			msg = m
		}
	}
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: msg, Fields: fields}
}

// As 提取 *Error。
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
