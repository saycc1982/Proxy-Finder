// 作者 ED, Telegram 联络 https://t.me/hongkongisp
//服务器推荐 顺安云 https://say.cc
//线上即开即用云主机, 请到顺安云

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
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

// Windows系统常用浏览器的User-Agent列表
var windowsUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/113.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:102.0) Gecko/20100101 Firefox/102.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:101.0) Gecko/20100101 Firefox/101.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36 Edg/114.0.1823.51",
}

// 初始化随机数生成器
func init() {
	rand.Seed(time.Now().UnixNano())
}

// 获取随机Windows浏览器User-Agent
func getRandomWindowsUserAgent() string {
	index := rand.Intn(len(windowsUserAgents))
	return windowsUserAgents[index]
}

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

// 扩展代理结果结构，包含是否暴露属本机IP信息
type ProxyResult struct {
	URL        string
	ExposesIP  bool
	DetectedIP string
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
	Ports        []int    // 自定义端口列表
	CustomURL    bool     // 是否使用了自定义URL
	LogEnabled   bool     // 是否启用日志
	LogFile      string   // 日志文件路径
	BatchSize    int      // 任务批次大小
	SaveMode     int      // 保存模式: 0(不保存), 1(保存所有), 2(只保存匿名)
}

var (
	config      Config
	foundProxies = struct {
		sync.RWMutex
		list []ProxyResult // 内存缓存有效代理列表
	}{}
	resultFileName string // 最终结果文件名
	isSaved        bool   // 记录是否实际保存了文件

	wg         sync.WaitGroup
	workerPool chan struct{} // 协程池
	shutdown   bool          // 控制是否停止新任务
	outputDisabled bool      // 控制是否禁用普通输出（关键提示不受影响）
	ctx        context.Context
	cancel     context.CancelFunc

	// 进度跟踪变量
	totalTasks     int64
	completedTasks int64
	dispatchedTasks int64  // 已分配的任务数（实时更新）
	startTime      time.Time
	tasksMutex     sync.Mutex
	progressTicker *time.Ticker
	progressStopped bool  // 标记进度是否已停止更新

	// 统一IO锁
	ioMutex     sync.Mutex  // 统一IO锁
	logFile     *os.File
	logWriter   *bufio.Writer // 日志缓冲区
	logFlushTicker *time.Ticker // 日志批量刷新定时器

	// 本机IP相关
	localIP       string
	ipFetchFailed bool

	// 退出状态跟踪
	exitStatus struct {
		sync.Mutex
		exiting bool
	}

	// 全局日志日志路径变量（确保退出时能访问）
	globalLogPath string
)

func main() {
	// 创建可取消的上下文
	ctx, cancel = context.WithCancel(context.Background())
	
	// 解析命令行参数
	parseFlags()

	// 如果用户输入了-h，直接显示帮助信息并退出
	if config.Help {
		printHelp()
		return
	}

	// 首先验证参数有效性 - 关键修改点：参数验证提前
	if err := validateConfig(); err != nil {
		// 先打印帮助菜单，再打印错误原因
		printHelp()
		printError(fmt.Sprintf("错误: 参数错误: %v\n", err))
		os.Exit(1)
	}

	// 确保data目录存在
	if err := ensureDataDir(); err != nil {
		// 先打印帮助菜单，再打印错误原因
		printHelp()
		printError(fmt.Sprintf("错误: 创建data目录失败: %v\n", err))
		os.Exit(1)
	}

	// 初始化日志（如果启用）
	if config.LogEnabled {
		// 初始化日志文件名（确保在data目录下）
		if config.LogFile == "" {
			timestamp := time.Now().Format("20060102150405")
			config.LogFile = fmt.Sprintf("data/log_%s.log", timestamp)
		} else {
			// 确保日志文件在data目录下
			if !strings.HasPrefix(config.LogFile, "data/") {
				config.LogFile = filepath.Join("data", config.LogFile)
			}
		}
		// 保存到全局变量，确保退出时能访问
		globalLogPath = config.LogFile
		
		if err := initLogger(); err != nil {
			// 先打印帮助菜单，再打印错误原因
			printHelp()
			printError(fmt.Sprintf("错误: 初始化日志失败: %v\n", err))
			os.Exit(1)
		}
		defer closeLogger()
	}

	// 检查系统文件描述符限制并并给出建议
	checkFileDescriptorLimit()

	// 检查高并发场景下的系统限制
	if config.Concurrency > 500 {
		if err := checkHighConcurrencySystemLimits(); err != nil {
			// 先打印帮助菜单，再打印错误原因
			printHelp()
			printError(fmt.Sprintf("高并发检查失败: %v\n", err))
			os.Exit(1)
		}
	}

	// 只有所有参数都正确后，才开始检查本机IP - 关键修改点
	// 获取本机IP（递进式尝试，两次超时）
	localIP, ipFetchFailed = getLocalIP()
	handleIPFetchResult()

	// 设置信号处理
	setupSignalHandler()

	// 初始化协程池
	workerPool = make(chan struct{}, config.Concurrency)
	printDebugWithCaller(fmt.Sprintf("协程池初始化完成，并发数: %d", config.Concurrency))

	// 初始化结果文件名（根据保存模式）
	initResultFileName()

	// 记录任务开始时间
	startTime = time.Now()

	// 显示当前配置信息
	printConfigInfo()

	// 启动进度显示计时器（详细模式下不启动）
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
		// 先打印帮助菜单，再打印错误原因
		printHelp()
		printError("无效的模式，请使用 -m s 或 -testfile\n")
		os.Exit(1)
	}

	// 等待所有任务完成
	printDebugWithCaller("等待所有任务完成...")
	wg.Wait()
	printDebugWithCaller("所有任务已完成")

	// 停止进度计时器并清理
	stopProgressTracker()
	progressStopped = true
	printFinalProgress()

	// 处理保存
	handleSave()
	
	// 打印保存的文件信息
	printSavedFilesInfo()
	
	// 打印完成信息
	printCompletionInfo()
	
	// 打印签名信息
	printSignature()
}

