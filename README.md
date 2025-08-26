# 代理检查工具 1.0 / go-proxy-finder 1.0

本代码已在Ubuntu 22.04 上测试并运行成功, 如要其他LINUX 系统上运行, 可以自行测试.

配置要求: 8核 8G 内存 10M带宽 Ubuntu 22.04

建议在一台全新云主机上运行以防止软件跟老系统冲突,可以到以下网址线上即时开通云主机

https://www.say.cc

联络作者 https://t.me/hongkongisp

一个高性能的代理发现和测试工具，支持HTTP、HTTPS、SOCKS4和SOCKS5等多种代理类型，可用于扫描和验证代理服务器的有效性。

## 功能特点

- 两种工作模式：代理发现模式和代理测试模式
- 支持多种代理类型：HTTP、HTTPS、SOCKS4、SOCKS5
- 自定义IP范围扫描或IP列表文件扫描
- 可指定端口范围进行扫描
- 高并发设计，扫描效率高
- 详细的进度显示，进度信息自动换行，提高可读性
- 自动检测并避免暴露本机IP的代理
- 结果自动去重并可保存到文件

## 环境要求

- Ubuntu 22.04 LTS
- Go 1.18+

## Ubuntu 22.04 安装步骤

### 1. 安装Go环境

```bash
# 更新系统包
sudo apt update && sudo apt upgrade -y

# 安装Go
sudo apt install -y golang-go

# 验证安装
go version
```

### 2. 获取代码

```bash
# 克隆代码仓库
git clone https://github.com/saycc1982/go-proxy-finder.git
cd go-proxy-finder
```

## 使用方法

### 基本用法

```bash
# 查看帮助信息
go run main.go -h

# 发现模式 - 扫描指定IP范围
go run main.go -m s -range 192.168.1.0/24 -p 200

# 发现模式 - 从文件读取IP列表
go run main.go -m s -file ips.txt -type http,https

# 测试模式 - 测试代理列表文件
go run main.go -testfile proxies.txt -v
```

### 参数说明

```
代理检查系统
用法:
  发现模式: proxy_checker -m s [-range IP范围] [-file IP文件] [-type 代理类型] [-ports 端口列表] [其他参数]
  测试模式: proxy_checker -testfile 代理文件 [其他参数]
  调试模式: 增加 -v 参数查看详细日志

注意:
  1. 本版本强制使用 /tmp 作为临时文件目录，-tempdir 参数已失效
  2. 高并发时请确保系统文件描述符限制足够高
  3. 如遇文件句柄错误，可临时提高系统限制: ulimit -n 65535

参数:
  -content string
        内容验证关键字
  -file string
        IP列表文件路径
  -h    显示帮助信息
  -header
        打印HTTP响应头
  -ignoressl
        忽略SSL证书错误
  -m string
        模式: s(发现模式)
  -no-color
        禁用颜色输出
  -p int
        并发数 (default 200)
  -ports string
        要扫描的端口，逗号分隔，如 80,8080（不输入则使用预设端口）
  -range string
        IP范围，如 0.0.0.0-255.255.255.0 或 0.0.0.0/21
  -retry int
        重试次数 (default 2)
  -tempdir string
        临时文件目录（本版本强制使用/tmp，此参数无效）
  -testfile string
        代理测试文件路径
  -t int
        超时时间(秒) (default 10)
  -type string
        代理类型，逗号分隔 (default "http,https,socks4,socks5")
  -url string
        测试URL (default "https://httpbin.org/ip")
  -v    详细模式（启用调试日志）
```

## 高级用法

### 1. 自定义端口扫描

```bash
# 只扫描80和8080端口
go run main.go -m s -range 192.168.1.0/24 -ports 80,8080
```

### 2. 内容验证

```bash
# 只保留能访问特定内容的代理
go run main.go -testfile proxies.txt -content "example" -url https://example.com
```

### 3. 提高并发数

```bash
# 提高系统文件描述符限制
ulimit -n 65535

# 使用500并发
go run main.go -m s -range 10.0.0.0/16 -p 500
```

## 注意事项

1. 大规模扫描可能会触发目标网络的安全机制，请确保你有合法授权
2. 高并发扫描会消耗较多系统资源，请根据服务器性能调整并发数
3. 扫描结果会保存在当前目录下，文件名格式为`proxy_时间戳.txt`
4. 程序支持中断后恢复，中断时会提示是否保存已发现的有效代理

## 许可证

本项目采用MIT许可证，详情参见LICENSE文件。
