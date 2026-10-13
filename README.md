# BoltViewer

本地运行的 BoltDB / bbolt 数据库查看器。Go 单文件程序自带中文网页界面，无需安装前端依赖，浏览器资源不依赖网络。

GitHub Actions 会为 `main` 的提交构建 Windows、Linux 和 macOS 版本。进入仓库的 **Actions → Build and test**，在成功的运行记录中下载对应系统的 `BoltViewer-*` 构建产物，解压后运行。

## 运行

Windows 下双击 `boltdbviewer.exe`。默认打开当前目录的 `database.db`，并在浏览器访问 <http://127.0.0.1:8080>。默认端口被占用时自动选择空闲端口，实际地址会显示在控制台并自动打开。如果没有找到文件，可在界面中选择数据库或输入绝对路径。

```powershell
.\boltdbviewer.exe -db "E:\data\my.db"
.\boltdbviewer.exe -db "E:\data\my.db" -addr 127.0.0.1:8090 -open=false
```

在控制台按 `Ctrl+C` 退出。端口已被占用时，用 `-addr` 指定其他本地端口。默认上传上限为 512 MiB，可用 `-max-upload-mb` 调整；使用本地路径打开不受上传大小限制。程序仅允许监听本机地址。

## 功能

- 按层级展开 bucket，查看子 bucket 和键值记录。
- 键名不区分大小写搜索，也可搜索原始键的十六进制片段；每页显示 100 条。
- 文本、JSON、HEX、Base64 四种视图。JSON 格式化保留大整数的原始数字。
- 显示文件大小、页面大小、当前 bucket 的直接记录数、子 bucket 数及 Sequence。
- 复制键和值、下载完整原始值，递归导出当前 bucket 的全部记录。
- 通过路径打开文件，或选择/拖入文件打开上传的本地副本。
- 快捷键：`Ctrl+K` 搜索、`Ctrl+O` 打开；列表支持 Enter，格式标签支持方向键。

数据库始终使用 bbolt 的 `ReadOnly` 模式打开，程序不创建或修改原始文件。上传文件会保存为系统临时目录中的副本，切换数据库或正常退出时删除。其他程序持有写锁时，路径打开可能失败；可关闭写入程序后重试，或上传一份一致性备份。界面不会自动刷新，可点击“刷新”重新读取。

单个值的服务端预览最多 1 MiB，界面内容最多显示 64 KiB；“下载原始值”和 bucket 导出包含完整数据。搜索只查键，不搜索值；记录按 BoltDB 原始字节顺序排列。当前 bucket 的计数和搜索需要扫描其直接键列表，大型 bucket 可能需要更长时间。

## 导出格式

`bucket-export.json` 使用 Base64 保存所有键和值，包括二进制数据。子 bucket 通过 `entries` 递归表示。空值为 `"value_base64": ""`。

```json
{
  "format": "boltdb-viewer/v1",
  "entries": [
    { "key_base64": "a2V5", "value_base64": "dmFsdWU=" },
    { "key_base64": "bmVzdGVk", "entries": [] }
  ]
}
```

## 从源码构建

需要 Go 1.23 或更新版本。

```powershell
go mod download
go test ./...
go vet ./...
go build -trimpath -ldflags="-s -w" -o boltdbviewer.exe .
```

如果默认 Go 模块代理无法访问，可在当前 PowerShell 会话设置：

```powershell
$env:GOPROXY = "https://goproxy.cn,direct"
```

Linux / macOS 使用 `go build -o boltdbviewer .` 后运行 `./boltdbviewer -db /path/to/database.db`。

`main_test.go` 验证嵌套和二进制 bucket、搜索分页、空值、大值、无损导出、只读访问、打开失败保留旧数据库、上传副本清理与请求来源检查。

JSON 格式化的独立检查可运行 `node tools/frontend-test.cjs`。浏览器交互检查见 `tools/smoke-ui.cjs`，需要 Playwright 和一个已打开数据库的本地查看器；使用 `VIEWER_URL` 指定地址、`BROWSER_EXE` 指定浏览器可执行文件。数据库文件、编译产物和测试截图均已加入 `.gitignore`。
