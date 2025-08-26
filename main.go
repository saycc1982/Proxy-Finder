// 作者 ED, Telegram 联络 https://t.me/hongkongisp
//服务器赞助 顺安云 https://say.cc
//线上即时开通云主机, 请到顺安云
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 颜色控制码（兼容终端）
const (
	ColorReset    = "\033[0m"
	ColorGreen    = "\033[32m"
	ColorYellow   = "\033[33m"
	ColorLightRed = "\033[91m"
	ColorCyan     = "\033[36m"
	ColorGray     = "\033[37m"
)

// 颜色语义定义
const (
	ColorSuccess = ColorGreen    // 成功信息
	ColorWarn    = ColorYellow   // 警告/进度
	ColorError   = ColorLightRed // 错误信息
	ColorInfo    = ColorCyan     // 提示/表头
	ColorDebug   = ColorGray     // 调试信息
)

// 终端控制符
const (
	ClearLine    = "\033[2K"  // 清除当前行
	CursorHome   = "\r"       // 光标移动到行首
	CursorUp     = "\033[1A"  // 光标上移一行
	CursorDown   = "\033[1B"  // 光标下移一行
	CursorBottom = "\033[999B" // 光标移动到最底部
)

// 所有支持的代理类型
var AllProxyTypes = []string{"http", "https", "socks4", "socks5"}

// httpbin.org/ip 返回的JSON结构
type IPResponse struct {
	Origin string `json:"origin"`
}

// 代理信息结构
type Proxy struct {
	Type string
	IP   string
	Port int
}

// 配置结构
type Config struct {
	Mode         string   // 模式: s(发现) 或 t(测试)
	Range        string   // IP范围
	File         string   // IP列表文件
	TestFile     string   // 代理测试文件
	Types        []string // 代理类型
	Verbose      bool     // 详细模式
	Timeout      int      // 代理测试超时(秒)
	Header       bool     // 打印HTTP头
	URL          string   // 测试URL
	IgnoreSSL    bool     // 忽略SSL证书
	Concurrency  int      // 并发数
	Help         bool     // 帮助标志
	ContentCheck string   // 内容验证关键字
	RetryCount   int      // 重试次数
	NoColor      bool     // 禁用颜色输出
	TempDir      string   // 临时文件目录（强制为/tmp）
	Ports        []int    // 自定义端口列表
}

// 临时文件管理器
type TempFileManager struct {
	proxyTempPath  string
	proxyFile      *os.File
	proxyWriter    *bufio.Writer
	resultTempPath string
	resultFile     *os.File
	resultWriter   *bufio.Writer
	resultCache    []string // 缓存结果文件内容，减少文件打开次数
	mutex          sync.Mutex
	cleanedUp      bool
}

var (
	config      Config
	foundProxies = struct {
		sync.RWMutex
		count int           // 有效代理计数
		list  []string      // 内存缓存有效代理列表（双保险）
	}{}
	resultFileName string // 最终结果文件名
	isSaved        bool   // 记录是否实际保存了文件

	wg         sync.WaitGroup
	workerPool chan struct{} // 协程池

	// 进度跟踪变量
	totalTasks     int64
	completedTasks int64
	startTime      time.Time
	tasksMutex     sync.Mutex
	progressTicker *time.Ticker
	terminalHeight int
	progressLine   int    // 进度条所在行号
	outputLines    int    // 已输出的行数
	lastProgress   string // 上一次进度信息

	// 输出同步锁
	outputMutex sync.Mutex

	// 本机IP相关
	localIP       string
	ipFetchFailed bool

	// 临时文件管理器实例
	tempMgr *TempFileManager

	// 询问状态跟踪
	promptStatus struct {
		sync.Mutex
		hasPrompted bool
	}

	// 强制临时文件路径为/tmp
	forceTempDir = "/tmp"
	
	// 清理状态控制
	cleanupMutex sync.Mutex
	isCleaning   bool
)

func main() {
	// 解析命令行参数（提前解析以便检查-h）
	parseFlags()

	// 如果用户输入了-h，直接显示帮助信息并退出，不执行其他操作
	if config.Help {
		printHelp()
		return
	}

	// 检查系统文件描述符限制并给出建议
	checkFileDescriptorLimit()

	// 注册退出时清理临时文件的函数
	defer func() {
		// 确保在程序任何退出路径都清理临时文件
		cleanupTemporaryFiles()
	}()

	// 初始化临时文件管理器（强制使用/tmp）
	initTempFileManager()

	// 初始化终端配置
	initTerminal()
	terminalHeight = getTerminalHeight()
	progressLine = terminalHeight - 1 // 进度条默认显示在终端倒数第二行
	outputLines = 0

	// 调试日志：打印当前模式和临时文件路径
	printDebug(fmt.Sprintf("当前运行模式: %s，测试文件: %s", config.Mode, config.TestFile))
	printDebug(fmt.Sprintf("强制临时文件目录: %s", forceTempDir))
	printDebug(fmt.Sprintf("代理临时文件: %s", tempMgr.proxyTempPath))
	printDebug(fmt.Sprintf("结果临时文件: %s", tempMgr.resultTempPath))

	// 获取本机IP（递进式尝试，两次超时）
	localIP, ipFetchFailed = getLocalIP()
	handleIPFetchResult()

	// 验证参数有效性
	if err := validateConfig(); err != nil {
		printError(fmt.Sprintf("参数错误: %v", err))
		printHelp()
		os.Exit(1)
	}

	// 检查高并发场景下的系统限制
	if config.Concurrency > 500 {
		if err := checkHighConcurrencySystemLimits(); err != nil {
			printError(fmt.Sprintf("高并发检查失败: %v", err))
			os.Exit(1)
		}
	}

	// 设置信号处理
	setupSignalHandler()

	// 初始化协程池
	workerPool = make(chan struct{}, config.Concurrency)
	printDebug(fmt.Sprintf("协程池初始化完成，并发数: %d", config.Concurrency))

	// 初始化结果文件名
	initResultFileName()

	// 记录任务开始时间
	startTime = time.Now()

	// 显示当前配置信息
	printConfigInfo()

	// 启动进度显示计时器，提高频率到1秒一次
	startProgressTracker()

	// 重置保存状态
	isSaved = false

	// 根据模式执行核心逻辑
	switch config.Mode {
	case "s":
		runDiscoverMode()
	case "t":
		runTestMode()
	default:
		printError("无效的模式，请使用 -m s 或 -testfile")
		printHelp()
		os.Exit(1)
	}

	// 等待所有任务完成
	printDebug("等待所有任务完成...")
	wg.Wait()
	printDebug("所有任务已完成")

	// 强制刷新临时结果文件缓冲区+缓存结果到内存
	if err := tempMgr.FlushAllWriters(); err != nil {
		printError(fmt.Sprintf("刷新临时文件缓冲区失败: %v", err))
	} else {
		// 将结果缓存到内存，避免后续读取文件
		if err := tempMgr.cacheResults(); err != nil {
			printWarn(fmt.Sprintf("缓存结果到内存失败: %v", err))
		}
		printDebug("已强制刷新所有临时文件缓冲区并缓存结果")
	}

	// 停止进度计时器并显示最终进度
	stopProgressTracker()
	printFinalProgress()

	// 测试模式询问逻辑（优先使用内存列表验证）
	if config.Mode == "t" {
		foundProxies.RLock()
		validCount := foundProxies.count
		validList := foundProxies.list
		foundProxies.RUnlock()
		
		printDebug(fmt.Sprintf("测试模式 - 内存记录有效代理数: %d，临时文件路径: %s", validCount, tempMgr.resultTempPath))
		
		// 双保险：即使临时文件读取失败，也能通过内存列表判断
		if validCount > 0 || len(validList) > 0 {
			promptStatus.Lock()
			if !promptStatus.hasPrompted {
				printDebug("进入测试模式询问流程（基于内存记录）")
				// 优先使用内存列表，避免临时文件读取问题
				promptAndSaveResultsWithFallback(validList)
				promptStatus.hasPrompted = true
			} else {
				printDebug("已执行过询问流程，跳过")
			}
			promptStatus.Unlock()
		} else {
			printWarn("测试模式未发现有效代理，不生成结果文件")
		}
	} else {
		// 发现模式正常处理
		promptStatus.Lock()
		if !promptStatus.hasPrompted {
			printDebug("进入发现模式询问流程")
			foundProxies.RLock()
			validList := foundProxies.list
			foundProxies.RUnlock()
			promptAndSaveResultsWithFallback(validList)
			promptStatus.hasPrompted = true
		}
		promptStatus.Unlock()
	}

	// 显示任务完成信息
	printCompletionInfo()
}