// 打印签名信息
func printSignature() {
	// 先添加换行，与前面内容分隔开
	unifiedOutput("\n", false)
	// 输出签名内容
	signature := `___________________________________

如有建议或 BUG 反馈, 欢迎联络作者
作者 ED, Telegram 联络 https://t.me/hongkongisp
服务器推荐 顺安云 https://say.cc
线上即时即用云主机, 请到顺安云
`
	unifiedOutput(signature, false)
}

// 确保data目录存在
func ensureDataDir() error {
	if err := os.MkdirAll("data", 0755); err != nil {
		return fmt.Errorf("创建data目录失败: %v", err)
	}
	return nil
}

// 处理保存代理
func handleSave() {
	// 检查是否正在退出，防止重复处理
	exitStatus.Lock()
	if exitStatus.exiting {
		exitStatus.Unlock()
		return
	}
	exitStatus.exiting = true
	exitStatus.Unlock()
	
	// 只有当保存模式不是0时才保存
	if config.SaveMode != 0 {
		if err := saveResults(); err != nil {
			printError(fmt.Sprintf("保存代理失败: %v\n", err))
		} else {
			isSaved = true
		}
	}
}

// 保存代理结果
func saveResults() error {
	if config.SaveMode == 0 {
		return nil
	}
	
	// 确保data目录存在
	if err := ensureDataDir(); err != nil {
		return err
	}
	
	foundProxies.RLock()
	defer foundProxies.RUnlock()
	
	// 根据保存模式筛选代理
	var proxiesToSave []ProxyResult
	for _, proxy := range foundProxies.list {
		if config.SaveMode == 1 || 
		   (config.SaveMode == 2 && !proxy.ExposesIP) {
			proxiesToSave = append(proxiesToSave, proxy)
		}
	}
	
	if len(proxiesToSave) == 0 {
		return nil
	}
	
	// 打开文件
	file, err := os.Create(resultFileName)
	if err != nil {
		return fmt.Errorf("创建文件失败: %v", err)
	}
	defer file.Close()
	
	writer := bufio.NewWriter(file)
	defer writer.Flush()
	
	// 写入代理
	for _, proxy := range proxiesToSave {
		line := proxy.URL
		if config.Verbose && !ipFetchFailed {
			status := "非匿名"
			if !proxy.ExposesIP {
				status = "匿名"
			}
			line += fmt.Sprintf("  # %s, 检测到IP: %s", status, proxy.DetectedIP)
		}
		line += "\n"
		
		if _, err := writer.WriteString(line); err != nil {
			return fmt.Errorf("写入文件失败: %v", err)
		}
	}
	
	return nil
}

// 打印保存的文件信息，添加开头换行
func printSavedFilesInfo() {
	// 确保输出之间有足够的间隔
	newLinePrinted := false
	
	if config.SaveMode != 0 && resultFileName != "" {
		foundProxies.RLock()
		count := 0
		for _, proxy := range foundProxies.list {
			if config.SaveMode == 1 || 
			   (config.SaveMode == 2 && !proxy.ExposesIP) {
				count++
			}
		}
		foundProxies.RUnlock()
		
		if count > 0 {
			// 开头添加换行
			unifiedOutput("\n", false)
			newLinePrinted = true
			saveInfo := fmt.Sprintf("已保存 %d 个代理到文件: %s\n", count, resultFileName)
			unifiedOutput(saveInfo, false)
		} else {
			unifiedOutput("\n没有符合条件的代理可保存\n", false)
			newLinePrinted = true
		}
	}
	
	if config.LogEnabled && config.LogFile != "" {
		// 如果前面没加过换行，这里加一个
		if !newLinePrinted {
			unifiedOutput("\n", false)
		}
		logSaveInfo := fmt.Sprintf("日志已保存到文件: %s\n", config.LogFile)
		unifiedOutput(logSaveInfo, false)
	}
}

// 初始化日志系统
func initLogger() error {
	// 检查日志文件目录是否存在，不存在则创建
	logDir := filepath.Dir(config.LogFile)
	if logDir != "" {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return fmt.Errorf("创建日志目录失败: %v", err)
		}
	}

	// 创建日志文件
	var err error
	logFile, err = os.Create(config.LogFile)
	if err != nil {
		return fmt.Errorf("创建日志文件失败: %v", err)
	}

	// 创建带缓冲区的写入器
	logWriter = bufio.NewWriterSize(logFile, 16*1024)
	
	// 启动日志定时刷新
	logFlushTicker = time.NewTicker(3 * time.Second)
	go func() {
		for {
			select {
			case <-logFlushTicker.C:
				ioMutex.Lock()
				logWriter.Flush()
				ioMutex.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()
	
	return nil
}

// 关闭日志系统
func closeLogger() {
	ioMutex.Lock()
	defer ioMutex.Unlock()
	
	if logFlushTicker != nil {
		logFlushTicker.Stop()
	}
	
	if logWriter != nil {
		logWriter.Flush() // 最后一次强制刷新
	}
	if logFile != nil {
		logFile.Close()
	}
}

// 统一IO输出函数
func unifiedOutput(message string, isProgress bool) {
	// 如果输出已禁用，则不处理普通输出
	if outputDisabled && !isProgress {
		return
	}
	
	ioMutex.Lock()
	defer ioMutex.Unlock()
	
	// 打印到控制台
	if isProgress {
		// 进度信息特殊处理：覆盖当前行
		if runtime.GOOS == "windows" {
			fmt.Println(message)
		} else {
			fmt.Printf("\r\033[K%s", message)
		}
	} else {
		fmt.Print(message)
	}
	
	// 如果启用了日志，且不是进度信息
	if config.LogEnabled && logWriter != nil && !isProgress {
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		logMessage := fmt.Sprintf("[%s] %s", timestamp, message)
		logWriter.WriteString(logMessage)
	}
}

// 清空终端画面
func clearScreen() {
	ioMutex.Lock()
	defer ioMutex.Unlock()
	
	if outputDisabled {
		return
	}
	
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "cls")
		cmd.Stdout = os.Stdout
		cmd.Run()
	} else {
		fmt.Print("\033[H\033[2J") // ANSI清屏序列
	}
	
	// 清屏操作记录到日志
	if config.LogEnabled && logWriter != nil {
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		logWriter.WriteString(fmt.Sprintf("[%s] 屏幕已清空\n", timestamp))
		logWriter.Flush()
	}
}

