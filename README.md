# 上网代理查找工具 1.1v / Proxy-Finder 1.1v

一个高效的代理扫描和测试工具，支持检测 HTTP、HTTPS、SOCKS4 和 SOCKS5 类型的代理，判断其有效性和匿名性，适用于代理发现与批量验证场景。


## 功能特点
| 功能分类 | 具体说明 |
|----------|----------|
| 多类型支持 | 兼容 HTTP、HTTPS、SOCKS4、SOCKS5 四种主流代理类型 |
| 双工作模式 | 发现模式（扫描 IP 范围/文件）、测试模式（验证已有代理列表） |
| 匿名性检测 | 自动判断代理是否暴露本机真实 IP，区分匿名/非匿名代理 |
| 灵活配置 | 可自定义 IP 范围、扫描端口、超时时间、并发数等参数 |
| 批量处理 | 支持大型网段批次扫描，避免内存占用过高 |
| 结果筛选 | 通过内容验证关键字筛选符合业务需求的有效代理 |
| 日志与保存 | 支持详细调试日志、多种保存模式（全部/仅匿名代理） |
| 异常处理 | 完善的信号捕获（Ctrl+C）、重试机制、SSL 忽略选项 |


## 使用教学

### 1. 基本语法
```bash
# 发现模式（扫描新代理）
proxy-finder -m s [选项]

# 测试模式（验证已有代理）
proxy-finder -testfile [代理列表文件路径] [选项]
```

### 2. 核心选项说明
| 选项 | 类型 | 描述 | 示例 |
|------|------|------|------|
| `-m s` | 模式 | 指定为「发现模式」（必需） | `-m s` |
| `-range` | IP 范围 | 扫描的 IP 段（CIDR/起止格式） | `-range 192.168.1.0/24` 或 `-range 10.0.0.1-10.0.0.255` |
| `-file` | 文件路径 | 包含 IP/CIDR 的列表文件（每行一条） | `-file ips.txt` |
| `-testfile` | 文件路径 | 包含代理的列表文件（测试模式必需） | `-testfile proxies.txt` |
| `-type` | 代理类型 | 指定扫描的代理类型（逗号分隔） | `-type http,https,socks5` |
| `-v` | 开关 | 启用详细模式（显示调试日志，不显示进度） | `-v` |
| `-t` | 整数 | 代理测试超时时间（秒），默认 10 秒 | `-t 15` |
| `-p` | 整数 | 并发数，默认 200（高并发需调整系统限制） | `-p 150` |
| `-ports` | 端口列表 | 自定义扫描端口（逗号分隔） | `-ports 8080,3128,1080` |
| `-url` | URL | 自定义测试 URL（默认 https://httpbin.org/ip） | `-url https://www.baidu.com` |
| `-content` | 字符串 | 内容验证关键字（仅保留包含该关键字的响应） | `-content "success"` |
| `-s` | 整数 | 保存模式：0=不保存，1=保存所有有效代理，2=仅保存匿名代理（默认 1） | `-s 2` |
| `-h` | 开关 | 显示帮助信息 | `-h` |

### 3. 常用示例
#### 示例 1：扫描 IP 段的 HTTP/HTTPS 代理
```bash
# 扫描 192.168.1.0/24 网段，仅检测 HTTP/HTTPS 代理，并发 150，超时 12 秒
proxy-finder -m s -range 192.168.1.0/24 -type http,https -p 150 -t 12
```

#### 示例 2：从文件扫描 IP/CIDR 并保存匿名代理
```bash
# 从 ips.txt 读取 IP/CIDR，扫描预设端口，仅保存匿名代理到 data 目录
proxy-finder -m s -file ips.txt -s 2
```

#### 示例 3：测试已有代理列表并启用日志
```bash
# 测试 proxies.txt 中的代理，启用日志，自定义测试 URL 为百度
proxy-finder -testfile proxies.txt -log -url https://www.baidu.com
```

#### 示例 4：详细模式调试
```bash
# 详细模式扫描，显示每步调试信息，忽略 SSL 证书错误
proxy-finder -m s -range 10.0.0.0/24 -v -ignoressl
```


## Ubuntu 22.04 部署与安装

### 前置条件
- Ubuntu 22.04 LTS 系统（64 位）
- 具有 sudo 权限的用户
- 稳定的网络连接（用于安装依赖和测试代理）

### 安装步骤
#### 步骤 1：更新系统并安装依赖
```bash
# 更新系统包索引
sudo apt update && sudo apt upgrade -y

# 安装 Go 语言环境（编译必需）和 Git（可选，用于拉取代码）
sudo apt install -y golang git
```

#### 步骤 2：验证 Go 环境
```bash
go version
```
- 正常输出示例：`go version go1.18.1 linux/amd64`（版本 ≥1.16 即可）

