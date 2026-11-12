# 工具注册（tools.jsonc）

工具是否交给模型，由数据目录下的 `tools.jsonc` 决定。文件不存在或为空时，启动会用内置模板重建（默认四个工具全开）。

## 规则

- 只有出现在 `tools.jsonc` 里、且 `"enable": true` 的工具才会注册给 agent。
- 没写出的工具视为**未注册**；`enable` 为 `false` 或省略同样不启用。
- 写了不认识的名字会在启动时直接报错，并列出可用工具名——拼错不等于关闭。
- 开关只决定“想不想用”，不保证“本机有没有”：`bash` / `powershell` 还要探测到可执行文件才会注册。
  已启用但本机不可用的工具不会交给模型，原因显示在 TUI 的 `/tools` 里。
- 修改后需要重启 sprout 才会生效；`/model` 切换模型时会按同一份配置重建工具表。
- 模型的 `tools: false` 优先级更高：该模型下不向服务端提供任何工具。

## 内置工具

| 工具 | 参数 | 说明 |
| --- | --- | --- |
| `bash` | `command`、`timeout_seconds` | 执行 bash 命令；Windows 上需要真实的 Git Bash / MSYS2 / Cygwin |
| `powershell` | `command`、`timeout_seconds` | 执行 PowerShell 命令；优先 `pwsh` 7+，回退 5.1 |
| `read` | `path`、`offset`、`limit` | 按行读文本文件：默认从第 1 行读 2000 行，行号随内容返回 |
| `write` | `path`、`content` | 创建或覆盖文本文件，自动建父目录，先写临时文件再原子替换 |

### read

- 相对路径相对启动时的工作目录，与 shell 工具一致；不接受 `~` 开头（提示改用 shell 工具）。
- 超过 10MB、或前 8KB 里含 NUL 字节（二进制）时拒绝读取。
- 返回格式：首行是「共 N 行，本次返回第 a–b 行」的摘要，之后每行 `<行号>\t<原文>`。
- 输出达到 50KB 时截断，并提示用 `offset=` 续读。

### write

- `content` 是完整文件内容，不会追加；改局部内容要先 `read` 再整份写回。
- 目标是目录时失败；覆盖已有文件时沿用原权限。
- 先写同目录临时文件再 rename：中途出错不会把原文件变成半截。

## 示例

```jsonc
{
  "tools": {
    "bash": { "enable": false },   // 只读场景：不开 shell
    "read": { "enable": true },
    "write": { "enable": false }   // 不需要模型改文件
  }
}
```

模板与仓库根目录的 `tools.example.jsonc` 由测试保证一致。
