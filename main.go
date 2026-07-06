package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 命令行版本的入口
func main() {
	stopCPUProfile := startCPUProfileFromEnv()
	defer stopCPUProfile()

	initLocations()
	showMenu()
}

func startCPUProfileFromEnv() func() {
	profilePath := strings.TrimSpace(os.Getenv("BCFI_CPU_PROFILE"))
	if profilePath == "" {
		return func() {}
	}

	file, err := os.Create(profilePath)
	if err != nil {
		fmt.Println("创建 CPU Profile 失败:", err)
		return func() {}
	}

	if err := pprof.StartCPUProfile(file); err != nil {
		file.Close()
		fmt.Println("启动 CPU Profile 失败:", err)
		return func() {}
	}

	fmt.Println("CPU Profile 输出:", profilePath)
	return func() {
		pprof.StopCPUProfile()
		if err := file.Close(); err != nil {
			fmt.Println("关闭 CPU Profile 失败:", err)
		}
	}
}

// 显示主菜单
func showMenu() {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Println("----------------------------------------")
		fmt.Println("1. IPV4 优选 (TLS)")
		fmt.Println("2. IPV4 优选 (非 TLS)")
		fmt.Println("3. IPV6 优选 (TLS)")
		fmt.Println("4. IPV6 优选 (非 TLS)")
		fmt.Println("5. 单 IP 测速 (TLS)")
		fmt.Println("6. 单 IP 测速 (非 TLS)")
		fmt.Println("7. 清空缓存")
		fmt.Println("8. 更新数据")
		fmt.Println("0. 退出")
		fmt.Print("请选择菜单 (默认 0): ")

		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			input = "0"
		}

		switch input {
		case "0":
			fmt.Println("退出成功")
			return
		case "1":
			runIPSelector(4, true)
		case "2":
			runIPSelector(4, false)
		case "3":
			runIPSelector(6, true)
		case "4":
			runIPSelector(6, false)
		case "5":
			runSingleSpeedTest(true)
		case "6":
			runSingleSpeedTest(false)
		case "7":
			clearCache()
		case "8":
			updateData()
		default:
			fmt.Println("无效输入，请重新选择")
		}
	}
}

