package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"

	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/mcp"
	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/cli"
)

// runMCPAuth 实现 sprout mcp login / logout。只读 mcp.jsonc、写令牌文件，不装配 agent。
func runMCPAuth(dataDir, action, name string, getenv func(string) string, hc *http.Client, openURL func(string) error, stdout, stderr io.Writer) int {
	f, err := config.LoadMCP(config.MCPPath(dataDir))
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	srv, ok := f.Servers[name]
	if !ok {
		fmt.Fprintf(stderr, "错误: mcp.jsonc 里没有名为 %q 的服务端（已有：%v）\n", name, f.ServerNames())
		return cli.ExitError
	}
	store := mcp.NewAuthStore(config.MCPAuthPath(dataDir))

	if action == "logout" {
		had, err := store.Logout(name)
		switch {
		case err != nil:
			fmt.Fprintln(stderr, "错误:", err)
			return cli.ExitError
		case had:
			fmt.Fprintf(stdout, "已退出 %s 的登录，删除了保存的令牌。\n", name)
		default:
			fmt.Fprintf(stdout, "%s 没有保存的登录。\n", name)
		}
		return 0
	}

	if srv.URL == "" {
		fmt.Fprintf(stderr, "错误: %s 是本地 stdio 服务端，不需要 OAuth 登录\n", name)
		return cli.ExitError
	}
	if srv.OAuth == nil {
		fmt.Fprintf(stderr, "错误: %s 没有配置 oauth；在 mcp.jsonc 里给它加上 \"oauth\": {} 后再登录\n", name)
		return cli.ExitError
	}
	spec := mcpSpec(name, srv, getenv)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	open := func(_ context.Context, u string) error {
		fmt.Fprintln(stdout, "在浏览器里打开下面的地址完成授权（等待 5 分钟，Ctrl+C 取消）：")
		fmt.Fprintln(stdout, u)
		if err := openURL(u); err != nil {
			fmt.Fprintln(stderr, "提示: 没能自动打开浏览器，请手动复制上面的地址:", err)
		}
		return nil
	}
	if err := mcp.Login(ctx, store, name, spec.URL, *spec.OAuth, hc, open); err != nil {
		fmt.Fprintln(stderr, "错误: 登录失败:", err)
		return cli.ExitError
	}
	fmt.Fprintf(stdout, "已登录 %s。正在运行的 sprout 在 /mcp 面板按 r 重连即可使用。\n", name)
	return 0
}

// defaultOpenURL 是 runMCPAuth 在正式运行时用的浏览器打开方式；测试里换成直接访问回调。
var defaultOpenURL = platform.OpenURL