// 递进式获取本机IP
// 首先尝试 http://myip.ipip.net (3秒超时)
// 失败则尝试 http://checkip.dyndns.org (3秒超时)
// 都失败则返回空和错误状态
func getLocalIP() (string, bool) {
	// 尝试第一个IP获取服务
	printInfo("正在获取本机IP（尝试 1/2: http://myip.ipip.net，3秒超时）")
	ip, err := fetchIPFromService("http://myip.ipip.net", 3*time.Second, parseIPIPNetResponse)
	if err == nil && ip != "" {
		return ip, false
	}
	printWarn(fmt.Sprintf("第一个IP服务失败: %v", err))

	// 尝试第二个IP获取服务
	printInfo("正在获取本机IP（尝试 2/2: http://checkip.dyndns.org，3秒超时）")
	ip, err = fetchIPFromService("http://checkip.dyndns.org", 3*time.Second, parseDynDNSResponse)
	if err == nil && ip != "" {
		return ip, false
	}
	printWarn(fmt.Sprintf("第二个IP服务失败: %v", err))

	// 两次尝试都失败
	return "", true
}

// 从指定服务获取IP
func fetchIPFromService(url string, timeout time.Duration, parser func(string) string) (string, error) {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: config.IgnoreSSL,
			},
			DialContext: (&net.Dialer{
				Timeout:   timeout / 2,
				KeepAlive: 0,
			}).DialContext,
			TLSHandshakeTimeout: timeout / 2,
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("响应状态码非200: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %v", err)
	}

	ip := parser(string(body))
	if ip == "" {
		return "", fmt.Errorf("无法从响应中解析IP")
	}

	return ip, nil
}

// 解析 myip.ipip.net 的响应
// 响应格式示例: "当前 IP：1.2.3.4  来自：xx省xx市 电信"
func parseIPIPNetResponse(body string) string {
	// 使用正则表达式提取IP地址
	re := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	matches := re.FindStringSubmatch(body)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// 解析 checkip.dyndns.org 的响应
// 响应格式示例: "<html><head><title>Current IP Check</title></head><body>Current IP Address: 1.2.3.4</body></html>"
func parseDynDNSResponse(body string) string {
	// 使用正则表达式提取IP地址
	re := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	matches := re.FindStringSubmatch(body)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// 处理IP获取结果
func handleIPFetchResult() {
	if ipFetchFailed {
		// 两次尝试都失败：切换URL为百度
		config.URL = "https://www.baidu.com"
		printWarn(fmt.Sprintf("已自动切换测试URL为: %s", config.URL))
		printWarn("注意：由于无法获取本机IP，将不检查代理是否暴露本机IP")
	} else {
		printInfo(fmt.Sprintf("本机真实IP: %s", localIP))
	}
}

// 检查系统文件描述符限制并给出建议
func checkFileDescriptorLimit() {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return // 只在类Unix系统检查
	}

	var rlimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlimit); err != nil {
		printWarn(fmt.Sprintf("无法检查文件描述符限制: %v", err))
		return
	}

	// 如果当前限制低于2048，给出提示
	if rlimit.Cur < 2048 {
		printWarn(fmt.Sprintf("警告：系统文件描述符限制较低(%d)，可能导致'打开文件过多'错误", rlimit.Cur))
		printWarn("建议临时提高限制：ulimit -n 4096")
		printWarn("或永久修改：在/etc/security/limits.conf中添加")
		printWarn("* soft nofile 4096")
		printWarn("* hard nofile 8192")
		time.Sleep(3 * time.Second) // 给用户时间阅读提示
	}
}

// 检查高并发场景下的系统限制
func checkHighConcurrencySystemLimits() error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		printWarn("警告：在非类Unix系统上使用高并发可能导致不可预期的问题")
		return nil
	}

	var rlimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlimit); err != nil {
		return fmt.Errorf("无法检查文件描述符限制: %v", err)
	}

	// 如果系统限制是1024且并发数超过500，提示用户修改
	if rlimit.Cur == 1024 {
		return fmt.Errorf(
			"检测到系统文件描述符限制为默认值1024，无法支持高并发(%d)\n"+
			"请先在终端执行以下命令提高限制，然后重新运行程序:\n"+
			"  ulimit -n 65535", config.Concurrency)
	}

	// 如果系统限制仍然不足，给出警告
	if rlimit.Cur < uint64(config.Concurrency*2) {
		printWarn(fmt.Sprintf(
			"警告：系统文件描述符限制(%d)可能不足以支持指定的并发数(%d)\n"+
			"建议提高限制以避免'打开文件过多'错误: ulimit -n %d",
			rlimit.Cur, config.Concurrency, config.Concurrency*2))
		time.Sleep(5 * time.Second) // 给用户时间阅读警告
	}

	return nil
}

// 初始化临时文件管理器（强制使用/tmp目录）
func initTempFileManager() {
	// 1. 验证/tmp目录是否存在且可写
	if err := checkDirWritable(forceTempDir); err != nil {
		fmt.Printf("错误：强制临时目录 /tmp 不可用（%v），程序无法运行\n", err)
		os.Exit(1)
	}

	// 2. 创建代理列表临时文件（带唯一前缀）
	proxyTemp, err := os.CreateTemp(forceTempDir, "proxy_checker_list_*.tmp")
	if err != nil {
		fmt.Printf("创建代理临时文件失败（路径: %s）: %v\n", forceTempDir, err)
		os.Exit(1)
	}

	// 3. 创建结果临时文件（带唯一前缀）
	resultTemp, err := os.CreateTemp(forceTempDir, "proxy_checker_result_*.tmp")
	if err != nil {
		proxyTemp.Close()
		os.Remove(proxyTemp.Name())
		fmt.Printf("创建结果临时文件失败（路径: %s）: %v\n", forceTempDir, err)
		os.Exit(1)
	}

	// 4. 设置临时文件权限（仅当前用户可读写）
	if err := setFilePerm(proxyTemp.Name(), 0600); err != nil {
		printWarn(fmt.Sprintf("警告：无法设置代理临时文件权限（%s）: %v", proxyTemp.Name(), err))
	}
	if err := setFilePerm(resultTemp.Name(), 0600); err != nil {
		printWarn(fmt.Sprintf("警告：无法设置结果临时文件权限（%s）: %v", resultTemp.Name(), err))
	}

	// 5. 初始化临时文件管理器
	tempMgr = &TempFileManager{
		proxyTempPath:  proxyTemp.Name(),
		proxyFile:      proxyTemp,
		proxyWriter:    bufio.NewWriter(proxyTemp),
		resultTempPath: resultTemp.Name(),
		resultFile:     resultTemp,
		resultWriter:   bufio.NewWriter(resultTemp),
		resultCache:    []string{},
		cleanedUp:      false,
	}
}

// 辅助函数：检查目录是否可写
func checkDirWritable(dir string) error {
	// 尝试在目录下创建临时文件来验证可写性
	testFile := filepath.Join(dir, "proxy_checker_test_write.tmp")
	file, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("创建测试文件失败: %v", err)
	}
	// 立即清理测试文件
	defer func() {
		file.Close()
		os.Remove(testFile)
	}()
	return nil
}

// 辅助函数：设置文件权限
func setFilePerm(path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}