// runIPSelector 运行 IP 优选流程
func runIPSelector(ipType int, useTLS bool) {
	bandwidth := 1
	expectedLatency := 0
	expectedDataCenter := ""
	expectedCount := 1
	rttTestCount := 100
	rttTestAll := false
	taskNum := 50
	rttServerName := defaultRTTServerName
	rttTimeout := defaultRTTTimeout

	scanner := bufio.NewScanner(os.Stdin)
	if useTLS {
		fmt.Printf("请设置 RTT TLS SNI (默认 %s): ", defaultRTTServerName)
		if scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input != "" {
				if isValidServerName(input) {
					rttServerName = input
				} else {
					fmt.Printf("SNI 无效，已使用默认值 %s\n", defaultRTTServerName)
				}
			}
		}
	}

	fmt.Printf("请设置 RTT 超时 (默认 %d，最低 %d，单位毫秒): ", int(defaultRTTTimeout/time.Millisecond), minRTTTimeoutMs)
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" {
			val, err := strconv.Atoi(input)
			if err != nil || val <= 0 {
				fmt.Printf("输入无效，已使用默认值 %d 毫秒\n", int(defaultRTTTimeout/time.Millisecond))
			} else {
				if val < minRTTTimeoutMs {
					fmt.Printf("低于最低超时限制，自动设置为 %d 毫秒\n", minRTTTimeoutMs)
					val = minRTTTimeoutMs
				}
				rttTimeout = time.Duration(val) * time.Millisecond
			}
		}
	}

	fmt.Print("请设置期望的带宽大小 (默认最小 1，单位 Mbps): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			bandwidth = 1
		} else {
			val, err := strconv.Atoi(input)
			if err != nil || val <= 0 {
				fmt.Println("输入无效，已使用默认值 1 Mbps")
				bandwidth = 1
			} else {
				bandwidth = val
			}
		}
	}

	fmt.Print("请设置期望延迟 (默认不限，单位 毫秒): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" {
			val, err := strconv.Atoi(input)
			if err != nil || val <= 0 {
				fmt.Println("输入无效，已不限制期望延迟")
				expectedLatency = 0
			} else {
				expectedLatency = val
			}
		}
	}

	fmt.Print("请设置期望数据中心位置 (默认不限，可输入三字码/城市/区域/国家代码): ")
	if scanner.Scan() {
		expectedDataCenter = strings.TrimSpace(scanner.Text())
	}

	fmt.Print("请设置期望个数 (默认 1，最大 100): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" {
			val, err := strconv.Atoi(input)
			if err != nil || val <= 0 {
				fmt.Println("输入无效，已使用默认值 1")
				expectedCount = 1
			} else {
				expectedCount = val
			}
			if expectedCount > 100 {
				fmt.Println("超过最大数量限制，自动设置为最大值 100")
				expectedCount = 100
			}
		}
	}

	fmt.Print("请设置 RTT 测试数量 (默认 100，输入 all 测试全部): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" {
			if strings.EqualFold(input, "all") || input == "全部" {
				rttTestAll = true
			} else {
				val, err := strconv.Atoi(input)
				if err != nil || val <= 0 {
					fmt.Println("输入无效，已使用默认值 100")
					rttTestCount = 100
				} else {
					rttTestCount = val
				}
			}
		}
	}

	fmt.Print("请设置 RTT 测试进程数 (默认 50，最大 100): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			taskNum = 50
		} else {
			val, err := strconv.Atoi(input)
			if err != nil {
				fmt.Println("输入无效，已使用默认值 50")
				taskNum = 50
			} else if val <= 0 {
				fmt.Println("进程数不能为 0，自动设置为默认值")
				taskNum = 50
			} else {
				taskNum = val
			}
			if taskNum > 100 {
				fmt.Println("超过最大进程限制，自动设置为最大值")
				taskNum = 100
			}
		}
	}

	speed := bandwidth * 128
	startTime := time.Now()

	// 执行 Cloudflare 测试
	results := cloudflareTest(ipType, useTLS, taskNum, speed, expectedLatency, expectedDataCenter, expectedCount, rttTestCount, rttTestAll, rttServerName, rttTimeout, nil)
	if len(results) > 0 {
		fmt.Println()
		fmt.Println("初始优选结果")
		printSelectionResults(results)
		fmt.Print("如需继续按更低延迟替换最高延迟候选，请输入新的延迟阈值 (留空跳过，单位毫秒): ")
		if scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input != "" {
				val, err := strconv.Atoi(input)
				if err != nil || val <= 0 {
					fmt.Println("输入无效，跳过候选替换")
				} else {
					results = replaceSlowestCandidates(ipType, useTLS, taskNum, speed, expectedDataCenter, results, val, rttTestCount, rttTestAll, rttServerName, rttTimeout)
				}
			}
		}
	}
	if len(results) > 0 {
		if err := recordValidIPs(results); err != nil {
			fmt.Println("记录有效 IP 失败:", err)
		} else {
			fmt.Println("有效 IP 已记录到", dataPath(validIPTextFile))
		}
	}
	endTime := time.Now()

	fmt.Println()
	fmt.Println("设置带宽:", bandwidth, "Mbps")
	if expectedLatency > 0 {
		fmt.Println("期望延迟:", expectedLatency, "毫秒以内")
	}
	if expectedDataCenter != "" {
		fmt.Println("期望数据中心:", expectedDataCenter)
	}
	if useTLS {
		fmt.Println("RTT TLS SNI:", rttServerName)
	}
	fmt.Println("RTT 超时:", int(rttTimeout/time.Millisecond), "毫秒")
	fmt.Println("期望个数:", expectedCount)
	if rttTestAll {
		fmt.Println("RTT 测试数量: 全部")
	} else {
		fmt.Println("RTT 测试数量:", rttTestCount)
	}
	fmt.Printf("优选结果: %d 个\n", len(results))
	printSelectionResults(results)
	fmt.Println("总计用时:", int(endTime.Sub(startTime).Seconds()), "秒")
}

// SelectionResult 优选结果
type SelectionResult struct {
	IP         string
	MaxSpeed   int
	LatencyMs  int
	DataCenter string
}

type validIPRecord struct {
	IP         string `json:"ip"`
	MaxSpeed   int    `json:"max_speed_kb"`
	LatencyMs  int    `json:"latency_ms"`
	DataCenter string `json:"data_center,omitempty"`
	UpdatedAt  string `json:"updated_at"`
}

func printSelectionResults(results []SelectionResult) {
	for i, result := range results {
		fmt.Printf("%d. 优选 IP: %s, 实测带宽: %.2f Mbps, 峰值速度: %s, 往返延迟: %d 毫秒, 数据中心: %s\n",
			i+1, result.IP, speedKBToMbps(result.MaxSpeed), formatSpeed(result.MaxSpeed), result.LatencyMs, result.DataCenter)
	}
}

func recordValidIPs(results []SelectionResult) error {
	if len(results) == 0 {
		return nil
	}

	records := loadValidIPRecords()
	recordByIP := make(map[string]validIPRecord, len(records)+len(results))
	for _, record := range records {
		if record.IP != "" {
			recordByIP[record.IP] = record
		}
	}

	now := time.Now().Format(time.RFC3339)
	for _, result := range results {
		if result.IP == "" {
			continue
		}
		record := validIPRecord{
			IP:         result.IP,
			MaxSpeed:   result.MaxSpeed,
			LatencyMs:  result.LatencyMs,
			DataCenter: result.DataCenter,
			UpdatedAt:  now,
		}
		old, ok := recordByIP[result.IP]
		if ok && !isBetterValidIPRecord(record, old) {
			old.UpdatedAt = now
			if record.DataCenter != "" {
				old.DataCenter = record.DataCenter
			}
			recordByIP[result.IP] = old
			continue
		}
		recordByIP[result.IP] = record
	}

	records = records[:0]
	for _, record := range recordByIP {
		records = append(records, record)
	}
	sortValidIPRecords(records)

	body, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(dataPath(validIPJSONFile), body, 0644); err != nil {
		return err
	}
	return os.WriteFile(dataPath(validIPTextFile), []byte(formatValidIPText(records)), 0644)
}