// 信号处理函数
func setupSignalHandler() {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	
	// 跟踪中断信号次数
	interruptCount := 0
	
	go func() {
		for {
			<-c
			interruptCount++
			
			// 记录中断信号到日志
			logEntry := fmt.Sprintf("收到中断信号(Ctrl+C)，次数: %d\n", interruptCount)
			if config.LogEnabled && logWriter != nil {
				ioMutex.Lock()
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				logWriter.WriteString(fmt.Sprintf("[%s] %s", timestamp, logEntry))
				logWriter.Flush()
				ioMutex.Unlock()
			}
			
			// 检查是否正在退出，防止重复处理
			exitStatus.Lock()
			if exitStatus.exiting {
				exitStatus.Unlock()
				continue
			}
			exitStatus.Unlock()
			
			// 立即停止接受新任务
			shutdown = true
			cancel()
			
			// 第一次中断：处理保存并退出
			if interruptCount == 1 {				
				// 启动一个独立协程处理保存，避免阻塞信号处理
				go func() {
					// 检查是否需要保存
					needSave := false
					if config.SaveMode != 0 {
						foundProxies.RLock()
						count := 0
						for _, proxy := range foundProxies.list {
							if config.SaveMode == 1 || 
							   (config.SaveMode == 2 && !proxy.ExposesIP) {
								count++
							}
						}
						foundProxies.RUnlock()
						needSave = count > 0
					}
					
					// 只在需要保存时显示提示
					if needSave {
						clearScreen()
						msg := "接收到中断信号(Ctrl+C)，正在处理保存操作...\n"
						fmt.Print(msg)
					} else {
						clearScreen()
						msg := "接收到中断信号(Ctrl+C)，正在退出...\n"
						fmt.Print(msg)
					}
					
					// 短暂等待确保数据一致性
					time.Sleep(500 * time.Millisecond)
					
					// 处理保存
					handleSave()
					
					// 打印保存的文件信息
					printSavedFilesInfo()
					
					// 打印完成信息和签名
					printCompletionInfo()
					printSignature()
					
					os.Exit(0)
				}()
			} else {
				// 第二次中断，强制退出
				clearScreen()
				fmt.Println("收到第二次中断信号，强制退出！")
				os.Exit(1)
			}
		}
	}()
}

// 递进式获取本机IP
func getLocalIP() (string, bool) {
	// 尝试第一个IP获取服务
	printInfo("正在获取本机IP（尝试 1/2: http://myip.ipip.net，3秒超时）")
	ip, err := fetchIPFromService("http://myip.ipip.net", 3*time.Second, parseIPIPNetResponse)
	if err == nil && ip != "" {
		return ip, false
	}
	printWarn(fmt.Sprintf("第一个IP服务失败: %v", err))

	// 尝试第二个IP获取服务（trackip.net）
	printInfo("正在获取本机IP（尝试 2/2: https://trackip.net/ip，3秒超时）")
	ip, err = fetchIPFromService("https://trackip.net/ip", 3*time.Second, parseTrackIPResponse)
	if err == nil && ip != "" {
		return ip, false
	}
	printWarn(fmt.Sprintf("第二个IP服务失败: %v", err))

	// 两次尝试都失败
	return "", true
}

// 从指定服务获取IP
func fetchIPFromService(url string, timeout time.Duration, parser func(string) string) (string, error) {
	// 创建HTTP客户端时使用随机Windows User-Agent
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

	// 创建请求并设置随机User-Agent
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("User-Agent", getRandomWindowsUserAgent())

	resp, err := client.Do(req)
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

	// 当启用-header选项时，打印IP获取服务的响应头
	if config.Header {
		printFormattedHeaders(url, resp)
	}

	ip := parser(string(body))
	if ip == "" {
		return "", fmt.Errorf("无法从响应中解析IP")
	}

	return ip, nil
}