// 获取终端高度
func getTerminalHeight() int {
	cmd := exec.Command("tput", "lines")
	output, err := cmd.Output()
	if err != nil {
		return 24 // 默认值
	}
	height, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 24 // 默认值
	}
	return height
}

// 初始化终端支持
func initTerminal() {
	if runtime.GOOS == "linux" {
		if os.Getenv("TERM") == "" {
			os.Setenv("TERM", "xterm-256color")
		}
	}
}

// 带颜色的打印函数 - 优化版本，处理进度条位置
func printSuccess(msg string) {
	outputMutex.Lock()
	defer outputMutex.Unlock()
	
	// 保存当前进度行内容
	saveProgressLine()
	
	// 移动光标到当前输出行
	moveToOutputLine()
	
	if config.NoColor {
		fmt.Println(msg)
	} else {
		fmt.Printf("%s%s%s\n", ColorSuccess, msg, ColorReset)
	}
	
	outputLines++
	adjustProgressLine()
	
	// 恢复进度条
	restoreProgressLine()
}

func printWarn(msg string) {
	outputMutex.Lock()
	defer outputMutex.Unlock()
	
	// 保存当前进度行内容
	saveProgressLine()
	
	// 移动光标到当前输出行
	moveToOutputLine()
	
	if config.NoColor {
		fmt.Println(msg)
	} else {
		fmt.Printf("%s%s%s\n", ColorWarn, msg, ColorReset)
	}
	
	outputLines++
	adjustProgressLine()
	
	// 恢复进度条
	restoreProgressLine()
}

func printError(msg string) {
	outputMutex.Lock()
	defer outputMutex.Unlock()
	
	// 保存当前进度行内容
	saveProgressLine()
	
	// 移动光标到当前输出行
	moveToOutputLine()
	
	if config.NoColor {
		fmt.Println(msg)
	} else {
		fmt.Printf("%s%s%s\n", ColorError, msg, ColorReset)
	}
	
	outputLines++
	adjustProgressLine()
	
	// 恢复进度条
	restoreProgressLine()
}

func printInfo(msg string) {
	outputMutex.Lock()
	defer outputMutex.Unlock()
	
	// 保存当前进度行内容
	saveProgressLine()
	
	// 移动光标到当前输出行
	moveToOutputLine()
	
	if config.NoColor {
		fmt.Println(msg)
	} else {
		fmt.Printf("%s%s%s\n", ColorInfo, msg, ColorReset)
	}
	
	outputLines++
	adjustProgressLine()
	
	// 恢复进度条
	restoreProgressLine()
}

func printDebug(msg string) {
	if !config.Verbose {
		return
	}
	outputMutex.Lock()
	defer outputMutex.Unlock()
	
	// 保存当前进度行内容
	saveProgressLine()
	
	// 移动光标到当前输出行
	moveToOutputLine()
	
	if config.NoColor {
		fmt.Println("[DEBUG] " + msg)
	} else {
		fmt.Printf("%s[DEBUG] %s%s\n", ColorDebug, msg, ColorReset)
	}
	
	outputLines++
	adjustProgressLine()
	
	// 恢复进度条
	restoreProgressLine()
}

// 移动光标到当前输出行
func moveToOutputLine() {
	if outputLines > 0 {
		fmt.Printf("\033[%dE", 1) // 下移一行
	}
}

// 保存当前进度行内容
func saveProgressLine() {
	// 仅在有进度信息时保存
	if lastProgress != "" {
		// 移动到进度行
		moveToProgressLine()
		// 保存当前行内容
		lastProgress = getCurrentLineContent()
	}
}

// 恢复进度条
func restoreProgressLine() {
	if lastProgress != "" {
		moveToProgressLine()
		fmt.Print(ClearLine)
		fmt.Print(lastProgress)
		// 移动回输出行
		moveToOutputLine()
	}
}

// 获取当前行内容（简化实现）
func getCurrentLineContent() string {
	return lastProgress
}

// 移动到进度条所在行
func moveToProgressLine() {
	// 计算需要移动的行数
	linesToMove := outputLines - progressLine
	if linesToMove > 0 {
		fmt.Printf("\033[%dA", linesToMove) // 上移
	} else if linesToMove < 0 {
		fmt.Printf("\033[%dB", -linesToMove) // 下移
	}
}

// 调整进度条位置
func adjustProgressLine() {
	// 确保进度条始终在终端底部附近
	if outputLines >= terminalHeight-2 {
		progressLine = outputLines + 1
	}
}

// 进度条打印函数 - 改进版，在进度文字头尾添加换行
func printProgress() {
	tasksMutex.Lock()
	defer tasksMutex.Unlock()

	if totalTasks == 0 || completedTasks == 0 {
		return
	}

	remaining := totalTasks - completedTasks
	percent := float64(completedTasks) / float64(totalTasks) * 100

	elapsed := time.Since(startTime)
	estimatedTotal := elapsed * time.Duration(totalTasks) / time.Duration(completedTasks)
	remainingTime := estimatedTotal - elapsed

	var timeStr string
	if remainingTime >= 24*time.Hour {
		days := int(remainingTime.Hours() / 24)
		hours := int(remainingTime.Hours()) % 24
		minutes := int(remainingTime.Minutes()) % 60
		timeStr = fmt.Sprintf("%d天%d时%d分", days, hours, minutes)
	} else {
		hours := int(remainingTime.Hours())
		minutes := int(remainingTime.Minutes()) % 60
		seconds := int(remainingTime.Seconds()) % 60
		timeStr = fmt.Sprintf("%d时%d分%d秒", hours, minutes, seconds)
	}

	foundProxies.RLock()
	validCount := foundProxies.count
	foundProxies.RUnlock()

	ipDisplay := localIP
	if ipDisplay == "" {
		ipDisplay = "无法获取"
	}

	// 进度文字头部和尾部添加换行，增强可读性
	progress := fmt.Sprintf("\n进度: 总任务数: %d, 已完成: %d, 剩余: %d, 完成率: %.2f%%, 有效代理: %d, 预计剩余时间: %s\n",
		totalTasks, completedTasks, remaining, percent, validCount, timeStr)

	outputMutex.Lock()
	defer outputMutex.Unlock()

	// 保存进度信息
	lastProgress = progress
	
	// 移动到进度行
	moveToProgressLine()
	
	// 清除当前行并打印新进度
	fmt.Print(ClearLine)
	if config.NoColor {
		fmt.Print(progress)
	} else {
		fmt.Printf("%s%s%s", ColorWarn, progress, ColorReset)
	}
	
	// 移动回输出行
	moveToOutputLine()
}

// 打印最终进度
func printFinalProgress() {
	printProgress()
	// 确保进度条后有一个换行
	outputMutex.Lock()
	defer outputMutex.Unlock()
	fmt.Println()
}

