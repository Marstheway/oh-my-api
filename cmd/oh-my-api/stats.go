package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"github.com/Marstheway/oh-my-api/internal/stats"
)

func runStats(configPath string, args []string) {
	if hasResetFlag(args) {
		confirmAndReset(configPath)
		return
	}

	since, until := parseDateRange(args)

	dbPath := getDatabasePath(configPath)
	if err := stats.Init(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to init database: %v\n", err)
		os.Exit(1)
	}
	defer stats.Close()

	q := stats.GetQuerier()

	earliestDate, _ := q.QueryEarliestDate()

	total, err := q.QueryTotal(since, until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: query failed: %v\n", err)
		os.Exit(1)
	}

	keys, err := q.QueryByKeys(since, until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: query by keys failed: %v\n", err)
		os.Exit(1)
	}

	providers, err := q.QueryByProviders(since, until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: query by providers failed: %v\n", err)
		os.Exit(1)
	}

	providerOnly, err := q.QueryByProviderOnly(since, until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: query by provider only failed: %v\n", err)
		os.Exit(1)
	}

	printStats(total, keys, providerOnly, providers, since, until, earliestDate)
}

func hasResetFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--reset" {
			return true
		}
	}
	return false
}

func confirmAndReset(configPath string) {
	dbPath := getDatabasePath(configPath)

	fmt.Println("警告: 此操作将清空所有统计数据，且不可恢复！")
	fmt.Printf("数据库路径: %s\n", dbPath)
	fmt.Print("确认清空？请输入 'yes' 继续: ")

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input != "yes" {
		fmt.Println("操作已取消")
		return
	}

	if err := stats.Init(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to init database: %v\n", err)
		os.Exit(1)
	}
	defer stats.Close()

	if err := stats.ClearStats(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to reset stats: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("统计数据已清空")
}

func parseDateRange(args []string) (string, string) {
	var since, until string
	now := time.Now().UTC()

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--today":
			since = now.Format("2006-01-02")
			until = since
		case "--week":
			since = now.AddDate(0, 0, -6).Format("2006-01-02")
			until = now.Format("2006-01-02")
		case "--month":
			since = now.AddDate(0, 0, -29).Format("2006-01-02")
			until = now.Format("2006-01-02")
		case "--since":
			if i+1 < len(args) {
				since = args[i+1]
				i++
			}
		case "--until":
			if i+1 < len(args) {
				until = args[i+1]
				i++
			}
		}
	}

	return since, until
}

func getDatabasePath(configPath string) string {
	cfg, err := loadConfigQuiet(configPath)
	if err != nil || cfg.Database.Path == "" {
		return "./data/oh-my-api.db"
	}
	return cfg.Database.Path
}

func printStats(total *stats.TotalStats, keys map[string]*stats.KeyStats, providerOnly map[string]*stats.ProviderOnlyStats, providers map[string]*stats.ProviderStats, since, until, earliestDate string) {
	fmt.Printf("=== 统计范围: %s ===\n", formatTimeRange(since, until, earliestDate))
	fmt.Println()

	fmt.Println("=== 总调用量 ===")
	fmt.Printf("请求次数: %s\n", formatNumber(total.RequestCount))
	fmt.Printf("输入 Token: %s\n", formatNumber(total.InputTokens))
	fmt.Printf("输出 Token: %s\n", formatNumber(total.OutputTokens))

	avgLatency := float64(0)
	throughput := float64(0)
	if total.RequestCount > 0 {
		avgLatency = float64(total.LatencyMs) / float64(total.RequestCount) / 1000 // 转换为秒
	}
	if total.LatencyMs > 0 {
		throughput = float64(total.OutputTokens) / float64(total.LatencyMs) * 1000
	}
	fmt.Printf("平均延迟: %.2f s\n", avgLatency)
	fmt.Printf("吞吐量: %.1f tokens/s\n", throughput)
	fmt.Println()

	fmt.Println("=== 按 Key 统计 ===")
	fmt.Printf("%-20s %15s %15s %12s %12s\n", "Key", "输入 Token", "输出 Token", "请求次数", "平均延迟")
	for name, s := range keys {
		avgLat := float64(0)
		if s.RequestCount > 0 {
			avgLat = float64(s.LatencyMs) / float64(s.RequestCount) / 1000 // 转换为秒
		}
		fmt.Printf("%-20s %15s %15s %12s %10ss\n",
			truncate(name, 20),
			formatNumber(s.InputTokens),
			formatNumber(s.OutputTokens),
			formatNumber(s.RequestCount),
			fmt.Sprintf("%.2f", avgLat),
		)
	}
	fmt.Println()

	fmt.Println("=== 按 Provider 统计 ===")
	fmt.Printf("%-20s %15s %15s %12s %12s %12s\n",
		"Provider", "输入 Token", "输出 Token", "吞吐量", "平均延迟", "请求次数")

	for name, s := range providerOnly {
		throughput := float64(0)
		avgLat := float64(0)
		if s.LatencyMs > 0 {
			throughput = float64(s.OutputTokens) / float64(s.LatencyMs) * 1000
		}
		if s.RequestCount > 0 {
			avgLat = float64(s.LatencyMs) / float64(s.RequestCount) / 1000 // 转换为秒
		}
		fmt.Printf("%-20s %15s %15s %11s/s %10ss %12s\n",
			truncate(name, 20),
			formatNumber(s.InputTokens),
			formatNumber(s.OutputTokens),
			fmt.Sprintf("%.1f", throughput),
			fmt.Sprintf("%.2f", avgLat),
			formatNumber(s.RequestCount),
		)
	}
	fmt.Println()

	fmt.Println("=== 按 Provider/Model 统计 ===")
	fmt.Printf("%-30s %15s %15s %12s %12s %12s\n",
		"Provider/Model", "输入 Token", "输出 Token", "吞吐量", "平均延迟", "请求次数")

	for key, s := range providers {
		throughput := float64(0)
		avgLat := float64(0)
		if s.LatencyMs > 0 {
			throughput = float64(s.OutputTokens) / float64(s.LatencyMs) * 1000
		}
		if s.RequestCount > 0 {
			avgLat = float64(s.LatencyMs) / float64(s.RequestCount) / 1000 // 转换为秒
		}
		fmt.Printf("%-30s %15s %15s %11s/s %10ss %12s\n",
			truncate(key, 30),
			formatNumber(s.InputTokens),
			formatNumber(s.OutputTokens),
			fmt.Sprintf("%.1f", throughput),
			fmt.Sprintf("%.2f", avgLat),
			formatNumber(s.RequestCount),
		)
	}
}

func formatTimeRange(since, until, earliestDate string) string {
	if since == "" && until == "" {
		if earliestDate == "" {
			return "无数据"
		}
		return fmt.Sprintf("%s ~ 至今", earliestDate)
	}
	if since == "" {
		return fmt.Sprintf("截至 %s", until)
	}
	if until == "" {
		return fmt.Sprintf("从 %s 起", since)
	}
	if since == until {
		return since
	}
	return fmt.Sprintf("%s ~ %s", since, until)
}

func formatNumber(n int64) string {
	s := strconv.FormatInt(n, 10)
	var result []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, c)
	}
	return string(result)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func loadConfigQuiet(path string) (*databaseConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg databaseConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

type databaseConfig struct {
	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`
}