// 解析 myip.ipip.net 的响应
func parseIPIPNetResponse(body string) string {
	re := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	matches := re.FindStringSubmatch(body)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// 解析 trackip.net 的响应
func parseTrackIPResponse(body string) string {
	re := regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
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
		time.Sleep(3 * time.Second)
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

	if rlimit.Cur == 1024 {
		return fmt.Errorf(
			"检测到系统文件描述符限制为默认值1024，无法支持高并发(%d)\n"+
			"请先在终端执行以下命令提高限制，然后重新运行程序:\n"+
			"  ulimit -n 65535", config.Concurrency)
	}

	if rlimit.Cur < uint64(config.Concurrency*2) {
		printWarn(fmt.Sprintf(
			"警告：系统文件描述符限制(%d)可能不足以支持指定的并发数(%d)\n"+
			"建议提高限制以避免'打开文件过多'错误: ulimit -n %d",
			rlimit.Cur, config.Concurrency, config.Concurrency*2))
		time.Sleep(5 * time.Second)
	}

	return nil
}

// 打印函数 - 成功信息
func printSuccess(msg string) {
	fullMsg := "\033[32m" + msg + "\033[0m\n" // 绿色
	unifiedOutput(fullMsg, false)
}

// 打印函数 - 警告信息
func printWarn(msg string) {
	fullMsg := "\033[33m" + msg + "\033[0m\n" // 黄色
	unifiedOutput(fullMsg, false)
}

// 打印函数 - 错误信息
func printError(msg string) {
	fullMsg := "\033[31m" + msg + "\033[0m\n" // 红色
	unifiedOutput(fullMsg, false)
}

// 打印函数 - 普通信息
func printInfo(msg string) {
	fullMsg := msg + "\n"
	unifiedOutput(fullMsg, false)
}

// 打印函数 - 调试信息（仅在-v模式下显示）
func printDebug(msg string) {
	// 只在详细模式且输出未禁用时打印
	if !config.Verbose || outputDisabled {
		return
	}
	
	fullMsg := "\033[90m[DEBUG] " + msg + "\033[0m\n" // 灰色
	unifiedOutput(fullMsg, false)
}

// 增加调试日志，帮助定位卡壳位置
func printDebugWithCaller(msg string) {
	// 只在详细模式下显示
	if !config.Verbose {
		return
	}
	
	// 获取调用者信息
	_, file, line, ok := runtime.Caller(1)
	if !ok {
		file = "???"
		line = 0
	}
	shortFile := filepath.Base(file)
	
	printDebug(fmt.Sprintf("%s:%d - %s", shortFile, line, msg))
}

// 进度条打印函数（详细模式下不打印）
func printProgress() {
	// 关键修改：详细模式下不打印进度
	if config.Verbose {
		return
	}
	
	// 如果已经收到中断信号或已停止，不再打印进度
	if shutdown || progressStopped || outputDisabled {
		return
	}
	
	tasksMutex.Lock()
	defer tasksMutex.Unlock()

	if totalTasks == 0 {
		return
	}

	remaining := totalTasks - completedTasks
	percent := float64(completedTasks) / float64(totalTasks) * 100

	elapsed := time.Since(startTime)
	var estimatedTotal time.Duration
	var remainingTime time.Duration
	
	// 改进的剩余时间计算逻辑
	// 1. 等待第一个批次完成后才开始计算剩余时间
	// 2. 确保不会出现负数时间
	if completedTasks > 0 {
		// 计算是否达到最小完成量（第一个批次）
		// 使用批次大小或总任务的10%作为阈值，取较小值
		minCompleted := int64(config.BatchSize)
		if totalTasks / 10 < minCompleted {
			minCompleted = totalTasks / 10 // 总任务的10%
		}
		// 确保至少有一个合理的最小值
		if minCompleted < 10 {
			minCompleted = 10
		}
		
		// 只有在完成第一个批次或10%的任务后才开始计算
		if completedTasks >= minCompleted {
			// 计算预计总时间
			estimatedTotal = elapsed * time.Duration(totalTasks) / time.Duration(completedTasks)
			remainingTime = estimatedTotal - elapsed
			
			// 确保剩余时间不为负（多重保障）
			if remainingTime < 0 {
				remainingTime = 0
			}
		} else {
			// 尚未完成第一个批次，显示"计算中..."
			remainingTime = -1
		}
	} else {
		// 尚未完成任何任务
		remainingTime = -1
	}

	var timeStr string
	if remainingTime < 0 {
		timeStr = "计算中..."
	} else if remainingTime >= 24*time.Hour {
		days := int(remainingTime.Hours() / 24)
		hours := int(remainingTime.Hours()) % 24
		minutes := int(remainingTime.Minutes()) % 60
		timeStr = fmt.Sprintf("%d天%d时%d分", days, hours, minutes)
	} else if remainingTime >= time.Hour {
		hours := int(remainingTime.Hours())
		minutes := int(remainingTime.Minutes()) % 60
		seconds := int(remainingTime.Seconds()) % 60
		timeStr = fmt.Sprintf("%d时%d分%d秒", hours, minutes, seconds)
	} else if remainingTime >= time.Minute {
		minutes := int(remainingTime.Minutes())
		seconds := int(remainingTime.Seconds()) % 60
		timeStr = fmt.Sprintf("%d分%d秒", minutes, seconds)
	} else {
		seconds := int(remainingTime.Seconds())
		timeStr = fmt.Sprintf("%d秒", seconds)
	}

	foundProxies.RLock()
	validCount := len(foundProxies.list)
	foundProxies.RUnlock()

	// 显示已分配/总任务数，已完成/总任务数
	progress := fmt.Sprintf("进度: 已分配: %d/%d, 已完成: %d/%d, 剩余: %d, 完成率: %.2f%%, 有效代理: %d, 预计剩余时间: %s",
		dispatchedTasks, totalTasks, completedTasks, totalTasks, remaining, percent, validCount, timeStr)

	// 详细模式下不输出，非详细模式下覆盖当前行
	unifiedOutput(progress, true)
}

// 打印最终进度（详细模式下不打印）
func printFinalProgress() {
	// 关键修改：详细模式下不打印最终进度
	if config.Verbose {
		return
	}
	
	if !shutdown && !progressStopped && !outputDisabled && totalTasks > 0 { 
		// 打印最终进度后换行，避免覆盖
		fmt.Println()
	}
}

// 解析命令行参数
func parseFlags() {
	mode := flag.String("m", "", "模式: s(发现模式)")
	rangeFlag := flag.String("range", "", "IP范围，如 0.0.0.0-255.255.255.0 或 0.0.0.0/21")
	file := flag.String("file", "", "IP列表文件路径，支持单个IP和CIDR格式，每行一个")
	testFile := flag.String("testfile", "", "代理测试文件路径")
	proxyType := flag.String("type", strings.Join(AllProxyTypes, ","), "代理类型，逗号分隔")
	verbose := flag.Bool("v", false, "详细模式（启用调试日志，不显示进度）")
	timeout := flag.Int("t", 10, "超时时间(秒)")
	retry := flag.Int("retry", 2, "重试次数")
	header := flag.Bool("header", false, "打印HTTP响应头，且只保留有有效响应头的代理")
	urlFlag := flag.String("url", "https://httpbin.org/ip", "测试URL")
	contentCheck := flag.String("content", "", "内容验证关键字")
	ignoreSSL := flag.Bool("ignoressl", false, "忽略SSL证书错误")
	concurrency := flag.Int("p", 200, "并发数")
	help := flag.Bool("h", false, "显示帮助信息")
	portsFlag := flag.String("ports", "", "要扫描的端口，逗号分隔，如 80,8080")
	logEnabled := flag.Bool("log", false, "启用日志功能，将所有输出保存到日志文件")
	logFile := flag.String("logfile", "", "指定日志文件路径，默认为data/log_时间戳.log")
	batchSize := flag.Int("batch", 1000, "任务批次大小，处理大型网段时使用")
	saveMode := flag.Int("s", 1, "保存模式: 0(不保存), 1(保存所有有效代理), 2(只保存匿名代理)，默认1")

	flag.Parse()

	modeStr := *mode
	if *testFile != "" {
		modeStr = "t"
	}

	urlStr := *urlFlag
	customURL := false
	if urlStr != "https://httpbin.org/ip" {
		customURL = true
	}
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		urlStr = "https://" + urlStr
		customURL = true
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
		Ports:        ports,
		CustomURL:    customURL,
		LogEnabled:   *logEnabled,
		LogFile:      *logFile,
		BatchSize:    *batchSize,
		SaveMode:     *saveMode,
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

// 验证配置参数
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
	if config.BatchSize <= 0 {
		return fmt.Errorf("批次大小必须大于0")
	}
	if config.SaveMode < 0 || config.SaveMode > 2 {
		return fmt.Errorf("无效的保存模式: %d，必须是0、1或2", config.SaveMode)
	}

	for _, port := range config.Ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("无效的端口号: %d（必须在1-65535之间）", port)
		}
	}

	return nil
}