// 解析命令行参数
func parseFlags() {
	mode := flag.String("m", "", "模式: s(发现模式)")
	rangeFlag := flag.String("range", "", "IP范围，如 0.0.0.0-255.255.255.0 或 0.0.0.0/21")
	file := flag.String("file", "", "IP列表文件路径")
	testFile := flag.String("testfile", "", "代理测试文件路径")
	proxyType := flag.String("type", strings.Join(AllProxyTypes, ","), "代理类型，逗号分隔")
	verbose := flag.Bool("v", false, "详细模式（启用调试日志）")
	timeout := flag.Int("t", 10, "超时时间(秒)")
	retry := flag.Int("retry", 2, "重试次数")
	header := flag.Bool("header", false, "打印HTTP响应头")
	urlFlag := flag.String("url", "https://httpbin.org/ip", "测试URL")
	contentCheck := flag.String("content", "", "内容验证关键字")
	ignoreSSL := flag.Bool("ignoressl", false, "忽略SSL证书错误")
	concurrency := flag.Int("p", 200, "并发数")
	help := flag.Bool("h", false, "显示帮助信息")
	noColor := flag.Bool("no-color", false, "禁用颜色输出")
	tempDir := flag.String("tempdir", "", "临时文件目录（本版本强制使用/tmp，此参数无效）")
	portsFlag := flag.String("ports", "", "要扫描的端口，逗号分隔，如 80,8080（不输入则使用预设端口）")

	flag.Parse()

	// 提示用户TempDir参数已失效
	if *tempDir != "" {
		printWarn(fmt.Sprintf("警告：-tempdir 参数已失效，本版本强制使用 /tmp 作为临时目录，%s 参数将被忽略", *tempDir))
	}

	modeStr := *mode
	if *testFile != "" {
		modeStr = "t"
	}

	urlStr := *urlFlag
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		urlStr = "https://" + urlStr
	}

	// 解析端口参数
	var ports []int
	if *portsFlag != "" {
		portStrs := strings.Split(*portsFlag, ",")
		for _, portStr := range portStrs {
			portStr = strings.TrimSpace(portStr)
			if portStr == "" {
				continue
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				printError(fmt.Sprintf("无效的端口号: %s，将忽略该端口", portStr))
				continue
			}
			if port < 1 || port > 65535 {
				printError(fmt.Sprintf("端口号必须在1-65535之间: %d，将忽略该端口", port))
				continue
			}
			ports = append(ports, port)
		}
		// 去重端口
		ports = uniqueInts(ports)
	}

	config = Config{
		Mode:         modeStr,
		Range:        *rangeFlag,
		File:         *file,
		TestFile:     *testFile,
		Types:        strings.Split(*proxyType, ","),
		Verbose:      *verbose,
		Timeout:      *timeout,
		RetryCount:   *retry,
		Header:       *header,
		URL:          urlStr,
		IgnoreSSL:    *ignoreSSL,
		Concurrency:  *concurrency,
		Help:         *help,
		ContentCheck: *contentCheck,
		NoColor:      *noColor,
		TempDir:      forceTempDir, // 强制赋值为/tmp
		Ports:        ports,        // 自定义端口列表
	}
}

// 整数去重
func uniqueInts(slice []int) []int {
	seen := make(map[int]bool)
	result := []int{}
	for _, num := range slice {
		if !seen[num] {
			seen[num] = true
			result = append(result, num)
		}
	}
	return result
}

// 验证配置参数（不限制最大并发数）
func validateConfig() error {
	validTypes := map[string]bool{
		"http":   true,
		"https":  true,
		"socks4": true,
		"socks5": true,
	}
	for _, t := range config.Types {
		if !validTypes[t] {
			return fmt.Errorf("无效的代理类型: %s", t)
		}
	}

	if config.Mode == "s" {
		if config.Range == "" && config.File == "" {
			return fmt.Errorf("发现模式必须指定 -range 或 -file")
		}
	} else if config.Mode == "t" {
		if config.TestFile == "" {
			return fmt.Errorf("测试模式必须指定 -testfile")
		}
	} else if config.Mode == "" {
		return fmt.Errorf("必须指定模式 (-m s 或 -testfile)")
	}

	if config.Timeout <= 2 {
		return fmt.Errorf("超时时间必须大于2秒")
	}
	if config.RetryCount < 0 {
		return fmt.Errorf("重试次数不能为负数")
	}
	if config.Concurrency <= 0 {
		return fmt.Errorf("并发数必须大于0")
	}

	// 验证端口有效性（如果用户指定了端口）
	if len(config.Ports) > 0 {
		for _, port := range config.Ports {
			if port < 1 || port > 65535 {
				return fmt.Errorf("无效的端口号: %d（必须在1-65535之间）", port)
			}
		}
	}

	return nil
}

// 打印配置信息
func printConfigInfo() {
	if len(config.Types) == len(AllProxyTypes) {
		printWarn(fmt.Sprintf("使用代理类型: 所有支持的类型（%s）", strings.Join(AllProxyTypes, ", ")))
	} else {
		printWarn(fmt.Sprintf("使用代理类型: %s", strings.Join(config.Types, ", ")))
	}
	
	// 显示端口信息
	if len(config.Ports) > 0 {
		portStrs := make([]string, len(config.Ports))
		for i, port := range config.Ports {
			portStrs[i] = strconv.Itoa(port)
		}
		printWarn(fmt.Sprintf("扫描端口: %s", strings.Join(portStrs, ", ")))
	} else {
		printWarn("使用预设端口进行扫描")
	}
	
	printWarn(fmt.Sprintf("并发数: %d，超时时间: %d秒，重试次数: %d",
		config.Concurrency, config.Timeout, config.RetryCount))
	printWarn(fmt.Sprintf("测试URL: %s", config.URL))
	printWarn(fmt.Sprintf("临时文件目录: %s（不可修改）", forceTempDir))
}

// 打印帮助信息
func printHelp() {
	outputMutex.Lock()
	defer outputMutex.Unlock()

	if config.NoColor {
		fmt.Println("代理检查系统")
	} else {
		fmt.Println(ColorInfo + "代理检查系统" + ColorReset)
	}
	fmt.Println("用法:")
	fmt.Println("  发现模式: proxy_checker -m s [-range IP范围] [-file IP文件] [-type 代理类型] [-ports 端口列表] [其他参数]")
	fmt.Println("  测试模式: proxy_checker -testfile 代理文件 [其他参数]")
	fmt.Println("  调试模式: 增加 -v 参数查看详细日志")
	fmt.Println("\n注意:")
	fmt.Println("  1. 本版本强制使用 /tmp 作为临时文件目录，-tempdir 参数已失效")
	fmt.Println("  2. 高并发时请确保系统文件描述符限制足够高")
	fmt.Println("  3. 如遇文件句柄错误，可临时提高系统限制: ulimit -n 65535")
	fmt.Println("\n参数:")
	flag.PrintDefaults()
}

// 初始化结果文件名
func initResultFileName() {
	timestamp := time.Now().Format("20060102150405")
	var base, ext string

	if config.Mode == "t" && config.TestFile != "" {
		ext = filepath.Ext(config.TestFile)
		base = strings.TrimSuffix(filepath.Base(config.TestFile), ext)
		resultFileName = fmt.Sprintf("%s_%s%s", base, timestamp, ext)
	} else {
		resultFileName = fmt.Sprintf("proxy_%s.txt", timestamp)
	}

	// 确保结果文件不存放在/tmp（避免被系统清理）
	if filepath.Dir(resultFileName) == forceTempDir {
		resultFileName = filepath.Join(".", filepath.Base(resultFileName))
		printDebug(fmt.Sprintf("结果文件路径调整为当前目录: %s（避免/tmp自动清理）", resultFileName))
	}

	count := 1
	for {
		if _, err := os.Stat(resultFileName); os.IsNotExist(err) {
			break
		}
		if config.Mode == "t" && config.TestFile != "" {
			resultFileName = fmt.Sprintf("%s_%s_%d%s", base, timestamp, count, ext)
		} else {
			resultFileName = fmt.Sprintf("proxy_%s_%d.txt", timestamp, count)
		}
		count++
	}
	printDebug(fmt.Sprintf("结果文件将保存为: %s", resultFileName))
}

// 信号处理函数
func setupSignalHandler() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		printWarn("\n接收到中断信号(Ctrl+C)，正在处理结果并清理临时文件...")

		// 立即停止进度计时器，减少资源占用
		stopProgressTracker()
		
		// 强制刷新缓冲区并关闭文件句柄
		if tempMgr != nil {
			tempMgr.FlushAllWriters()
			// 先关闭文件句柄再尝试读取，避免句柄泄露
			tempMgr.closeFiles()
		}

		// 测试模式下询问逻辑（用内存列表双保险）
		if config.Mode == "t" {
			foundProxies.RLock()
			validCount := foundProxies.count
			validList := foundProxies.list
			foundProxies.RUnlock()
			
			if validCount > 0 || len(validList) > 0 {
				promptStatus.Lock()
				if !promptStatus.hasPrompted {
					printDebug("中断信号处理 - 进入测试模式询问流程（基于内存记录）")
					promptAndSaveResultsWithFallback(validList)
					promptStatus.hasPrompted = true
				}
				promptStatus.Unlock()
			} else {
				printWarn("测试模式未发现有效代理，不生成结果文件")
			}
		} else {
			promptStatus.Lock()
			if !promptStatus.hasPrompted {
				printDebug("中断信号处理 - 进入发现模式询问流程")
				foundProxies.RLock()
				validList := foundProxies.list
				foundProxies.RUnlock()
				promptAndSaveResultsWithFallback(validList)
				promptStatus.hasPrompted = true
			}
			promptStatus.Unlock()
		}

		// 执行清理操作并退出
		cleanupTemporaryFiles()
		os.Exit(0)
	}()
}

