package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
	"log"
	"math/big"
	"time"

	"github.com/dominant-strategies/go-quai/quaiclient/ethclient"
	_ "github.com/go-sql-driver/mysql"
)

func main() {
	// Command-line arguments
	rpc := flag.String("rpc", "", "RPC URL for the blockchain")
	dsn := flag.String("dsn", "", "Database DSN")
	interval := flag.Int("interval", 3, "Polling interval in seconds")
	startHeight := flag.Int64("start", -1, "Starting block height")
	pushGateway := flag.String("pushgateway", "", "Pushgateway address")
	flag.Parse()

	if *rpc == "" || *dsn == "" {
		log.Fatalf("rpc and dsn parameters are required")
	}

	// Connect to the blockchain RPC
	client, err := ethclient.Dial(*rpc)
	if err != nil {
		log.Fatalf("failed to connect to node: %v", err)
	}
	defer client.Close()

	// Connect to the database
	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Determine the starting block height
	var lastHeight *big.Int
	if *startHeight >= 0 {
		lastHeight = big.NewInt(*startHeight)
	} else {
		blockNumber, err := client.BlockNumber(ctx)
		if err != nil {
			log.Fatalf("failed to get block number: %v", err)
		}
		lastHeight = big.NewInt(int64(blockNumber))
	}

	log.Printf("Starting from block height: %s", lastHeight)

	// Polling loop
	ticker := time.NewTicker(time.Duration(*interval) * time.Second)
	defer ticker.Stop()


	rowCountMetric := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "quai_network_block_difficulty",
		Help: fmt.Sprintf("quai_network_block_difficulty"),
	})

	for range ticker.C {
		// Get the current block number
		currentBlockNumber, err := client.BlockNumber(ctx)
		if err != nil {
			log.Printf("failed to get block number: %v", err)
			continue
		}

		currentHeight := big.NewInt(int64(currentBlockNumber))
		if currentHeight.Cmp(lastHeight) > 0 {
			// Query all heights between lastHeight and currentHeight
			for height := new(big.Int).Add(lastHeight, big.NewInt(1)); height.Cmp(currentHeight) <= 0; height.Add(height, big.NewInt(1)) {
				header, err := client.HeaderByNumber(ctx, height)
				if err != nil {
					log.Printf("failed to get block header for height %s: %v", height, err)
					continue
				}

				// Insert block difficulty and block number into the database
				difficulty := header.WorkObjectHeader().Difficulty().Uint64()
				blockNumber := header.WorkObjectHeader().Number().Uint64()

				_, err = db.Exec(`INSERT INTO quai_block_difficulty (block_number, difficulty, timestamp) VALUES (?, ?, ?)`,
					blockNumber, difficulty, time.Now().UTC())
				if err != nil {
					log.Printf("failed to insert data for block %d: %v", blockNumber, err)
					continue
				}
				log.Printf("Inserted data for block %d: difficulty=%d", blockNumber, difficulty)

				// Update the metric
				rowCountMetric.Set(float64(difficulty))

				// Push the metric to Pushgateway
				err = push.New(*pushGateway, "quai").
					Collector(rowCountMetric).
					Grouping("job", "quai").
					Push()
				if err != nil {
					log.Printf("Failed to push metrics: %v", err)
				} else {
					log.Printf("Pushed metrics successfully: %s = %d", "quai_network_block_difficulty", difficulty)
				}



			}
			lastHeight = currentHeight
		}
	}
}