// 初始化结果文件名
func initResultFileName() {
	if config.SaveMode == 0 {
		return
	}
	
	timestamp := time.Now().Format("20060102150405")
	suffix := ""
	if config.SaveMode == 2 {
		suffix = "_anonymous"
	}
	resultFileName = fmt.Sprintf("data/proxies_%s%s.txt", timestamp, suffix)
}

// 打印配置信息
func printConfigInfo() {
	if len(config.Types) == len(AllProxyTypes) {
		printWarn(fmt.Sprintf("使用代理类型: 所有支持的类型（%s）", strings.Join(AllProxyTypes, ", ")))
	} else {
		printWarn(fmt.Sprintf("使用代理类型: %s", strings.Join(config.Types, ", ")))
	}
	
	if len(config.Ports) > 0 {
		portStrs := make([]string, len(config.Ports))
		for i, port := range config.Ports {
			portStrs[i] = strconv.Itoa(port)
		}
		printWarn(fmt.Sprintf("扫描端口: %s", strings.Join(portStrs, ", ")))
	} else {
		// 收集并显示所有代理类型的预设端口
		portSet := make(map[int]bool)
		for _, proxyType := range config.Types {
			ports := getCommonPorts(proxyType)
			for _, port := range ports {
				portSet[port] = true
			}
		}
		// 转换为有序列表
		ports := make([]int, 0, len(portSet))
		for port := range portSet {
			ports = append(ports, port)
		}
		sort.Ints(ports)
		portStrs := make([]string, len(ports))
		for i, port := range ports {
			portStrs[i] = strconv.Itoa(port)
		}
		printWarn(fmt.Sprintf("使用预设端口进行扫描: %s", strings.Join(portStrs, ", ")))
	}
	
	printWarn(fmt.Sprintf("并发数: %d，超时时间: %d秒，重试次数: %d",
		config.Concurrency, config.Timeout, config.RetryCount))
	printWarn(fmt.Sprintf("测试URL: %s", config.URL))
	
	if config.CustomURL {
		printWarn("注意：使用自定义URL只能测试连通性，无法检查代理是否暴露本机IP")
	}
	
	if config.Header {
		printWarn("已启用-header选项：只保留有有效响应头的代理，并打印其响应头")
	}
	
	// 显示保存模式
	switch config.SaveMode {
	case 0:
		printWarn("保存模式: 不保存任何代理")
	case 1:
		printWarn("保存模式: 保存所有有效代理到data文件夹")
	case 2:
		printWarn("保存模式: 只保存匿名代理到data文件夹")
	}
	
	if config.LogEnabled {
		printWarn(fmt.Sprintf("已启用日志功能，日志文件: %s", globalLogPath))
	}
	
	// 对于大型网段，显示批次处理信息
	if config.Mode == "s" && (config.Range != "" && strings.Contains(config.Range, "/") || 
	                         config.File != "") {
		printWarn(fmt.Sprintf("使用批次处理模式，批次大小: %d，适合大型网段扫描", config.BatchSize))
	}
}

// 打印帮助信息
func printHelp() {
	ioMutex.Lock()
	defer ioMutex.Unlock()
	
	fmt.Println("代理查找器使用帮助:")
	fmt.Println("  功能: 扫描和测试HTTP/HTTPS/SOCKS4/SOCKS5代理的有效性和匿名性")
	fmt.Println("\n使用方式:")
	fmt.Println("  发现模式:")
	fmt.Println("    proxy-finder -m s -range <IP范围> [选项]")
	fmt.Println("    proxy-finder -m s -file <IP列表文件> [选项]")
	fmt.Println("  测试模式:")
	fmt.Println("    proxy-finder -testfile <代理列表文件> [选项]")
	fmt.Println("\n选项:")
	fmt.Println("  -m s          指定发现模式")
	fmt.Println("  -range        IP范围，格式: 192.168.1.1-192.168.1.255 或 192.168.1.0/24")
	fmt.Println("  -file         IP列表文件路径，支持单个IP和CIDR格式（如192.168.1.0/24），每行一个记录")
	fmt.Println("  -testfile     包含代理列表的文件路径，用于测试模式，每行一个代理")
	fmt.Println("  -type         代理类型，逗号分隔，默认为所有支持类型(http,https,socks4,socks5)")
	fmt.Println("  -v            详细模式，显示调试信息（不显示进度）")
	fmt.Println("  -t            超时时间(秒)，默认为10秒")
	fmt.Println("  -retry        重试次数，默认为2次")
	fmt.Println("  -header       打印HTTP响应头，只保留有有效响应头的代理")
	fmt.Println("  -url          测试URL，默认为https://httpbin.org/ip")
	fmt.Println("  -content      内容验证关键字，只保留包含该关键字的响应")
	fmt.Println("  -ignoressl    忽略SSL证书错误")
	fmt.Println("  -p            并发数，默认为200")
	fmt.Println("  -ports        要扫描的端口，逗号分隔，如 80,8080")
	fmt.Println("  -log          启用日志功能，将输出保存到日志文件")
	fmt.Println("  -logfile      指定日志文件路径，默认为data/log_时间戳.log")
	fmt.Println("  -batch        任务批次大小，处理大型网段时使用，默认为1000")
	fmt.Println("  -s            保存模式: 0(不保存), 1(保存所有有效代理), 2(只保存匿名代理)，默认1")
	fmt.Println("  -h            显示帮助信息")
	fmt.Println("\n示例:")
	fmt.Println("  从文件扫描IP和CIDR范围的HTTP和HTTPS代理:")
	fmt.Println("    proxy-finder -m s -file ips.txt -type http,https")
	fmt.Println("  测试proxies.txt文件中的代理，只保存匿名代理:")
	fmt.Println("    proxy-finder -testfile proxies.txt -s 2")
	fmt.Println("  使用详细模式和自定义端口扫描:")
	fmt.Println("    proxy-finder -m s -range 10.0.0.0/24 -ports 8080,3128 -v")
}