#### 步骤 3：获取代码并编译
```bash
# 方式 1：Git 拉取（推荐，便于更新）
git clone https://github.com/saycc1982/proxy-finder.git  # 替换为实际仓库地址
cd proxy-finder

# 方式 2：手动下载源码（无 Git 时）
# 1. 下载 proxy-finder.go 文件到本地
# 2. 进入文件所在目录

# 编译生成可执行文件
go build -o proxy-finder proxy-finder.go
```

#### 步骤 4：配置系统路径（可选，全局调用）
```bash
# 将程序移动到系统全局路径，支持任意目录调用
sudo mv proxy-finder /usr/local/bin/

# 验证配置
proxy-finder -h  # 正常显示帮助信息即成功
```

#### 步骤 5：创建数据目录（程序自动创建，手动创建可避免权限问题）
```bash
# 创建 data 目录（用于保存结果和日志）
mkdir -p ~/proxy-finder-data
chmod 755 ~/proxy-finder-data

# 后续运行时可指定日志/结果路径到该目录
proxy-finder -m s -range 192.168.1.0/24 -logfile ~/proxy-finder-data/proxy-log.log
```


## 可能遇到的问题及解决方法

### 1. 权限错误（无法创建文件/目录）
#### 错误表现
- 提示 `创建 data 目录失败: permission denied` 或 `无法写入日志文件`
#### 解决方法
```bash
# 方法 1：给当前目录赋予写入权限
chmod 755 .

# 方法 2：指定自定义数据目录（推荐）
mkdir -p ~/proxy-data
proxy-finder -m s -range 192.168.1.0/24 -logfile ~/proxy-data/log.txt -s 1
```

### 2. 网络连接问题（无法访问测试 URL 或目标 IP）
#### 错误表现
- 提示 `请求失败: context deadline exceeded` 或 `响应状态码非 200`
#### 解决方法
```bash
# 1. 检查网络连通性
ping 8.8.8.8  # 测试外网连接
curl https://httpbin.org/ip  # 测试默认测试 URL

# 2. 关闭防火墙（临时测试，生产环境需按需配置规则）
sudo ufw disable

# 3. 更换国内测试 URL（如默认 URL 无法访问）
proxy-finder -m s -range 192.168.1.0/24 -url https://www.baidu.com
```

### 3. 系统资源限制（too many open files）
#### 错误表现
- 高并发时提示 `打开文件过多` 或 `too many open files`
#### 解决方法
```bash
# 方法 1：临时提高文件描述符限制（当前终端生效）
ulimit -n 65535

# 方法 2：永久提高限制（重启后生效）
sudo tee -a /etc/security/limits.conf << EOF
* soft nofile 65535
* hard nofile 65535
EOF

# 方法 3：降低并发数（避免资源耗尽）
proxy-finder -m s -range 192.168.1.0/24 -p 100  # 并发数从默认 200 降至 100
```

### 4. 编译错误（go build 失败）
#### 错误表现
- 提示 `undefined: xxx` 或 `version go1.xx is required`
#### 解决方法
```bash
# 1. 检查 Go 版本（需 ≥1.16）
go version

# 2. 升级 Go 版本（以 1.20.4 为例）
sudo apt remove golang -y
wget https://dl.google.com/go/go1.20.4.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.20.4.linux-amd64.tar.gz

# 3. 配置 Go 环境变量
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# 4. 重新编译
go build -o proxy-finder proxy-finder.go
```

### 5. 无法获取本机 IP（影响匿名性检测）
#### 错误表现
- 提示 `两次 IP 服务失败` 或 `无法检查代理是否暴露本机 IP`
#### 解决方法
```bash
# 1. 手动指定国内可访问的测试 URL（跳过 IP 检测）
proxy-finder -m s -range 192.168.1.0/24 -url https://www.baidu.com

# 2. 检查 IP 服务连通性
curl http://myip.ipip.net  # 测试第一个 IP 服务
curl https://trackip.net/ip  # 测试第二个 IP 服务
```


## 注意事项
1. **合规性**：请确保扫描的 IP 段和代理使用符合当地法律法规，禁止用于未授权的网络探测。
2. **资源占用**：高并发（如 `-p 500`）可能占用大量 CPU/带宽，建议根据服务器配置调整。
3. **代理列表格式**：`-testfile` 支持的代理格式：`http://ip:port`、`socks5://ip:port` 或 `ip:port`（默认 http）。
4. **日志查看**：启用 `-log` 后，日志默认保存到 `data/log_时间戳.log`，可通过 `-logfile` 自定义路径。


## 联系方式
如有功能建议或 BUG 反馈，欢迎联络作者：
- 作者：ED
- 联络：https://t.me/hongkongisp
- 服务器推荐：顺安云 https://www.say.cc
- （香港自营机房，稳定可靠）
