package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DataDirEnv 是覆盖数据目录的环境变量。
const DataDirEnv = "SPROUT_DATA_DIR"

// ResolveDataDir 解析并准备数据目录。优先级：flagValue > SPROUT_DATA_DIR > <exeDir>/data/.sprout。
//
// 数据目录是安装级目录，跟随二进制，不跟随用户 home 或当前工作目录。
// 目录不可写时报错并提示 --data-dir，不会静默回退到别处。
func ResolveDataDir(flagValue string) (string, error) {
	return resolveDataDir(flagValue, os.Getenv(DataDirEnv), os.Executable)
}

func resolveDataDir(flagValue, envValue string, executable func() (string, error)) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = envValue
	}
	if dir == "" {
		exe, err := executable()
		if err != nil {
			return "", fmt.Errorf("无法定位可执行文件，请用 --data-dir 或 %s 指定数据目录: %w", DataDirEnv, err)
		}
		if isGoBuildPath(exe) {
			return "", fmt.Errorf("检测到通过 go run 启动（二进制在临时目录 %s），默认数据目录会落在临时目录里；"+
				"请显式指定：--data-dir ./data/.sprout（或设置 %s）", filepath.Dir(exe), DataDirEnv)
		}
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir = filepath.Join(filepath.Dir(exe), "data", ".sprout")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("数据目录路径无效 %q: %w", dir, err)
	}
	if err := ensureWritableDir(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// isGoBuildPath 判断路径是否位于 go run 生成的临时构建目录（go-build*）下。
// 两种分隔符都认：在 Unix 上调用时也可能拿到 Windows 风格的路径（跨平台测试、日志里的历史路径），
// filepath.ToSlash 只转当前平台的分隔符，反斜杠得自己先换掉。
func isGoBuildPath(p string) bool {
	for part := range strings.SplitSeq(strings.ReplaceAll(filepath.ToSlash(p), `\`, "/"), "/") {
		if strings.HasPrefix(part, "go-build") {
			return true
		}
	}
	return false
}

func ensureWritableDir(dir string) error {
	hint := "请用 --data-dir 或 " + DataDirEnv + " 指定一个可写的目录"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("无法创建数据目录 %s: %w；%s", dir, err, hint)
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("数据目录 %s 不可写: %w；%s", dir, err, hint)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// ModelsPath 是 models.jsonc 的路径。
func ModelsPath(dataDir string) string { return filepath.Join(dataDir, "models.jsonc") }

// MCPVersionsPath 是 MCP 协商版本缓存的路径：记住每个服务端上次谈成的协议版本，
// 下次启动可以跳过 server/discover 的试探，少一次 HTTPS 往返。
func MCPVersionsPath(dataDir string) string { return joinDataDir(dataDir, "mcp-versions.json") }

// MCPToolsPath 是 MCP 工具列表缓存的路径：有效期内启动时直接用，省掉一次 tools/list。
func MCPToolsPath(dataDir string) string { return joinDataDir(dataDir, "mcp-tools.json") }

// MCPAuthPath 是 MCP OAuth 令牌的存储路径（权限 0600）。令牌只放这里，不进其他缓存。
func MCPAuthPath(dataDir string) string { return joinDataDir(dataDir, "mcp-auth.json") }

// joinDataDir 是数据目录下文件的统一拼接入口；单独抽出来方便测试替换，也避免各处手拼分隔符。
func joinDataDir(dataDir, name string) string { return filepath.Join(dataDir, name) }

// SessionsDir 是会话根目录。
func SessionsDir(dataDir string) string { return filepath.Join(dataDir, "sessions") }

// LogsDir 是日志目录。
func LogsDir(dataDir string) string { return filepath.Join(dataDir, "logs") }

// SystemPromptPath 是系统提示词模板 SYSTEMPROMPT.md 的路径。
func SystemPromptPath(dataDir string) string { return joinDataDir(dataDir, "SYSTEMPROMPT.md") }

// AgentsFileName 是用户写给 agent 的说明文件名：数据目录里的一份是全局说明，工作目录及其上级里的是项目说明。
const AgentsFileName = "AGENTS.md"

// AgentsPath 是数据目录里全局 AGENTS.md 的路径。
func AgentsPath(dataDir string) string { return joinDataDir(dataDir, AgentsFileName) }