// 清理临时文件的统一函数
func cleanupTemporaryFiles() {
	cleanupMutex.Lock()
	defer cleanupMutex.Unlock()
	
	// 防止重复清理
	if isCleaning {
		return
	}
	isCleaning = true
	
	// 主动清理临时文件
	if tempMgr != nil {
		if err := tempMgr.Cleanup(); err != nil {
			printError(fmt.Sprintf("清理临时文件失败: %v", err))
		} else {
			printDebug("临时文件已成功清理")
		}
	}
}

// 启动进度跟踪器
func startProgressTracker() {
	// 提高进度更新频率到1秒一次
	progressTicker = time.NewTicker(1 * time.Second)
	go func() {
		for range progressTicker.C {
			printProgress()
		}
	}()
}

// 停止进度跟踪器
func stopProgressTracker() {
	if progressTicker != nil {
		progressTicker.Stop()
	}
}

// 增加已完成任务数
func incrementCompletedTasks() {
	tasksMutex.Lock()
	defer tasksMutex.Unlock()
	completedTasks++
}

// 运行发现模式（修复任务分发问题）
func runDiscoverMode() {
	if err := writeIPToTempFile(); err != nil {
		printError(fmt.Sprintf("处理IP源失败: %v", err))
		os.Exit(1)
	}

	// 读取IP列表（添加详细错误日志）
	ips, err := tempMgr.ReadProxyLine()
	if err != nil {
		printError(fmt.Sprintf("读取临时IP文件失败（路径: %s）: %v", tempMgr.proxyTempPath, err))
		os.Exit(1)
	}
	uniqueIPs := uniqueStrings(ips)
	totalIPs := len(uniqueIPs)

	if totalIPs == 0 {
		printError("未解析到任何IP地址，无法执行扫描")
		os.Exit(1)
	}

	// 计算总任务数：IP数 × 代理类型数 × 端口数（添加验证）
	totalTasksCount := int64(0)
	for _, proxyType := range config.Types {
		ports := getCommonPorts(proxyType)
		if len(ports) == 0 {
			printError(fmt.Sprintf("代理类型 %s 未找到可用端口，无法扫描", proxyType))
			os.Exit(1)
		}
		totalTasksCount += int64(totalIPs * len(ports))
	}

	tasksMutex.Lock()
	totalTasks = totalTasksCount
	tasksMutex.Unlock()

	printWarn(fmt.Sprintf("开始发现代理，共 %d 个IP，%d 种类型，总任务数: %d",
		totalIPs, len(config.Types), totalTasks))

	// 直接分发任务（修复核心问题：移除嵌套goroutine）
	for _, ip := range uniqueIPs {
		for _, proxyType := range config.Types {
			ports := getCommonPorts(proxyType)
			for _, port := range ports {
				wg.Add(1)
				go processProxy(Proxy{
					Type: proxyType,
					IP:   ip,
					Port: port,
				})
			}
		}
	}
}

// 运行测试模式
func runTestMode() {
	if err := writeProxyToTempFile(); err != nil {
		printError(fmt.Sprintf("处理代理文件失败: %v", err))
		os.Exit(1)
	}

	proxyLines, err := tempMgr.ReadProxyLine()
	if err != nil {
		printError(fmt.Sprintf("读取临时代理文件失败（路径: %s）: %v", tempMgr.proxyTempPath, err))
		os.Exit(1)
	}

	var proxies []Proxy
	for _, line := range proxyLines {
		proxy, err := parseProxy(line)
		if err != nil {
			printDebug(fmt.Sprintf("解析代理错误: %v，行: %s", err, line))
			continue
		}
		proxies = append(proxies, proxy)
	}

	tasksMutex.Lock()
	totalTasks = int64(len(proxies))
	tasksMutex.Unlock()

	printWarn(fmt.Sprintf("开始测试代理，共 %d 个代理，总任务数: %d", len(proxies), totalTasks))
	
	// 启动工作协程池处理代理
	for _, proxy := range proxies {
		wg.Add(1)
		go processProxy(proxy)
	}
}

