package main

// JobNavi の API（Go）。backend/（Python・FastAPI）の置き換え。設定は環境変数で渡す（config.go）。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openPool(ctx context.Context, databaseURL string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL を読めません (%T)", err) // 例外の文字列には接続先が入りうる。型だけ
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.ConnConfig.ConnectTimeout = 15 * time.Second
	cfg.MaxConns = int32(maxConns)
	cfg.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("DB の接続プールを作れませんでした (%T)", err)
	}
	return pool, nil
}

// runCLI は道具（マイグレーション・ダミーデータ・運営用の画像の操作・索引づくり）。引数が無い・serve のときは API を起動する。
func runCLI(args []string) int {
	switch args[0] {
	case "migrate":
		return migrateMain(args[1:], os.LookupEnv)
	case "seed":
		return seedMain(args[1:], os.LookupEnv)
	case "seed-images":
		return seedImagesMain(args[1:], os.LookupEnv)
	case "images":
		return imagesMain(args[1:], os.LookupEnv)
	case "search-index":
		return searchIndexMain(args[1:], os.LookupEnv)
	case "healthcheck":
		return healthcheckMain(os.LookupEnv)
	}
	fmt.Println("usage: api [serve | migrate | seed | seed-images | images | search-index | healthcheck] …")
	return 2
}

func main() {
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		os.Exit(runCLI(os.Args[1:]))
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	cfg, err := loadConfig(os.LookupEnv)
	if err != nil {
		logger.Fatalf("設定エラー: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	pool, err := openPool(startCtx, cfg.DatabaseURL, poolMaxConns)
	if err == nil {
		err = pool.Ping(startCtx)
	}
	cancel()
	if err != nil {
		logger.Fatalf("DB に接続できません (%T)", err)
	}
	defer pool.Close()

	handler, err := newServer(cfg, pool, logger.Printf)
	if err != nil {
		logger.Fatalf("設定エラー: %v", err)
	}
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = srv.Shutdown(shutdown)
	}()
	logger.Printf("listening on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("サーバーが止まりました (%T)", err)
	}
}

// healthcheckMain は、コンテナの死活確認（シェルも curl も無いイメージで使う）。自分の /health に 1 回問い合わせて、200 なら 0。
func healthcheckMain(e env) int {
	port := strings.TrimSpace(e.str("PORT"))
	if port == "" {
		port = defaultPort
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