// 获取常见代理端口
func getCommonPorts(proxyType string) []int {
	switch proxyType {
	case "http", "https":
		return []int{80, 81, 3128, 8080, 8081, 9050, 9051}
	case "socks4", "socks5":
		return []int{1080, 9050, 9051, 8080, 3128}
	default:
		return []int{80, 81, 1080, 3128, 8080, 8081, 9050, 9051}
	}
}

// 打印格式化的响应头，增加状态码打印和开头换行
func printFormattedHeaders(url string, resp *http.Response) {
	// 开头添加换行，与其他内容分隔开
	unifiedOutput("\n", false)
	// 打印状态码和URL
	statusInfo := fmt.Sprintf("===== %s 响应头（状态码：%d） =====\n", url, resp.StatusCode)
	unifiedOutput(statusInfo, false)
	for name, values := range resp.Header {
		for _, value := range values {
			headerLine := fmt.Sprintf("%s: %s\n", name, value)
			unifiedOutput(headerLine, false)
		}
	}
	unifiedOutput("===============================\n", false)
}

// 运行发现模式
func runDiscoverMode() {
	var ips []string
	var err error
	
	// 从文件或IP范围获取IP列表
	if config.File != "" {
		printInfo(fmt.Sprintf("从文件读取IP和CIDR列表: %s", config.File))
		ips, err = readIPsFromFile(config.File)
	} else if config.Range != "" {
		printInfo(fmt.Sprintf("解析IP范围: %s", config.Range))
		ips, err = parseIPRange(config.Range)
	}
	
	if err != nil {
		// 先打印帮助菜单，再打印错误原因
		printHelp()
		printError(fmt.Sprintf("获取IP列表失败: %v", err))
		return
	}
	
	if len(ips) == 0 {
		// 先打印帮助菜单，再打印错误原因
		printHelp()
		printError("未获取到任何IP地址")
		return
	}
	
	// 去重IP列表
	ips = uniqueStrings(ips)
	printInfo(fmt.Sprintf("共解析到 %d 个唯一IP地址", len(ips)))
	
	// 获取要扫描的端口
	ports := getPortsToScan()
	if len(ports) == 0 {
		// 先打印帮助菜单，再打印错误原因
		printHelp()
		printError("未获取到任何端口，无法进行扫描")
		return
	}
	
	// 计算总任务数
	totalTasks = int64(len(ips) * len(ports) * len(config.Types))
	printInfo(fmt.Sprintf("总任务数: %d (IP数: %d, 端口数: %d, 代理类型数: %d)",
		totalTasks, len(ips), len(ports), len(config.Types)))
	
	// 按批次处理IP，避免内存占用过高
	batchSize := config.BatchSize
	for i := 0; i < len(ips); i += batchSize {
		end := i + batchSize
		if end > len(ips) {
			end = len(ips)
		}
		batchIPs := ips[i:end]
		printDebugWithCaller(fmt.Sprintf("处理IP批次 %d-%d/%d", i+1, end, len(ips)))
		
		// 处理当前批次的IP
		processIPBatch(batchIPs, ports)
		
		// 检查是否需要停止
		if shutdown {
			printInfo("检测到终止信号，停止处理更多批次")
			break
		}
	}
}

// 处理IP批次
func processIPBatch(ips []string, ports []int) {
	for _, ip := range ips {
		if shutdown {
			break
		}
		
		for _, port := range ports {
			if shutdown {
				break
			}
			
			for _, proxyType := range config.Types {
				if shutdown {
					break
				}
				
				// 增加已分配任务计数
				tasksMutex.Lock()
				dispatchedTasks++
				tasksMutex.Unlock()
				
				wg.Add(1)
				workerPool <- struct{}{}
				
				go func(ip string, port int, proxyType string) {
					defer wg.Done()
					defer func() { <-workerPool }()
					
					// 检查是否已终止
					if shutdown {
						tasksMutex.Lock()
						completedTasks++
						tasksMutex.Unlock()
						return
					}
					
					// 测试代理
					testProxy(ip, port, proxyType)
					
					// 更新完成任务计数
					tasksMutex.Lock()
					completedTasks++
					tasksMutex.Unlock()
				}(ip, port, proxyType)
			}
		}
	}
}

// 运行测试模式
func runTestMode() {
	printInfo(fmt.Sprintf("从文件读取代理列表: %s", config.TestFile))
	proxies, err := readProxiesFromFile(config.TestFile)
	if err != nil {
		// 先打印帮助菜单，再打印错误原因
		printHelp()
		printError(fmt.Sprintf("读取代理列表失败: %v", err))
		return
	}
	
	if len(proxies) == 0 {
		// 先打印帮助菜单，再打印错误原因
		printHelp()
		printError("未读取到任何代理")
		return
	}
	
	// 去重代理列表
	uniqueProxies := uniqueProxyList(proxies)
	printInfo(fmt.Sprintf("共读取到 %d 个代理，去重后 %d 个", len(proxies), len(uniqueProxies)))
	
	// 设置总任务数
	totalTasks = int64(len(uniqueProxies))
	
	// 提交所有代理测试任务
	for _, proxy := range uniqueProxies {
		if shutdown {
			break
		}
		
		// 增加已分配任务计数
		tasksMutex.Lock()
		dispatchedTasks++
		tasksMutex.Unlock()
		
		wg.Add(1)
		workerPool <- struct{}{}
		
		go func(p Proxy) {
			defer wg.Done()
			defer func() { <-workerPool }()
			
			// 检查是否已终止
			if shutdown {
				tasksMutex.Lock()
				completedTasks++
				tasksMutex.Unlock()
				return
			}
			
			// 测试代理
			testProxy(p.IP, p.Port, p.Type)
			
			// 更新完成任务计数
			tasksMutex.Lock()
			completedTasks++
			tasksMutex.Unlock()
		}(proxy)
	}
}

