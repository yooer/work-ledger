package web

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"work-ledger/internal/apperr"
	"work-ledger/internal/validator"
)

// Action 是一个 Ajax 操作：先过验证器，再调用处理函数（处理函数只调用 service）。
type Action struct {
	// Validate 把原始输入转为强类型参数；为 nil 时直接把 Input 传给 Handle。
	Validate func(in validator.Input) (any, error)
	// Handle 执行操作，返回 Result 或任意可 JSON 序列化的数据。
	Handle func(c *Ctx, input any) (any, error)
	// Guard 可对单个 action 追加更严格的权限（默认继承入口的权限）。
	Guard Guard
	// Multipart 为 true 时接受 multipart/form-data（文件上传例外）。
	Multipart bool
}

// Actions 是 action 名 → 操作 的分发表。
type Actions map[string]Action

// Result 是操作成功后的响应。
type Result struct {
	Message  string `json:"message,omitempty"`
	Redirect string `json:"redirect,omitempty"`
	Data     any    `json:"data,omitempty"`
	Reload   bool   `json:"reload,omitempty"`
	// FlashOnRedirect 为 true 时把 Message 存入会话，跳转后的页面显示。
	FlashOnRedirect bool `json:"-"`
}

var actionName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

const maxJSONBody = 2 << 20 // 2 MB

func (actions Actions) handler() HandlerFunc {
	return func(c *Ctx) error {
		in, err := readInput(c, actions)
		if err != nil {
			return err
		}
		name, _ := in["action"].(string)
		name = strings.TrimSpace(name)
		if name == "" || !actionName.MatchString(name) {
			return apperr.BadRequest("请求缺少有效的 action 字段。")
		}
		act, ok := actions[name]
		if !ok {
			return apperr.BadRequest("不支持的操作：" + name)
		}
		c.Action = name
		delete(in, "action")

		switch act.Guard {
		case Admin:
			if !c.IsAdmin() {
				return apperr.Forbidden("该操作仅系统管理员可以执行。")
			}
		case AdminManager:
			if !c.IsAdmin() && !c.IsManager() {
				return apperr.Forbidden("该操作仅系统管理员或部门经理可以执行。")
			}
		}

		var input any = in
		if act.Validate != nil {
			input, err = act.Validate(in)
			if err != nil {
				return err
			}
		}
		out, err := act.Handle(c, input)
		if err != nil {
			return err
		}
		if c.written {
			return nil
		}
		resp := map[string]any{"ok": true}
		switch r := out.(type) {
		case Result:
			if r.FlashOnRedirect && r.Message != "" {
				c.Flash("success", r.Message)
			}
			if r.Message != "" {
				resp["message"] = r.Message
			}
			if r.Redirect != "" {
				resp["redirect"] = r.Redirect
			}
			if r.Reload {
				resp["reload"] = true
			}
			if r.Data != nil {
				resp["data"] = r.Data
			}
		case nil:
		default:
			resp["data"] = r
		}
		return c.JSON(http.StatusOK, resp)
	}
}

func readInput(c *Ctx, actions Actions) (validator.Input, error) {
	ct, _, _ := mime.ParseMediaType(c.R.Header.Get("Content-Type"))
	switch ct {
	case "application/json":
		body, err := io.ReadAll(io.LimitReader(c.R.Body, maxJSONBody+1))
		if err != nil {
			return nil, apperr.BadRequest("读取请求失败。")
		}
		if len(body) > maxJSONBody {
			return nil, apperr.Status(http.StatusRequestEntityTooLarge, "too_large", "请求内容过大。")
		}
		in := validator.Input{}
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, apperr.BadRequest("请求体必须是 JSON 对象。")
		}
		return in, nil
	case "multipart/form-data":
		// 只有声明了 Multipart 的 action 才允许；大小限制由具体 action 在读取文件时控制。
		if err := c.R.ParseMultipartForm(8 << 20); err != nil {
			return nil, apperr.BadRequest("上传内容无法解析：" + err.Error())
		}
		in := validator.FromValues(c.R.MultipartForm.Value)
		name, _ := in["action"].(string)
		if act, ok := actions[name]; !ok || !act.Multipart {
			return nil, apperr.BadRequest("该操作不接受文件上传。")
		}
		return in, nil
	default:
		return nil, apperr.Status(http.StatusUnsupportedMediaType, "unsupported_media_type", "请求必须使用 JSON 格式（Content-Type: application/json）。")
	}
}