// 处理单个代理检测
func processProxy(proxy Proxy) {
	workerPool <- struct{}{}
	defer func() {
		<-workerPool
		incrementCompletedTasks()
		wg.Done()
	}()

	isValid := false
	var detectedIP string
	for i := 0; i <= config.RetryCount; i++ {
		if i > 0 {
			printDebug(fmt.Sprintf("重试检测代理: %s://%s:%d (第%d次)",
				proxy.Type, proxy.IP, proxy.Port, i+1))
		}

		ipAddr, valid := testProxy(proxy)
		if valid {
			isValid = true
			detectedIP = ipAddr
			break
		}

		if i < config.RetryCount {
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
	}

	if isValid {
		proxyURL := fmt.Sprintf("%s://%s:%d", proxy.Type, proxy.IP, proxy.Port)
		// 写入临时文件并添加到内存列表（双保险）
		addProxyResult(proxyURL)

		// 只有在成功获取本机IP的情况下才检查是否暴露
		if !ipFetchFailed && localIP != "" {
			if detectedIP == localIP {
				printSuccess(fmt.Sprintf("发现有效代理: %s，但该代理会暴露本机IP", proxyURL))
			} else if detectedIP != "" && detectedIP != proxy.IP {
				printWarn(fmt.Sprintf("发现有效代理: %s，返回IP与代理IP不符: %s",
					proxyURL, detectedIP))
			} else {
				printSuccess(fmt.Sprintf("发现有效代理: %s，返回IP: %s", proxyURL, detectedIP))
			}
		} else {
			printSuccess(fmt.Sprintf("发现有效代理: %s", proxyURL))
		}
	}
}

// 将IP列表写入临时文件（添加写入计数和验证）
func writeIPToTempFile() error {
	ipCount := 0 // 记录写入的IP数量

	if config.Range != "" {
		parsedIPs, err := parseIPRange(config.Range)
		if err != nil {
			return fmt.Errorf("解析IP范围: %v", err)
		}
		for _, ip := range parsedIPs {
			if err := tempMgr.WriteProxy(ip); err != nil {
				return fmt.Errorf("写入IP到临时文件（%s）: %v", tempMgr.proxyTempPath, err)
			}
			ipCount++
		}
	}

	if config.File != "" {
		file, err := os.Open(config.File)
		if err != nil {
			return fmt.Errorf("打开IP文件: %v", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if err := tempMgr.WriteProxy(line); err != nil {
				return fmt.Errorf("写入IP到临时文件（%s）: %v", tempMgr.proxyTempPath, err)
			}
			ipCount++
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("读取IP文件: %v", err)
		}
	}

	// 写入后立即刷新
	if err := tempMgr.FlushProxyWriter(); err != nil {
		return err
	}

	printDebug(fmt.Sprintf("成功写入 %d 个IP到临时文件（%s）", ipCount, tempMgr.proxyTempPath))
	if ipCount == 0 {
		return fmt.Errorf("未找到任何可扫描的IP地址")
	}

	return nil
}

// 将代理列表写入临时文件
func writeProxyToTempFile() error {
	file, err := os.Open(config.TestFile)
	if err != nil {
		return fmt.Errorf("打开代理文件: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := tempMgr.WriteProxy(line); err != nil {
			return fmt.Errorf("写入代理到临时文件（%s）: %v", tempMgr.proxyTempPath, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取代理文件: %v", err)
	}

	// 写入后立即刷新
	return tempMgr.FlushProxyWriter()
}

// 解析IP范围
func parseIPRange(rangeStr string) ([]string, error) {
	var ips []string

	if strings.Contains(rangeStr, "/") {
		ip, ipNet, err := net.ParseCIDR(rangeStr)
		if err != nil {
			return nil, err
		}

		for ip := ip.Mask(ipNet.Mask); ipNet.Contains(ip); incIP(ip) {
			ips = append(ips, ip.String())
		}
		return ips, nil
	}

	if strings.Contains(rangeStr, "-") {
		parts := strings.Split(rangeStr, "-")
		if len(parts) != 2 {
			return nil, fmt.Errorf("无效IP范围格式: %s", rangeStr)
		}

		startIP := net.ParseIP(parts[0])
		endIP := net.ParseIP(parts[1])
		if startIP == nil || endIP == nil {
			return nil, fmt.Errorf("无效IP地址: %s", rangeStr)
		}

		startIP = startIP.To4()
		endIP = endIP.To4()
		if startIP == nil || endIP == nil {
			return nil, fmt.Errorf("只支持IPv4地址: %s", rangeStr)
		}

		for ip := startIP; !ipAfter(ip, endIP); incIP(ip) {
			ips = append(ips, ip.String())
		}
		ips = append(ips, endIP.String())
		return ips, nil
	}

	ip := net.ParseIP(rangeStr)
	if ip == nil {
		return nil, fmt.Errorf("无效IP地址: %s", rangeStr)
	}
	return []string{ip.String()}, nil
}

// 解析代理字符串
func parseProxy(proxyStr string) (Proxy, error) {
	parts := strings.SplitN(proxyStr, "://", 2)
	if len(parts) != 2 {
		return Proxy{}, fmt.Errorf("无效代理格式: %s（需带协议头，如 http://ip:port）", proxyStr)
	}

	proxyType := parts[0]
	addr := parts[1]

	ipPort := strings.Split(addr, ":")
	if len(ipPort) != 2 {
		return Proxy{}, fmt.Errorf("无效代理地址: %s（需 ip:port 格式）", addr)
	}

	ip := ipPort[0]
	port, err := strconv.Atoi(ipPort[1])
	if err != nil || port < 1 || port > 65535 {
		return Proxy{}, fmt.Errorf("无效端口号: %s", ipPort[1])
	}

	return Proxy{
		Type: proxyType,
		IP:   ip,
		Port: port,
	}, nil
}

// 获取常见代理端口（添加默认端口兜底）
func getCommonPorts(proxyType string) []int {
	// 如果用户指定了端口，使用用户指定的端口
	if len(config.Ports) > 0 {
		return config.Ports
	}

	// 否则使用预设端口（确保每个类型都有默认端口）
	switch proxyType {
	case "http", "https":
		return []int{80, 81, 3128, 8080, 8081, 8888, 9090}
	case "socks4", "socks5":
		return []int{1080, 1081, 1082, 3128, 8080}
	default:
		// 兜底端口，确保不会返回空列表
		return []int{80, 8080, 1080}
	}
}

// 测试单个代理是否有效
func testProxy(proxy Proxy) (string, bool) {
	proxyURL := fmt.Sprintf("%s://%s:%d", proxy.Type, proxy.IP, proxy.Port)
	printDebug(fmt.Sprintf("测试代理: %s", proxyURL))

	if !isIPReachable(proxy.IP, proxy.Port) {
		printDebug(fmt.Sprintf("IP不可达: %s:%d", proxy.IP, proxy.Port))
		return "", false
	}

	client, err := createProxyClient(proxy)
	if err != nil {
		printDebug(fmt.Sprintf("创建客户端失败: %v，代理: %s", err, proxyURL))
		return "", false
	}

	resp, err := client.Get(config.URL)
	if err != nil {
		printDebug(fmt.Sprintf("代理访问失败: %v，代理: %s", err, proxyURL))
		return "", false
	}
	defer resp.Body.Close()

	// 严格检查状态码必须为200
	if resp.StatusCode != http.StatusOK {
		printDebug(fmt.Sprintf("状态码不符合要求: %d（要求200），代理: %s", resp.StatusCode, proxyURL))
		return "", false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		printDebug(fmt.Sprintf("读取响应失败: %v，代理: %s", err, proxyURL))
		return "", false
	}

	if config.Header {
		printFormattedHeaders(proxyURL, resp)
	}

	// 只有在成功获取本机IP且使用httpbin.org/ip时才解析IP
	var detectedIP string
	if !ipFetchFailed && localIP != "" && config.URL == "https://httpbin.org/ip" {
		var ipResp IPResponse
		if err := json.Unmarshal(body, &ipResp); err == nil {
			detectedIP = ipResp.Origin
			if commaIdx := strings.Index(detectedIP, ","); commaIdx != -1 {
				detectedIP = strings.TrimSpace(detectedIP[:commaIdx])
			}
		}
	}

	if config.ContentCheck != "" && !strings.Contains(string(body), config.ContentCheck) {
		printDebug(fmt.Sprintf("内容验证失败，未找到关键字: %s，代理: %s", config.ContentCheck, proxyURL))
		return "", false
	}

	return detectedIP, true
}

// 格式化打印HTTP响应头
func printFormattedHeaders(proxyURL string, resp *http.Response) {
	outputMutex.Lock()
	defer outputMutex.Unlock()
	
	// 保存当前进度行内容
	saveProgressLine()
	
	// 移动光标到当前输出行
	moveToOutputLine()

	fmt.Println()
	if config.NoColor {
		fmt.Printf("代理 %s 响应头:\n", proxyURL)
	} else {
		fmt.Printf("%s代理 %s 响应头:%s\n", ColorInfo, proxyURL, ColorReset)
	}

	fmt.Printf("HTTP/1.1 %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))

	headers := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		headers = append(headers, k)
	}
	sort.Strings(headers)

	for _, k := range headers {
		if strings.ToLower(k) == "x-forwarded-for" {
			fmt.Printf("%s: %s (可能暴露客户端IP)\n", k, strings.Join(resp.Header[k], ", "))
		} else {
			fmt.Printf("%s: %s\n", k, strings.Join(resp.Header[k], ", "))
		}
	}
	fmt.Println()
	
	outputLines += 3 + len(headers) // 计算新增的行数
	adjustProgressLine()
	
	// 恢复进度条
	restoreProgressLine()
}

// 检查IP:Port是否可达
func isIPReachable(ip string, port int) bool {
	address := fmt.Sprintf("%s:%d", ip, port)
	conn, err := net.DialTimeout("tcp", address, time.Duration(config.Timeout/2)*time.Second)
	if err != nil {
		return false
	}
	conn.Close() // 立即关闭连接，释放句柄
	return true
}

// 创建支持代理的HTTP客户端
func createProxyClient(proxy Proxy) (*http.Client, error) {
	proxyURLStr := fmt.Sprintf("%s://%s:%d", proxy.Type, proxy.IP, proxy.Port)
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: config.IgnoreSSL,
		},
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 0, // 禁用长连接，减少句柄占用
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: time.Duration(config.Timeout) * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxConnsPerHost: 1,    // 限制每个主机连接数
		DisableKeepAlives: true, // 禁用长连接
	}

	if proxy.Type == "http" || proxy.Type == "https" {
		proxyURL, err := url.Parse(proxyURLStr)
		if err != nil {
			return nil, fmt.Errorf("无效代理URL: %v", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	} else {
		transport.Proxy = nil
		transport.Dial = func(network, addr string) (net.Conn, error) {
			return dialSocksProxy(proxy.Type, proxy.IP, proxy.Port, network, addr)
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   time.Duration(config.Timeout) * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // 禁止重定向，减少连接
		},
	}, nil
}

// SOCKS代理拨号
func dialSocksProxy(proxyType, ip string, port int, network, addr string) (net.Conn, error) {
	proxyAddr := fmt.Sprintf("%s:%d", ip, port)
	conn, err := net.DialTimeout(network, proxyAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接代理服务器: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("设置超时: %v", err)
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("解析目标地址: %v", err)
	}
	portNum, err := strconv.Atoi(portStr)
	if err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("无效端口: %v", err)
	}

	if proxyType == "socks4" {
		return socks4Handshake(conn, host, portNum)
	}
	return socks5Handshake(conn, host, portNum)
}

// SOCKS4握手
func socks4Handshake(conn net.Conn, host string, port int) (net.Conn, error) {
	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			conn.Close() // 出错时确保关闭连接
			return nil, fmt.Errorf("解析主机: %v", err)
		}
		ip = ips[0].To4()
		if ip == nil {
			conn.Close() // 出错时确保关闭连接
			return nil, fmt.Errorf("不支持IPv6地址: %s", host)
		}
	} else {
		ip = ip.To4()
		if ip == nil {
			conn.Close() // 出错时确保关闭连接
			return nil, fmt.Errorf("不支持IPv6地址: %s", host)
		}
	}

	req := make([]byte, 9)
	req[0] = 0x04        
	req[1] = 0x01        
	req[2] = byte(port >> 8)
	req[3] = byte(port & 0xff)
	copy(req[4:8], ip[:4])
	req[8] = 0x00        

	if _, err := conn.Write(req); err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("发送SOCKS4请求: %v", err)
	}

	resp := make([]byte, 8)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("读取SOCKS4响应: %v", err)
	}

	if resp[0] != 0x00 || resp[1] != 0x5a {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("SOCKS4代理拒绝，状态码: %d", resp[1])
	}

	conn.SetDeadline(time.Time{})
	return conn, nil
}

