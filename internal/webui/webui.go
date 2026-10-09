// Package webui 提供升级服务的运维管理页：一个内嵌的单页应用，直接调用 /v1/admin/*
// 的 JSON 接口。
//
// 为什么不另起一个前端工程：管理面只有十几张表和三个写操作，为它引入一套构建链，
// 运维要装的东西、要修的东西都比它省下来的代码多。内嵌单页跟着二进制一起发布，
// 版本天然对齐；页面与接口的一致性由 internal/api 的结构测试兜底。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets
var assets embed.FS

// Assets 返回内嵌的静态资源，供测试断言「页面调用的接口」与「服务端注册的接口」没有漂移。
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// embed 的资源在编译期就确定了，走到这里说明二进制被破坏，直接炸掉比静默 404 好。
		panic("webui: embedded assets are missing: " + err.Error())
	}
	return sub
}

// Handler 返回管理页的处理器，挂在 /admin/ 下。页面本身不需要令牌——它调用的
// /v1/admin/* 仍然逐个校验 Bearer Token，没有令牌的浏览器只会看到「未授权」。
func Handler() http.Handler {
	files := http.FileServer(http.FS(Assets()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 单页应用按 hash 路由，刷新任意深层路径都应该回到同一份 index.html，而不是 404。
		if _, err := fs.Stat(Assets(), strings.TrimPrefix(r.URL.Path, "/")); err != nil {
			cloned := r.Clone(r.Context())
			cloned.URL.Path = "/"
			r = cloned
		}
		// 管理页带着令牌操作，任何一层缓存都不该留下它的副本。
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// 页面只由同源的 app.js / app.css 组成，令牌只经 fetch 发给同源接口：
		// 关掉其余一切加载与嵌入，把 XSS 与点击劫持的面收窄到零。
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; "+
				"img-src 'self' data:; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		files.ServeHTTP(w, r)
	})
}