func loadValidIPRecords() []validIPRecord {
	body, err := os.ReadFile(dataPath(validIPJSONFile))
	if err != nil {
		return nil
	}
	var records []validIPRecord
	if err := json.Unmarshal(body, &records); err != nil {
		fmt.Println("读取有效 IP 记录失败，已重新生成:", err)
		return nil
	}
	return records
}

func isBetterValidIPRecord(candidate validIPRecord, current validIPRecord) bool {
	if candidate.LatencyMs > 0 && (current.LatencyMs <= 0 || candidate.LatencyMs < current.LatencyMs) {
		return true
	}
	if candidate.LatencyMs == current.LatencyMs && candidate.MaxSpeed > current.MaxSpeed {
		return true
	}
	return false
}

func sortValidIPRecords(records []validIPRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].LatencyMs == records[j].LatencyMs {
			if records[i].MaxSpeed == records[j].MaxSpeed {
				return records[i].IP < records[j].IP
			}
			return records[i].MaxSpeed > records[j].MaxSpeed
		}
		if records[i].LatencyMs <= 0 {
			return false
		}
		if records[j].LatencyMs <= 0 {
			return true
		}
		return records[i].LatencyMs < records[j].LatencyMs
	})
}

func formatValidIPText(records []validIPRecord) string {
	var builder strings.Builder
	builder.WriteString("IP\tLatencyMs\tMbps\tkB/s\tDataCenter\tUpdatedAt\n")
	for _, record := range records {
		builder.WriteString(fmt.Sprintf("%s\t%d\t%.2f\t%d\t%s\t%s\n",
			record.IP, record.LatencyMs, speedKBToMbps(record.MaxSpeed), record.MaxSpeed, record.DataCenter, record.UpdatedAt))
	}
	return builder.String()
}

func speedKBToMbps(speedKB int) float64 {
	return float64(speedKB) / 128
}

func formatSpeed(speedKB int) string {
	return fmt.Sprintf("%.2f Mbps (%d kB/s)", speedKBToMbps(speedKB), speedKB)
}

// cloudflareTest 核心测试逻辑
func cloudflareTest(ipType int, useTLS bool, taskNum int, speed int, expectedLatency int, expectedDataCenter string, expectedCount int, rttTestCount int, rttTestAll bool, rttServerName string, rttTimeout time.Duration, skipIPs map[string]struct{}) []SelectionResult {
	expectedDataCenter = strings.TrimSpace(expectedDataCenter)
	if expectedCount <= 0 {
		expectedCount = 1
	}

	downloadAllData()
	filename := dataPath("ips-v4.txt")
	if ipType == 6 {
		filename = dataPath("ips-v6.txt")
	}
	content, err := getFileContent(filename)
	if err != nil {
		fmt.Println("读取 IP 列表失败:", err)
		return nil
	}
	ipList := parseIPList(content)
	fmt.Printf("正在从 %d 个子网中随机生成 IP...\n", len(ipList))
	if len(ipList) == 0 {
		fmt.Println("IP 列表为空，无法执行 RTT 测试")
		return nil
	}

	sampleSize := normalizeRTTTestCount(len(ipList), rttTestCount, rttTestAll)
	if rttTestAll {
		fmt.Printf("每轮 RTT 测试数量: 全部 (%d)\n", sampleSize)
	} else {
		fmt.Printf("每轮 RTT 测试数量: %d\n", sampleSize)
	}
	rttKeepCount := rttResultKeepCount(sampleSize, expectedCount, expectedDataCenter != "")

	var selectedResults []SelectionResult
	selectedIPs := cloneIPSet(skipIPs)
	testedIPs := cloneIPSet(skipIPs)

	for {
		var rttResults []RTTResult
		for {
			sampled := randomSample(ipList, sampleSize)

			var testIPs []string
			if ipType == 6 {
				testIPs = getRandomIPv6s(sampled)
			} else {
				testIPs = getRandomIPv4s(sampled)
			}
			testIPs = filterNewIPs(testIPs, testedIPs)
			if len(testIPs) == 0 {
				fmt.Println("当前生成的 IP 都已测试过，继续生成新的测试 IP...")
				continue
			}

			fmt.Printf("已生成 %d 个测试 IP，开始 RTT 测试...\n", len(testIPs))

			rttResults = runRTTTest(testIPs, taskNum, useTLS, rttKeepCount, rttServerName, rttTimeout)
			if len(rttResults) > 0 {
				break
			}
			fmt.Println("当前所有 IP 都存在 RTT 丢包，继续新的 RTT 测试...")
		}

		if expectedLatency > 0 {
			filteredResults := filterRTTResultsByLatency(rttResults, expectedLatency)
			if len(filteredResults) == 0 {
				fmt.Printf("当前所有 IP 的 RTT 都超过期望延迟 %d 毫秒，继续新的 RTT 测试...\n", expectedLatency)
				continue
			}
			if len(filteredResults) < len(rttResults) {
				fmt.Printf("已按期望延迟 <= %d 毫秒过滤，保留 %d 个 IP\n", expectedLatency, len(filteredResults))
			}
			rttResults = filteredResults
		}

		if expectedDataCenter != "" {
			filteredResults := filterRTTResultsByDataCenter(rttResults, expectedDataCenter)
			if len(filteredResults) == 0 {
				fmt.Printf("当前所有 IP 的 RTT 数据中心都未达到期望 %s，继续新的 RTT 测试...\n", expectedDataCenter)
				continue
			}
			if len(filteredResults) < len(rttResults) {
				fmt.Printf("已按期望数据中心 %s 提前过滤，保留 %d 个 IP\n", expectedDataCenter, len(filteredResults))
			}
			rttResults = filteredResults
		}

		fmt.Println("待测速的 IP 地址")
		for _, r := range rttResults {
			if r.DataCenter != "" {
				fmt.Printf("%s 往返延迟 %d 毫秒，数据中心 %s\n", r.IP, r.LatencyMs, formatDataCenter(r.DataCenter))
			} else {
				fmt.Printf("%s 往返延迟 %d 毫秒\n", r.IP, r.LatencyMs)
			}
		}

		neededCount := expectedCount - len(selectedResults)
		speedResults := runSerialSpeedTests(rttResults, useTLS, speed, expectedDataCenter, selectedIPs, neededCount)
		if len(speedResults) > 0 {
			if err := recordValidIPs(speedResults); err != nil {
				fmt.Println("记录本轮有效 IP 失败:", err)
			} else {
				fmt.Printf("本轮 %d 个有效 IP 已记录到 %s\n", len(speedResults), dataPath(validIPTextFile))
			}
		}
		for _, result := range speedResults {
			selectedResults = append(selectedResults, result)
			selectedIPs[result.IP] = struct{}{}
			fmt.Printf("已找到 %d/%d 个符合期望条件的 IP\n", len(selectedResults), expectedCount)
			if len(selectedResults) >= expectedCount {
				return selectedResults
			}
		}
		fmt.Printf("当前已找到 %d/%d 个符合期望条件的 IP，重新开始新一轮测试...\n", len(selectedResults), expectedCount)
	}
}