// SOCKS5握手
func socks5Handshake(conn net.Conn, host string, port int) (net.Conn, error) {
	req := []byte{0x05, 0x01, 0x00}
	if _, err := conn.Write(req); err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("发送SOCKS5认证请求: %v", err)
	}

	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("读取SOCKS5认证响应: %v", err)
	}

	if resp[0] != 0x05 {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("不支持的SOCKS版本: %d", resp[0])
	}
	if resp[1] != 0x00 {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("需要认证，不支持的认证方法: %d", resp[1])
	}

	var reqBuf []byte
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			reqBuf = make([]byte, 10)
			reqBuf[3] = 0x01 
			copy(reqBuf[4:8], ip4[:4])
		} else if ip6 := ip.To16(); ip6 != nil {
			reqBuf = make([]byte, 22)
			reqBuf[3] = 0x04 
			copy(reqBuf[4:20], ip6[:16])
		} else {
			conn.Close() // 出错时确保关闭连接
			return nil, fmt.Errorf("无效IP地址: %s", host)
		}
	} else {
		if len(host) > 255 {
			conn.Close() // 出错时确保关闭连接
			return nil, fmt.Errorf("域名过长: %s", host)
		}
		reqBuf = make([]byte, 7+len(host))
		reqBuf[3] = 0x03 
		reqBuf[4] = byte(len(host))
		copy(reqBuf[5:5+len(host)], []byte(host))
	}

	reqBuf[0] = 0x05 
	reqBuf[1] = 0x01 
	reqBuf[2] = 0x00 
	reqBuf[len(reqBuf)-2] = byte(port >> 8)
	reqBuf[len(reqBuf)-1] = byte(port & 0xff)

	if _, err := conn.Write(reqBuf); err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("发送SOCKS5连接请求: %v", err)
	}

	respBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, respBuf); err != nil {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("读取SOCKS5连接响应头: %v", err)
	}

	if respBuf[0] != 0x05 {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("不支持的SOCKS版本: %d", respBuf[0])
	}
	if respBuf[1] != 0x00 {
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("SOCKS5连接失败，错误码: %d", respBuf[1])
	}

	var skipLen int
	switch respBuf[3] {
	case 0x01: 
		skipLen = 4 + 2
	case 0x03: 
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			conn.Close() // 出错时确保关闭连接
			return nil, fmt.Errorf("读取域名长度: %v", err)
		}
		skipLen = int(lenBuf[0]) + 2
	case 0x04: 
		skipLen = 16 + 2
	default:
		conn.Close() // 出错时确保关闭连接
		return nil, fmt.Errorf("未知地址类型: %d", respBuf[3])
	}

	if skipLen > 0 {
		skipBuf := make([]byte, skipLen)
		if _, err := io.ReadFull(conn, skipBuf); err != nil {
			conn.Close() // 出错时确保关闭连接
			return nil, fmt.Errorf("读取地址信息: %v", err)
		}
	}

	conn.SetDeadline(time.Time{})
	return conn, nil
}

// 增加IP地址
func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// 检查一个IP是否在另一个IP之后
func ipAfter(ip, after net.IP) bool {
	for i := 0; i < len(ip); i++ {
		if ip[i] < after[i] {
			return false
		} else if ip[i] > after[i] {
			return true
		}
	}
	return false
}

// 字符串去重
func uniqueStrings(slice []string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, str := range slice {
		if !seen[str] {
			seen[str] = true
			result = append(result, str)
		}
	}
	return result
}

// 临时文件管理器：刷新所有缓冲区
func (m *TempFileManager) FlushAllWriters() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	var err1, err2 error
	if m.proxyWriter != nil {
		err1 = m.proxyWriter.Flush()
	}
	if m.resultWriter != nil {
		err2 = m.resultWriter.Flush()
	}

	if err1 != nil {
		return fmt.Errorf("刷新代理临时文件（%s）失败: %v", m.proxyTempPath, err1)
	}
	if err2 != nil {
		return fmt.Errorf("刷新结果临时文件（%s）失败: %v", m.resultTempPath, err2)
	}
	return nil
}

// 临时文件管理器：单独刷新代理文件缓冲区
func (m *TempFileManager) FlushProxyWriter() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.proxyWriter != nil {
		return m.proxyWriter.Flush()
	}
	return nil
}

// 临时文件管理器：单独刷新结果文件缓冲区
func (m *TempFileManager) FlushResultWriter() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.resultWriter != nil {
		return m.resultWriter.Flush()
	}
	return nil
}

// 临时文件管理器：关闭所有文件句柄
func (m *TempFileManager) closeFiles() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.proxyFile != nil {
		m.proxyFile.Close()
		m.proxyFile = nil // 标记为已关闭
	}
	if m.resultFile != nil {
		m.resultFile.Close()
		m.resultFile = nil // 标记为已关闭
	}
}

// 临时文件管理器：写入代理
func (m *TempFileManager) WriteProxy(line string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	_, err := m.proxyWriter.WriteString(line + "\n")
	return err
}

// 临时文件管理器：读取所有行
func (m *TempFileManager) ReadProxyLine() ([]string, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if err := m.proxyWriter.Flush(); err != nil {
		return nil, fmt.Errorf("刷新代理临时文件（%s）失败: %v", m.proxyTempPath, err)
	}

	// 临时打开文件读取，完成后立即关闭（减少句柄占用）
	file, err := os.Open(m.proxyTempPath)
	if err != nil {
		return nil, fmt.Errorf("打开代理临时文件（%s）失败: %v", m.proxyTempPath, err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取代理临时文件（%s）失败: %v", m.proxyTempPath, err)
	}

	return lines, nil
}

// 临时文件管理器：写入结果
func (m *TempFileManager) WriteResult(line string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	
	// 同时写入缓存和文件
	m.resultCache = append(m.resultCache, line)
	_, err := m.resultWriter.WriteString(line + "\n")
	return err
}

// 临时文件管理器：缓存结果到内存（减少文件读取）
func (m *TempFileManager) cacheResults() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// 如果缓存为空，从文件读取并缓存
	if len(m.resultCache) == 0 {
		file, err := os.Open(m.resultTempPath)
		if err != nil {
			return fmt.Errorf("打开结果临时文件（%s）失败: %v", m.resultTempPath, err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				m.resultCache = append(m.resultCache, line)
			}
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("读取结果临时文件（%s）失败: %v", m.resultTempPath, err)
		}
	}
	return nil
}

