package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dominant-strategies/go-quai/quaiclient/ethclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

// connectRPC 尝试连接到 RPC，并实现重试机制
func connectRPC(rpcURL string) (*ethclient.Client, error) {
	var client *ethclient.Client
	var err error
	for i := 0; i < 5; i++ { // 最多重试 5 次
		client, err = ethclient.Dial(rpcURL)
		if err == nil {
			log.Println("成功连接到 RPC")
			return client, nil
		}
		log.Printf("连接 RPC 失败（尝试 %d 次）：%v", i+1, err)
		time.Sleep(5 * time.Second)
	}
	return nil, fmt.Errorf("多次尝试后无法连接 RPC：%v", err)
}

func main() {
	// 命令行参数
	rpc := flag.String("rpc", "", "区块链的 RPC URL")
	interval := flag.Int("interval", 3, "轮询间隔（秒）")
	pushGateway := flag.String("pushgateway", "", "Pushgateway 地址")
	flag.Parse()

	// 检查必需的参数
	if *rpc == "" || *pushGateway == "" {
		log.Fatalf("rpc 和 pushgateway 参数是必需的")
	}

	// 连接到区块链 RPC
	client, err := connectRPC(*rpc)
	if err != nil {
		log.Fatalf("连接 RPC 失败：%v", err)
	}
	// 使用 defer 正确关闭客户端，假设 Close() 无返回值
	defer client.Close()

	// 设置 Prometheus 指标
	difficultyGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "quai_network_current_block_difficulty",
		Help: "当前区块的难度",
	})
	prometheus.MustRegister(difficultyGauge)

	// 设置定时器
	ticker := time.NewTicker(time.Duration(*interval) * time.Second)
	defer ticker.Stop()

	// 设置信号处理以实现优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// 主循环
	for {
		select {
		case <-ticker.C:
			// 创建带有超时的上下文
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

			// 获取当前区块号
			currentBlockNumber, err := client.BlockNumber(ctx)
			if err != nil {
				log.Printf("获取当前区块号失败：%v", err)
				cancel() // 显式取消上下文
				continue
			}

			// 将 currentBlockNumber 转换为 *big.Int
			blockNumberBig := new(big.Int).SetUint64(currentBlockNumber)

			// 获取区块头
			header, err := client.HeaderByNumber(ctx, blockNumberBig)
			if err != nil {
				log.Printf("获取区块 %d 的区块头失败：%v", currentBlockNumber, err)
				cancel() // 显式取消上下文
				continue
			}

			// 获取难度
			difficultyBig := header.Difficulty()
			if difficultyBig == nil {
				log.Printf("区块 %d 的难度信息为空", currentBlockNumber)
				cancel()
				continue
			}
			difficulty := difficultyBig.Uint64()

			// 更新指标
			difficultyGauge.Set(float64(difficulty))

			// 推送指标到 Pushgateway
			err = push.New(*pushGateway, "quai_current_difficulty").
				Collector(difficultyGauge).
				Grouping("job", "quai").
				Push()
			if err != nil {
				log.Printf("推送指标失败：%v", err)
			} else {
				log.Printf("成功推送当前区块 %d 的难度：%d", currentBlockNumber, difficulty)
			}

			// 取消上下文以释放资源
			cancel()

		case <-quit:
			log.Println("收到关闭信号，正在退出...")
			return
		}
	}
}
