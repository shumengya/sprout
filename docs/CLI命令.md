# CLI 命令

在 shell 里调用 `sprout` 时的子命令与选项。`--data-dir`、`-c`、`--resume` 写在 `run` 前后都可以；`--resume` 优先于 `-c`。

## 命令一览

| 命令 | 参数 | 作用 | 备注 |
| --- | --- | --- | --- |
| `sprout` | 无 | 启动 TUI | 默认入口 |
| `sprout` | `--data-dir DIR` | 指定数据目录 | 也可 `--data-dir=DIR`。优先级：该选项 > `SPROUT_DATA_DIR` > `<程序目录>/data/.sprout`。`go run` 须显式指定 |
| `sprout` / `sprout run` | `--effort 档位` | 指定启动时的思考程度，覆盖上次 `/effort` 记下的档位（`settings.jsonc`）和 `models.jsonc` 里的 `effort.default`；本次指定的值不会被记住 | 档位不存在、或当前模型没声明 `effort` 时报错退出（退出码 1），不静默忽略 |
| `sprout` | `-c`、`--continue` | 恢复当前目录最近会话后启动 TUI | 等价 `--resume latest`。失败则提示并新开会话 |
| `sprout` | `--resume ID` | 恢复指定会话后启动 TUI | `latest`、列表序号、或 ID 前缀。失败则提示并新开会话 |
| `sprout run` | 提问 | 单次运行后退出 | 多个词拼成一句；空白则用法错误。答案写 stdout，进度与错误写 stderr |
| `sprout run` | 同上三个选项 | 单次运行并指定数据目录或恢复会话 | 恢复失败则退出，不新开会话 |
| `sprout serve` | `[--data-dir DIR] [-c \| --resume ID] [--effort 档位]` | 在 stdin / stdout 上提供 JSON-RPC 2.0 服务，供其他程序驱动 | 协议见 `docs/rpc.md`。stdout 只走协议消息；恢复会话失败则退出（退出码 1）；stdin 结束或 `shutdown` 后退出 |
| `sprout models update` | `[--data-dir DIR]` | 从 models.dev 下载模型目录，写入数据目录的 `models.dev.json` | 只在执行时联网；启动时不联网。报告本次配置里哪些模型对得上。重启后生效 |
| `sprout update` | `[--repo OWNER/NAME] [--check]` | 查询 GitHub Releases 的最新版本；有新版本时下载本平台产物，校验 `SHA256SUMS.txt`、试运行后替换当前程序，重启生效 | 默认源是 `github.com/shumengya/sprout`，`--repo` 可指向 fork。`--check` 只报告、不下载。程序目录不可写（如 Program Files）时报错，不动原程序。开发构建（`dev`）只报告最新发布，不判版本高低 |
| `sprout` | `--version` | 打印版本 | — |
| `sprout` | `-h`、`--help` | 打印用法 | — |

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 成功（含 `--help`、`--version`、TUI 正常退出、`run` 完成） |
| 1 | 启动失败、配置错误、`run` 失败、`run` 恢复会话失败、TUI 异常退出、`models update` 拉取失败或配置读不出来、`update` 查询失败（网络、限流、仓库没有发布） |
| 2 | 用法错误（未知子命令或选项、缺少参数、`run` 无提问） |
| 130 | `sprout run` / `sprout serve` 被 Ctrl+C 中断 |