// 临时文件管理器：清理临时文件
func (m *TempFileManager) Cleanup() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cleanedUp {
		return nil
	}

	// 先关闭文件句柄
	if m.proxyFile != nil {
		m.proxyFile.Close()
		m.proxyFile = nil
	}
	if m.resultFile != nil {
		m.resultFile.Close()
		m.resultFile = nil
	}

	// 删除临时文件
	var err1, err2 error
	if m.proxyTempPath != "" {
		if _, err := os.Stat(m.proxyTempPath); err == nil {
			err1 = os.Remove(m.proxyTempPath)
			if err1 == nil {
				printDebug(fmt.Sprintf("已删除代理临时文件: %s", m.proxyTempPath))
			}
		}
	}
	if m.resultTempPath != "" {
		if _, err := os.Stat(m.resultTempPath); err == nil {
			err2 = os.Remove(m.resultTempPath)
			if err2 == nil {
				printDebug(fmt.Sprintf("已删除结果临时文件: %s", m.resultTempPath))
			}
		}
	}

	m.cleanedUp = true

	// 返回错误（如果有）
	if err1 != nil && !os.IsNotExist(err1) {
		return fmt.Errorf("删除代理临时文件（%s）失败: %v", m.proxyTempPath, err1)
	}
	if err2 != nil && !os.IsNotExist(err2) {
		return fmt.Errorf("删除结果临时文件（%s）失败: %v", m.resultTempPath, err2)
	}
	return nil
}

// 添加有效代理到临时文件和内存列表（双保险）
func addProxyResult(proxyURL string) {
	// 1. 写入临时文件并立即刷新
	if err := tempMgr.WriteResult(proxyURL); err != nil {
		printError(fmt.Sprintf("写入代理结果到临时文件（%s）失败: %v", tempMgr.resultTempPath, err))
	} else {
		// 写入后立即刷新缓冲区
		if err := tempMgr.FlushResultWriter(); err != nil {
			printError(fmt.Sprintf("刷新结果临时文件（%s）失败: %v", tempMgr.resultTempPath, err))
		}
	}

	// 2. 添加到内存列表（双保险）
	foundProxies.Lock()
	foundProxies.count++
	foundProxies.list = append(foundProxies.list, proxyURL)
	currentCount := foundProxies.count
	currentListLen := len(foundProxies.list)
	foundProxies.Unlock()
	
	printDebug(fmt.Sprintf("有效代理记录更新 - 计数: %d，内存列表长度: %d，代理: %s", currentCount, currentListLen, proxyURL))
}

// 带重试机制的临时文件读取（解决too many open files问题）
func readProxiesFromTempFileWithRetry() ([]string, error) {
	// 先尝试从缓存读取
	tempMgr.mutex.Lock()
	if len(tempMgr.resultCache) > 0 {
		defer tempMgr.mutex.Unlock()
		return uniqueStrings(tempMgr.resultCache), nil
	}
	tempMgr.mutex.Unlock()

	// 缓存为空时，尝试从文件读取（带重试）
	var proxies []string
	var err error
	
	// 最多重试3次，每次间隔100ms
	for i := 0; i < 3; i++ {
		proxies, err = readProxiesFromTempFile()
		if err == nil {
			return proxies, nil
		}
		
		// 检查是否是"打开文件过多"错误
		if strings.Contains(err.Error(), "too many open files") {
			printWarn(fmt.Sprintf("文件句柄不足，正在重试（第%d次）...", i+1))
			time.Sleep(100 * time.Millisecond)
			continue
		}
		break // 其他错误不再重试
	}
	
	return nil, err
}

// 从临时文件读取代理
func readProxiesFromTempFile() ([]string, error) {
	// 确保所有数据已写入磁盘
	if err := tempMgr.FlushResultWriter(); err != nil {
		return nil, fmt.Errorf("刷新结果临时文件（%s）失败: %v", tempMgr.resultTempPath, err)
	}

	// 临时打开文件，读取后立即关闭（减少句柄占用）
	file, err := os.Open(tempMgr.resultTempPath)
	if err != nil {
		return nil, fmt.Errorf("打开结果临时文件（%s）失败: %v", tempMgr.resultTempPath, err)
	}
	defer file.Close() // 确保文件会被关闭

	var proxies []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			proxies = append(proxies, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取结果临时文件（%s）失败: %v", tempMgr.resultTempPath, err)
	}

	// 去重并排序
	return uniqueStrings(proxies), nil
}

// 询问用户并保存结果（带内存列表回退机制）
func promptAndSaveResultsWithFallback(memoryList []string) {
	// 1. 尝试从临时文件读取（带重试机制）
	tempProxies, err := readProxiesFromTempFileWithRetry()
	if err != nil {
		printWarn(fmt.Sprintf("读取临时文件（%s）失败: %v，将使用内存记录的代理列表", tempMgr.resultTempPath, err))
		tempProxies = memoryList
	} else {
		// 2. 交叉验证临时文件和内存列表
		if len(tempProxies) != len(memoryList) {
			printWarn(fmt.Sprintf("临时文件（%s）与内存列表数量不一致: 临时文件 %d 个，内存 %d 个，将使用合并去重后的列表", 
				tempMgr.resultTempPath, len(tempProxies), len(memoryList)))
			// 合并去重
			combined := append(tempProxies, memoryList...)
			tempProxies = uniqueStrings(combined)
		}
	}

	if len(tempProxies) == 0 {
		printWarn("未发现有效代理，不生成结果文件")
		return
	}

	// 3. 询问用户是否保存
	outputMutex.Lock()
	fmt.Printf("\n共发现 %d 个有效代理，是否保存到文件 %s? [Y/n] ", len(tempProxies), resultFileName)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	outputMutex.Unlock()

	if answer == "" || answer == "y" || answer == "yes" {
		if err := saveResults(tempProxies); err != nil {
			printError(fmt.Sprintf("保存结果失败: %v", err))
			// 尝试使用内存列表再次保存
			if len(memoryList) > 0 {
				printWarn("尝试使用内存记录再次保存...")
				if err := saveResults(memoryList); err != nil {
					printError(fmt.Sprintf("再次保存失败: %v", err))
					isSaved = false
				} else {
					printSuccess(fmt.Sprintf("已使用内存记录保存 %d 个有效代理到 %s", len(memoryList), resultFileName))
					isSaved = true
				}
			} else {
				isSaved = false
			}
		} else {
			printSuccess(fmt.Sprintf("已成功保存 %d 个有效代理到 %s", len(tempProxies), resultFileName))
			isSaved = true
		}
	} else {
		printInfo("已取消保存结果")
		isSaved = false
	}
}

// 保存结果到文件
func saveResults(proxies []string) error {
	if len(proxies) == 0 {
		return fmt.Errorf("没有有效代理可保存")
	}

	// 创建结果文件目录（如果需要）
	if dir := filepath.Dir(resultFileName); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建结果文件目录失败: %v", err)
		}
	}

	// 临时打开文件，写入后立即关闭
	file, err := os.Create(resultFileName)
	if err != nil {
		return fmt.Errorf("创建结果文件失败: %v", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, proxy := range proxies {
		if _, err := writer.WriteString(proxy + "\n"); err != nil {
			return fmt.Errorf("写入结果文件失败: %v", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("刷新结果文件失败: %v", err)
	}

	// 设置文件权限（仅当前用户可读写）
	if err := os.Chmod(resultFileName, 0600); err != nil {
		printWarn(fmt.Sprintf("警告：无法设置结果文件权限: %v", err))
	}

	return nil
}

// 打印完成信息
func printCompletionInfo() {
	duration := time.Since(startTime)
	foundProxies.RLock()
	validCount := foundProxies.count
	foundProxies.RUnlock()

	printInfo(fmt.Sprintf("\n任务完成，总耗时: %v", duration))
	printInfo(fmt.Sprintf("发现有效代理总数: %d", validCount))
	
	// 只有当文件确实被保存时才显示保存路径
	if isSaved && validCount > 0 && resultFileName != "" {
		printInfo(fmt.Sprintf("有效代理已保存到: %s", resultFileName))
	}
}
    
