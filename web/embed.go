// Package webassets 内嵌模板与静态资源，生成单文件程序。
package webassets

import "embed"

//go:embed templates assets static
var FS embed.FS