// replaceSlowestCandidates 按新延迟阈值替换当前结果中延迟最高的候选
func replaceSlowestCandidates(ipType int, useTLS bool, taskNum int, speed int, expectedDataCenter string, results []SelectionResult, maxLatency int, rttTestCount int, rttTestAll bool, rttServerName string, rttTimeout time.Duration) []SelectionResult {
	if len(results) == 0 {
		return results
	}

	skippedIPs := selectionIPSet(results)
	for {
		worstIndex := highestLatencyIndex(results)
		if worstIndex < 0 || results[worstIndex].LatencyMs <= maxLatency {
			fmt.Printf("当前所有候选延迟均已 <= %d 毫秒\n", maxLatency)
			return results
		}

		worst := results[worstIndex]
		fmt.Printf("当前最高延迟候选为 %s (%d 毫秒)，继续寻找 <= %d 毫秒的替换 IP...\n", worst.IP, worst.LatencyMs, maxLatency)
		replacements := cloudflareTest(ipType, useTLS, taskNum, speed, maxLatency, expectedDataCenter, 1, rttTestCount, rttTestAll, rttServerName, rttTimeout, skippedIPs)
		if len(replacements) == 0 {
			return results
		}

		fmt.Printf("替换候选: %s (%d 毫秒) -> %s (%d 毫秒)\n", worst.IP, worst.LatencyMs, replacements[0].IP, replacements[0].LatencyMs)
		skippedIPs[worst.IP] = struct{}{}
		skippedIPs[replacements[0].IP] = struct{}{}
		results[worstIndex] = replacements[0]
	}
}