// 从文件读取IP列表，支持单个IP和CIDR格式
func readIPsFromFile(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %v", err)
	}
	defer file.Close()
	
	var allIPs []string
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue // 跳过空行和注释行
		}
		
		// 解析每行的IP或CIDR
		ips, err := parseIPOrCIDR(line)
		if err != nil {
			printWarn(fmt.Sprintf("文件 %s 第 %d 行格式错误: %v，将跳过", filename, lineNumber, err))
			continue
		}
		
		allIPs = append(allIPs, ips...)
	}
	
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取文件失败: %v", err)
	}
	
	return allIPs, nil
}

// 解析单个IP或CIDR格式，返回所有包含的IP
func parseIPOrCIDR(ipOrCIDR string) ([]string, error) {
	if strings.Contains(ipOrCIDR, "/") {
		// CIDR格式
		return parseCIDR(ipOrCIDR)
	} else if isValidIP(ipOrCIDR) {
		// 单个IP
		return []string{ipOrCIDR}, nil
	}
	
	return nil, fmt.Errorf("无效的IP或CIDR格式: %s", ipOrCIDR)
}

// 从文件读取代理列表
func readProxiesFromFile(filename string) ([]Proxy, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %v", err)
	}
	defer file.Close()
	
	var proxies []Proxy
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue // 跳过空行和注释行
		}
		
		proxy, err := parseProxy(line)
		if err != nil {
			printWarn(fmt.Sprintf("文件 %s 第 %d 行格式错误: %v，将跳过", filename, lineNumber, err))
			continue
		}
		
		proxies = append(proxies, proxy)
	}
	
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取文件失败: %v", err)
	}
	
	return proxies, nil
}

// 解析代理字符串
func parseProxy(proxyStr string) (Proxy, error) {
	// 支持的格式:
	// - http://ip:port
	// - https://ip:port
	// - socks4://ip:port
	// - socks5://ip:port
	// - ip:port (默认http)
	
	var proxyType string
	var ipPort string
	
	if strings.Contains(proxyStr, "://") {
		parts := strings.SplitN(proxyStr, "://", 2)
		if len(parts) != 2 {
			return Proxy{}, fmt.Errorf("格式不正确: %s", proxyStr)
		}
		
		proxyType = strings.ToLower(parts[0])
		ipPort = parts[1]
		
		// 验证代理类型
		valid := false
		for _, t := range AllProxyTypes {
			if proxyType == t {
				valid = true
				break
			}
		}
		if !valid {
			return Proxy{}, fmt.Errorf("不支持的代理类型: %s", proxyType)
		}
	} else {
		// 默认为http代理
		proxyType = "http"
		ipPort = proxyStr
	}
	
	// 解析IP和端口
	parts := strings.Split(ipPort, ":")
	if len(parts) != 2 {
		return Proxy{}, fmt.Errorf("缺少端口号: %s", ipPort)
	}
	
	ip := parts[0]
	portStr := parts[1]
	
	if !isValidIP(ip) {
		return Proxy{}, fmt.Errorf("无效的IP地址: %s", ip)
	}
	
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return Proxy{}, fmt.Errorf("无效的端口号: %s", portStr)
	}
	
	return Proxy{
		Type: proxyType,
		IP:   ip,
		Port: port,
	}, nil
}

// 验证IP地址有效性
func isValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// 解析IP范围
func parseIPRange(ipRange string) ([]string, error) {
	if strings.Contains(ipRange, "/") {
		// CIDR格式: 192.168.1.0/24
		return parseCIDR(ipRange)
	} else if strings.Contains(ipRange, "-") {
		// 起始-结束格式: 192.168.1.1-192.168.1.255
		return parseIPRangeStartEnd(ipRange)
	} else if isValidIP(ipRange) {
		// 单个IP
		return []string{ipRange}, nil
	}
	
	return nil, fmt.Errorf("无效的IP范围格式: %s", ipRange)
}

// 解析CIDR格式的IP范围
func parseCIDR(cidr string) ([]string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("解析CIDR失败: %v", err)
	}
	
	var ips []string
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
		ips = append(ips, ip.String())
	}
	
	// 移除网络地址和广播地址（对于/32和/31特殊处理）
	maskSize, _ := ipnet.Mask.Size()
	if maskSize < 31 {
		if len(ips) > 2 {
			ips = ips[1 : len(ips)-1]
		} else if len(ips) == 2 {
			ips = ips[:1]
		}
	}
	
	return ips, nil
}

// 解析起始-结束格式的IP范围
func parseIPRangeStartEnd(rangeStr string) ([]string, error) {
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("无效的IP范围格式，应使用 start-end 形式")
	}
	
	startIPStr := strings.TrimSpace(parts[0])
	endIPStr := strings.TrimSpace(parts[1])
	
	startIP := net.ParseIP(startIPStr)
	endIP := net.ParseIP(endIPStr)
	
	if startIP == nil || endIP == nil {
		return nil, fmt.Errorf("无效的IP地址: %s 或 %s", startIPStr, endIPStr)
	}
	
	// 确保都是IPv4或都是IPv6
	if startIP.To4() != nil && endIP.To4() == nil {
		return nil, fmt.Errorf("IP版本不匹配: 混合了IPv4和IPv6")
	}
	if startIP.To16() != nil && startIP.To4() == nil && 
	   (endIP.To16() == nil || endIP.To4() != nil) {
		return nil, fmt.Errorf("IP版本不匹配: 混合了IPv4和IPv6")
	}
	
	var ips []string
	// 使用bytes.Compare修复之前的ip.After错误
	for ip := startIP; bytes.Compare(ip, endIP) <= 0; incIP(ip) {
		ips = append(ips, ip.String())
	}
	
	return ips, nil
}