func runSerialSpeedTests(rttResults []RTTResult, useTLS bool, expectedSpeed int, expectedDataCenter string, selectedIPs map[string]struct{}, neededCount int) []SelectionResult {
	if neededCount <= 0 || len(rttResults) == 0 {
		return nil
	}

	speedPort := 80
	if useTLS {
		speedPort = 443
	}

	var selectedResults []SelectionResult
	acceptedIPs := cloneIPSet(selectedIPs)
	fmt.Printf("开始单线程速度测试，候选数 %d\n", len(rttResults))
	for _, r := range rttResults {
		if _, ok := acceptedIPs[r.IP]; ok {
			fmt.Printf("%s 已在优选结果中，跳过重复测速\n", r.IP)
			continue
		}

		maxSpeed, _, dc := runSpeedTestSimple(context.Background(), r.IP, speedPort, useTLS)
		fmt.Printf("%s 峰值速度 %s", r.IP, formatSpeed(maxSpeed))
		if dc != "" {
			fmt.Printf(", 数据中心 %s", formatDataCenter(dc))
		}
		if expectedDataCenter != "" && !matchesDataCenter(dc, expectedDataCenter) {
			fmt.Printf(", 未达到期望数据中心 %s", expectedDataCenter)
			fmt.Println()
			continue
		}
		fmt.Println()

		if maxSpeed < expectedSpeed {
			continue
		}
		acceptedIPs[r.IP] = struct{}{}

		dataCenter := dc
		if dataCenter != "" {
			dataCenter = formatDataCenter(dataCenter)
		}
		selectedResults = append(selectedResults, SelectionResult{
			IP:         r.IP,
			MaxSpeed:   maxSpeed,
			LatencyMs:  r.LatencyMs,
			DataCenter: dataCenter,
		})
	}

	sort.Slice(selectedResults, func(i, j int) bool {
		if selectedResults[i].MaxSpeed == selectedResults[j].MaxSpeed {
			return selectedResults[i].LatencyMs < selectedResults[j].LatencyMs
		}
		return selectedResults[i].MaxSpeed > selectedResults[j].MaxSpeed
	})

	if len(selectedResults) > neededCount {
		selectedResults = selectedResults[:neededCount]
	}
	return selectedResults
}

func highestLatencyIndex(results []SelectionResult) int {
	if len(results) == 0 {
		return -1
	}
	worstIndex := 0
	for i := 1; i < len(results); i++ {
		if results[i].LatencyMs > results[worstIndex].LatencyMs {
			worstIndex = i
		}
	}
	return worstIndex
}

func selectionIPSet(results []SelectionResult) map[string]struct{} {
	ipSet := make(map[string]struct{}, len(results))
	for _, result := range results {
		if result.IP != "" {
			ipSet[result.IP] = struct{}{}
		}
	}
	return ipSet
}

func cloneIPSet(ipSet map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(ipSet))
	for ip := range ipSet {
		cloned[ip] = struct{}{}
	}
	return cloned
}

func filterNewIPs(ipList []string, testedIPs map[string]struct{}) []string {
	newIPs := make([]string, 0, len(ipList))
	for _, ip := range ipList {
		if ip == "" {
			continue
		}
		if _, ok := testedIPs[ip]; ok {
			continue
		}
		testedIPs[ip] = struct{}{}
		newIPs = append(newIPs, ip)
	}
	return newIPs
}

// normalizeRTTTestCount 计算每轮 RTT 测试数量
func normalizeRTTTestCount(total int, requested int, all bool) int {
	if total <= 0 {
		return 0
	}
	if all {
		return total
	}
	if requested <= 0 {
		requested = 100
	}
	if requested > total {
		return total
	}
	return requested
}

func rttResultKeepCount(sampleSize int, expectedCount int, hasExpectedDataCenter bool) int {
	if sampleSize <= 0 {
		return 0
	}
	if hasExpectedDataCenter {
		return sampleSize
	}
	keepCount := expectedCount * 3
	if keepCount < 10 {
		keepCount = 10
	}
	if keepCount > sampleSize {
		return sampleSize
	}
	return keepCount
}