// 递增IP地址
func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// 获取要扫描的端口
func getPortsToScan() []int {
	if len(config.Ports) > 0 {
		// 使用用户指定的端口
		return config.Ports
	}
	
	// 收集所有代理类型的常见端口
	portSet := make(map[int]bool)
	for _, proxyType := range config.Types {
		ports := getCommonPorts(proxyType)
		for _, port := range ports {
			portSet[port] = true
		}
	}
	
	// 转换为有序列表
	ports := make([]int, 0, len(portSet))
	for port := range portSet {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	
	return ports
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

// 代理列表去重
func uniqueProxyList(proxies []Proxy) []Proxy {
	seen := make(map[string]bool)
	result := []Proxy{}
	for _, proxy := range proxies {
		key := fmt.Sprintf("%s://%s:%d", proxy.Type, proxy.IP, proxy.Port)
		if !seen[key] {
			seen[key] = true
			result = append(result, proxy)
		}
	}
	return result
}

// 测试代理
func testProxy(ip string, port int, proxyType string) {
	proxyURL := fmt.Sprintf("%s://%s:%d", proxyType, ip, port)
	printDebug(fmt.Sprintf("测试代理: %s", proxyURL))
	
	// 创建代理URL
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil {
		printDebug(fmt.Sprintf("解析代理URL失败: %v", err))
		return
	}
	
	// 创建HTTP传输配置
	transport := &http.Transport{
		Proxy: http.ProxyURL(parsedProxyURL),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: config.IgnoreSSL,
		},
		DialContext: (&net.Dialer{
			Timeout:   time.Duration(config.Timeout) * time.Second,
			KeepAlive: 0,
		}).DialContext,
		TLSHandshakeTimeout: time.Duration(config.Timeout) * time.Second / 2,
		ResponseHeaderTimeout: time.Duration(config.Timeout) * time.Second,
	}
	
	// 创建HTTP客户端
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(config.Timeout) * time.Second,
	}
	
	// 重试机制
	var resp *http.Response
	maxRetries := config.RetryCount
	for retry := 0; retry <= maxRetries; retry++ {
		if retry > 0 {
			printDebug(fmt.Sprintf("重试代理 %s (第 %d/%d 次)", proxyURL, retry, maxRetries))
		}
		
		// 创建请求并设置随机User-Agent
		req, err := http.NewRequest("GET", config.URL, nil)
		if err != nil {
			printDebug(fmt.Sprintf("创建请求失败: %v", err))
			continue
		}
		req.Header.Set("User-Agent", getRandomWindowsUserAgent())
		
		// 发送请求
		resp, err = client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			break // 成功，退出重试循环
		}
		
		// 处理错误
		if err != nil {
			printDebug(fmt.Sprintf("代理 %s 请求失败: %v", proxyURL, err))
		} else {
			printDebug(fmt.Sprintf("代理 %s 响应状态码: %d", proxyURL, resp.StatusCode))
			resp.Body.Close()
		}
		
		// 如果是最后一次重试且失败，返回
		if retry == maxRetries {
			return
		}
		
		// 指数退避重试
		backoff := time.Duration(retry*retry) * 100 * time.Millisecond
		time.Sleep(backoff)
	}
	
	// 确保响应不为空
	if resp == nil {
		return
	}
	defer resp.Body.Close()
	
	// 如果启用了-header选项，打印响应头
	if config.Header {
		printFormattedHeaders(proxyURL, resp)
	}
	
	// 读取响应内容
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		printDebug(fmt.Sprintf("读取响应内容失败: %v", err))
		return
	}
	
	// 如果设置了内容验证关键字，检查是否包含
	if config.ContentCheck != "" && !strings.Contains(string(body), config.ContentCheck) {
		printDebug(fmt.Sprintf("代理 %s 响应不包含验证关键字: %s", proxyURL, config.ContentCheck))
		return
	}
	
	// 检查代理是否匿名（不暴露本机IP）
	exposesIP := false
	detectedIP := ""
	
	if !ipFetchFailed && !config.CustomURL {
		// 解析httpbin.org/ip的响应
		var ipResp IPResponse
		if err := json.Unmarshal(body, &ipResp); err == nil && ipResp.Origin != "" {
			detectedIP = ipResp.Origin
			// 检查检测到的IP是否包含本机IP（可能有多个IP，用逗号分隔）
			if strings.Contains(ipResp.Origin, localIP) {
				exposesIP = true
			}
		} else {
			printDebug(fmt.Sprintf("无法解析代理 %s 的IP响应: %v", proxyURL, err))
			return
		}
	}
	
	// 记录有效代理
	result := ProxyResult{
		URL:        proxyURL,
		ExposesIP:  exposesIP,
		DetectedIP: detectedIP,
	}
	
	foundProxies.Lock()
	foundProxies.list = append(foundProxies.list, result)
	foundProxies.Unlock()
	
	// 显示结果
	if config.Verbose && !ipFetchFailed && !config.CustomURL {
		if exposesIP {
			printSuccess(fmt.Sprintf("有效非匿名代理: %s (检测到IP: %s)", proxyURL, detectedIP))
		} else {
			printSuccess(fmt.Sprintf("有效匿名代理: %s (检测到IP: %s)", proxyURL, detectedIP))
		}
	} else {
		printSuccess(fmt.Sprintf("有效代理: %s", proxyURL))
	}
}

// 启动进度跟踪器（详细模式下不启动）
func startProgressTracker() {
	// 关键修改：详细模式下不启动进度跟踪器
	if config.Verbose {
		return
	}
	
	progressTicker = time.NewTicker(500 * time.Millisecond)
	go func() {
		for {
			select {
			case <-progressTicker.C:
				printProgress()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// 停止进度跟踪器
func stopProgressTracker() {
	if progressTicker != nil {
		progressTicker.Stop()
	}
}

// 打印完成信息
func printCompletionInfo() {
	duration := time.Since(startTime)
	foundProxies.RLock()
	count := len(foundProxies.list)
	foundProxies.RUnlock()
	
	printInfo(fmt.Sprintf("\n任务完成，耗时: %v", duration))
	printInfo(fmt.Sprintf("共发现 %d 个有效代理", count))
}
 