// filterRTTResultsByLatency 按最大 RTT 延迟过滤测试结果
func filterRTTResultsByLatency(results []RTTResult, maxLatency int) []RTTResult {
	if maxLatency <= 0 {
		return results
	}

	filtered := make([]RTTResult, 0, len(results))
	for _, result := range results {
		if result.LatencyMs <= maxLatency {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

// filterRTTResultsByDataCenter 按 RTT 阶段拿到的数据中心提前过滤测试结果
func filterRTTResultsByDataCenter(results []RTTResult, expectedDataCenter string) []RTTResult {
	if strings.TrimSpace(expectedDataCenter) == "" {
		return results
	}

	filtered := make([]RTTResult, 0, len(results))
	for _, result := range results {
		if matchesDataCenter(result.DataCenter, expectedDataCenter) {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

// randomSample 从列表中随机抽取 n 个元素
func randomSample(list []string, n int) []string {
	shuffled := make([]string, len(list))
	copy(shuffled, list)
	randomMu.Lock()
	randomGenerator.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	randomMu.Unlock()
	if n > len(shuffled) {
		n = len(shuffled)
	}
	return shuffled[:n]
}

// RTTResult RTT 测试结果
type RTTResult struct {
	IP         string
	LatencyMs  int
	DataCenter string
}

// runRTTTest 运行 RTT 测试（并发，带进度显示）
func runRTTTest(ipList []string, taskNum int, useTLS bool, keepCount int, rttServerName string, rttTimeout time.Duration) []RTTResult {
	if len(ipList) < taskNum {
		taskNum = len(ipList)
	}

	var wg sync.WaitGroup
	resultChan := make(chan RTTResult, len(ipList))
	thread := make(chan struct{}, taskNum)
	var count int
	var mu sync.Mutex
	total := len(ipList)

	for _, ip := range ipList {
		wg.Add(1)
		thread <- struct{}{}
		go func(ip string) {
			defer func() {
				<-thread
				wg.Done()
				mu.Lock()
				count++
				current := count
				mu.Unlock()
				if current%10 == 0 || current == total {
					fmt.Printf("RTT 测试进度: %d/%d\n", current, total)
				}
			}()

			avgMs, dc := testRTT(ip, useTLS, rttServerName, rttTimeout)
			if avgMs > 0 {
				resultChan <- RTTResult{IP: ip, LatencyMs: avgMs, DataCenter: dc}
			}
		}(ip)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var results []RTTResult
	for r := range resultChan {
		results = append(results, r)
	}

	// 按最小延迟排序，保留指定数量进入后续过滤和速度测试
	sort.Slice(results, func(i, j int) bool {
		return results[i].LatencyMs < results[j].LatencyMs
	})

	if keepCount <= 0 {
		keepCount = 10
	}
	if len(results) > keepCount {
		fmt.Printf("RTT 测试完成，%d/%d 个 IP 有效，保留延迟最低的 %d 个\n", len(results), total, keepCount)
		results = results[:keepCount]
	} else {
		fmt.Printf("RTT 测试完成，%d/%d 个 IP 有效\n", len(results), total)
	}
	return results
}

// testRTT 测试单个 IP 的 RTT（TCP 连接 + 验证 CF-RAY）
// 连续 3 次取 TCP 连接时间，取平均延迟，中间任何一次失败直接丢弃
func testRTT(ip string, useTLS bool, rttServerName string, rttTimeout time.Duration) (int, string) {
	port := 80
	if useTLS {
		port = 443
	}
	if !isValidServerName(rttServerName) {
		rttServerName = defaultRTTServerName
	}
	if rttTimeout < minRTTTimeout {
		rttTimeout = minRTTTimeout
	}

	var totalMs int
	var dataCenter string
	for range 3 {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), rttTimeout)
		if err != nil {
			return 0, ""
		}
		tcpDuration := time.Since(start)

		conn.SetDeadline(start.Add(rttTimeout))

		var rwc net.Conn = conn
		if useTLS {
			tlsConn := tls.Client(conn, &tls.Config{ServerName: rttServerName, InsecureSkipVerify: true})
			if err := tlsConn.Handshake(); err != nil {
				conn.Close()
				return 0, ""
			}
			rwc = tlsConn
		}

		reqStr := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0\r\nConnection: close\r\n\r\n", rttServerName)
		_, err = rwc.Write([]byte(reqStr))
		if err != nil {
			rwc.Close()
			return 0, ""
		}

		reader := bufio.NewReader(rwc)
		resp, err := http.ReadResponse(reader, nil)
		rwc.Close()
		if err != nil {
			return 0, ""
		}
		resp.Body.Close()

		dc := extractDataCenter(resp.Header.Get("CF-RAY"))
		if dc == "" {
			return 0, ""
		}
		dataCenter = dc

		totalMs += int(tcpDuration.Milliseconds())
	}

	return totalMs / 3, dataCenter
}

// runSpeedTestSimple 简单速度测试，返回 (峰值速度 kB/s, TCP延迟ms, 三字码头)
func runSpeedTestSimple(ctx context.Context, ip string, port int, useTLS bool) (int, int, string) {
	var tcpMs int
	dialer := &net.Dialer{Timeout: speedDialTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			start := time.Now()
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
			if err == nil {
				tcpMs = int(time.Since(start).Milliseconds())
			}
			return conn, err
		},
	}
	if useTLS {
		transport.TLSClientConfig = &tls.Config{ServerName: speedTestDomain}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   speedTestTimeout,
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	testURL := fmt.Sprintf("%s://%s/%s", scheme, speedTestDomain, speedTestFile)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return 0, 0, ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, ""
	}
	defer resp.Body.Close()

	cfRay := resp.Header.Get("CF-RAY")
	dataCenter := extractDataCenter(cfRay)

	buf := make([]byte, 32*1024)
	var totalBytes int64
	var windowBytes int64
	windowStart := time.Now()
	maxSpeed := 0

	for {
		n, err := resp.Body.Read(buf)
		totalBytes += int64(n)
		windowBytes += int64(n)
		if err != nil {
			break
		}

		elapsed := time.Since(windowStart).Seconds()
		if elapsed >= 1.0 {
			speedKB := int(float64(windowBytes) / 1024 / elapsed)
			if speedKB > maxSpeed {
				maxSpeed = speedKB
			}
			windowBytes = 0
			windowStart = time.Now()
		}
	}

	// 最后一个不满 1 秒的窗口不参与峰值计算，避免时间过短导致速度虚高

	return maxSpeed, tcpMs, dataCenter
}

// extractDataCenter 从 CF-RAY 头提取三字码头
func extractDataCenter(cfRay string) string {
	if cfRay == "" {
		return ""
	}
	parts := strings.Split(cfRay, "-")
	if len(parts) < 2 {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
}

// lookupDataCenter 查找数据中心名称
func lookupDataCenter(colo string) string {
	locationMu.RLock()
	loc := locationMap[colo]
	locationMu.RUnlock()

	if loc.City != "" {
		return loc.City
	}
	return colo
}

// formatDataCenter 返回城市名和三字码，方便用户核对期望数据中心
func formatDataCenter(colo string) string {
	colo = strings.ToUpper(strings.TrimSpace(colo))
	if colo == "" {
		return ""
	}

	name := lookupDataCenter(colo)
	if name == "" || strings.EqualFold(name, colo) {
		return colo
	}
	return fmt.Sprintf("%s (%s)", name, colo)
}

// matchesDataCenter 判断 CF 数据中心是否符合用户期望位置
func matchesDataCenter(colo string, expected string) bool {
	expected = normalizeDataCenterText(expected)
	if expected == "" {
		return true
	}

	colo = strings.ToUpper(strings.TrimSpace(colo))
	if colo == "" {
		return false
	}

	locationMu.RLock()
	loc := locationMap[colo]
	locationMu.RUnlock()

	candidates := []string{colo, loc.Iata, loc.City, loc.Region, loc.Cca2}
	for _, candidate := range candidates {
		normalizedCandidate := normalizeDataCenterText(candidate)
		if normalizedCandidate == "" {
			continue
		}
		if len(expected) <= 3 {
			if normalizedCandidate == expected {
				return true
			}
			continue
		}
		if normalizedCandidate == expected || strings.Contains(normalizedCandidate, expected) {
			return true
		}
	}
	return false
}

func normalizeDataCenterText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "", "\t", "", "-", "", "_", "")
	return replacer.Replace(value)
}

// runSingleSpeedTest 单 IP 测速
func runSingleSpeedTest(useTLS bool) {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("请输入需要测速的 IP: ")
	if !scanner.Scan() {
		return
	}
	ip := strings.TrimSpace(scanner.Text())

	defaultPort := 80
	if useTLS {
		defaultPort = 443
	}

	fmt.Printf("请输入需要测速的端口 (默认%d): ", defaultPort)
	var port int
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			port = defaultPort
		} else {
			val, err := strconv.Atoi(input)
			if err != nil || val <= 0 {
				fmt.Printf("输入无效，已使用默认端口 %d\n", defaultPort)
				port = defaultPort
			} else {
				port = val
			}
		}
	} else {
		port = defaultPort
	}

	fmt.Printf("正在测速 %s 端口 %d\n", ip, port)

	speedKB, tcpMs, dc := runSpeedTestSimple(context.Background(), ip, port, useTLS)
	if dc != "" {
		fmt.Printf("%s 峰值速度 %s, TCP延迟 %dms, 数据中心=%s\n", ip, formatSpeed(speedKB), tcpMs, formatDataCenter(dc))
	} else {
		fmt.Printf("%s 峰值速度 %s, TCP延迟 %dms\n", ip, formatSpeed(speedKB), tcpMs)
	}
}

// clearCache 清空缓存，删除所有数据文件，下次运行重新下载
func clearCache() {
	for _, f := range []string{"locations.json", "ips-v4.txt", "ips-v6.txt", "url.txt"} {
		os.Remove(dataPath(f))
	}
	fmt.Println("缓存已清空，下次操作会自动重新下载数据")
}

// updateData 重新下载所有数据
func updateData() {
	fmt.Println("正在重新下载数据...")
	for _, f := range []string{"locations.json", "ips-v4.txt", "ips-v6.txt", "url.txt"} {
		os.Remove(dataPath(f))
	}
	initLocations()
}

// ----------------------- 工具函数 -----------------------

const (
	defaultRTTServerName = "cloudflare-ech.com"
	validIPJSONFile      = "valid-ips.json"
	validIPTextFile      = "valid-ips.txt"
	defaultRTTTimeout    = 250 * time.Millisecond
	minRTTTimeout        = 100 * time.Millisecond
	minRTTTimeoutMs      = 100
	speedDialTimeout     = 800 * time.Millisecond
	speedTestTimeout     = 4 * time.Second
)

func isValidServerName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 253 || strings.ContainsAny(value, " /\\\t\r\n:") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

var (
	dataDir         string
	randomMu        sync.Mutex
	randomGenerator = rand.New(rand.NewSource(time.Now().UnixNano()))
	locationMap     map[string]location
	locationMu      sync.RWMutex
	speedTestDomain string
	speedTestFile   string
)

type location struct {
	Iata   string  `json:"iata"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Cca2   string  `json:"cca2"`
	Region string  `json:"region"`
	City   string  `json:"city"`
}

func dataPath(name string) string {
	if dataDir == "" {
		return name
	}
	return filepath.Join(dataDir, name)
}

var downloadClient = &http.Client{Timeout: 30 * time.Second}

func getURLContent(targetURL string) (string, error) {
	resp, err := downloadClient.Get(targetURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func getFileContent(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func saveToFile(filename, content string) error {
	dir := filepath.Dir(filename)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return os.WriteFile(filename, []byte(content), 0644)
}

func parseIPList(content string) []string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var ipList []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			ipList = append(ipList, line)
		}
	}
	return ipList
}

func nextRandomIntn(n int) int {
	randomMu.Lock()
	defer randomMu.Unlock()
	return randomGenerator.Intn(n)
}

func getRandomIPv4s(ipList []string) []string {
	var randomIPs []string
	for _, subnet := range ipList {
		subnet = strings.TrimSpace(subnet)
		if subnet == "" {
			continue
		}
		if idx := strings.Index(subnet, "/"); idx >= 0 {
			subnet = subnet[:idx]
		}
		octets := strings.Split(subnet, ".")
		if len(octets) == 4 {
			octets[3] = fmt.Sprintf("%d", nextRandomIntn(256))
			randomIPs = append(randomIPs, strings.Join(octets, "."))
		}
	}
	return randomIPs
}

func getRandomIPv6s(ipList []string) []string {
	var randomIPs []string
	for _, subnet := range ipList {
		subnet = strings.TrimSpace(subnet)
		if subnet == "" {
			continue
		}
		if idx := strings.Index(subnet, "/"); idx >= 0 {
			subnet = subnet[:idx]
		}
		// 展开 :: 压缩，确保有 8 段
		if strings.Contains(subnet, "::") {
			parts := strings.Split(subnet, "::")
			left := strings.Split(parts[0], ":")
			var right []string
			if len(parts) > 1 && parts[1] != "" {
				right = strings.Split(parts[1], ":")
			}
			missing := 8 - len(left) - len(right)
			sections := left
			for range missing {
				sections = append(sections, "0")
			}
			sections = append(sections, right...)
			subnet = strings.Join(sections, ":")
		}
		sections := strings.Split(subnet, ":")
		if len(sections) >= 3 {
			sections = sections[:3]
			for i := 3; i < 8; i++ {
				sections = append(sections, fmt.Sprintf("%x", nextRandomIntn(65536)))
			}
			randomIPs = append(randomIPs, strings.Join(sections, ":"))
		}
	}
	return randomIPs
}

// downloadAllData 确保所有数据文件存在，缺失则自动下载
func downloadAllData() {
	urlFilename := dataPath("url.txt")
	if _, err := os.Stat(urlFilename); os.IsNotExist(err) {
		fmt.Println("本地", urlFilename, "不存在，正在下载...")
		content, err := getURLContent("https://www.baipiao.eu.org/cloudflare/url")
		if err != nil {
			fmt.Println("下载测速 URL 失败:", err)
			return
		}
		if err := saveToFile(urlFilename, content); err != nil {
			fmt.Println("保存测速 URL 失败:", err)
			return
		}
	}

	content, err := getFileContent(urlFilename)
	if err != nil {
		fmt.Println("读取测速 URL 失败:", err)
		return
	}
	content = strings.TrimSpace(content)
	parts := strings.SplitN(content, "/", 2)
	if len(parts) == 2 {
		speedTestDomain = parts[0]
		speedTestFile = parts[1]
	} else {
		fmt.Println("测速 URL 格式异常")
	}

	for _, item := range []struct{ file, url string }{
		{"ips-v4.txt", "https://www.baipiao.eu.org/cloudflare/ips-v4"},
		{"ips-v6.txt", "https://www.baipiao.eu.org/cloudflare/ips-v6"},
	} {
		fp := dataPath(item.file)
		if _, err := os.Stat(fp); os.IsNotExist(err) {
			fmt.Println("本地", fp, "不存在，正在下载...")
			c, err := getURLContent(item.url)
			if err != nil {
				fmt.Println("下载 IP 列表失败:", err)
				return
			}
			if err := saveToFile(fp, c); err != nil {
				fmt.Println("保存 IP 列表失败:", err)
				return
			}
		}
	}

	fp := dataPath("locations.json")
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		fmt.Println("本地", fp, "不存在，正在下载...")
		resp, err := downloadClient.Get("https://www.baipiao.eu.org/cloudflare/locations")
		if err != nil {
			fmt.Println("获取位置信息失败:", err)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
		resp.Body.Close()
		if err != nil {
			fmt.Println("读取响应内容失败:", err)
			return
		}
		if err := saveToFile(fp, string(body)); err != nil {
			fmt.Println("保存位置信息失败:", err)
			return
		}
	}
}

// initLocations 初始化数据中心位置信息
func initLocations() {
	downloadAllData()

	fp := dataPath("locations.json")
	body, err := os.ReadFile(fp)
	if err != nil {
		fmt.Println("读取位置文件失败:", err)
		return
	}

	var locations []location
	if err := json.Unmarshal(body, &locations); err != nil {
		fmt.Println("解析位置信息 JSON 失败:", err)
		return
	}

	loadedMap := make(map[string]location)
	for _, loc := range locations {
		iata := strings.ToUpper(strings.TrimSpace(loc.Iata))
		if iata == "" {
			continue
		}
		loc.Iata = iata
		loadedMap[iata] = loc
	}

	locationMu.Lock()
	locationMap = loadedMap
	locationMu.Unlock()

	fmt.Printf("已加载 %d 个数据中心位置信息\n", len(loadedMap))
}
