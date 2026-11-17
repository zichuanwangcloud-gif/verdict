// verdict：一个二进制两个角色。
//
//	verdict server            启动后端（API + 前端 + agent WebSocket）
//	verdict agent init ...    在运行时上写入后端地址与令牌
//	verdict agent run         在运行时上常驻，领取并执行测试
//	verdict agent probe       打印本机 harness 探测结果
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"verdict/internal/agent"
	"verdict/internal/protocol"
	"verdict/internal/server"
	"verdict/internal/server/store"
)

func usage() {
	fmt.Fprintln(os.Stderr, `用法:
  verdict server [-addr :8791] [-db data/verdict.db] [-web web/dist] [-bin dist]
  verdict agent init --server <url> --token <token> [--dir ~/.verdict-agent] [--path $PATH]
  verdict agent run [--dir ~/.verdict-agent]
  verdict agent probe [--dir ~/.verdict-agent]
  verdict version`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "server":
		runServer(os.Args[2:])
	case "agent":
		if len(os.Args) < 3 {
			usage()
		}
		runAgent(os.Args[2], os.Args[3:])
	case "version", "--version", "-v":
		fmt.Println(protocol.Version)
	default:
		usage()
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	addr := fs.String("addr", env("VERDICT_ADDR", "127.0.0.1:8791"), "监听地址（镜像内默认 :8791）")
	dbPath := fs.String("db", env("VERDICT_DB", "data/verdict.db"), "数据库文件")
	webDir := fs.String("web", env("VERDICT_WEB_DIR", "web/dist"), "前端构建产物目录")
	binDir := fs.String("bin", env("VERDICT_BIN_DIR", "dist"), "供运行时下载的 agent 二进制目录")
	fs.Parse(args)

	token := os.Getenv("VERDICT_ADMIN_TOKEN")
	if token == "" {
		token = "vad_" + store.RandomHex(16)
		log.Printf("未设置 VERDICT_ADMIN_TOKEN，本次启动的临时管理员令牌：%s", token)
		log.Printf("（重启后会变化，生产环境请显式设置 VERDICT_ADMIN_TOKEN）")
	}

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	srv := server.New(server.Config{AdminToken: token, WebDir: *webDir, BinDir: *binDir}, st)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Hub().Watchdog(ctx)

	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Printf("verdict %s 已启动: http://%s", protocol.Version, *addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func runAgent(cmd string, args []string) {
	fs := flag.NewFlagSet("agent "+cmd, flag.ExitOnError)
	dir := fs.String("dir", agent.DefaultDir(), "agent 数据目录")
	srvURL := fs.String("server", "", "后端地址（init）")
	token := fs.String("token", "", "运行时令牌（init）")
	path := fs.String("path", "", "后台服务使用的 PATH（init，默认取当前环境）")
	fs.Parse(args)

	switch cmd {
	case "init":
		p := *path
		if p == "" {
			p = os.Getenv("PATH")
		}
		if err := agent.SaveConfig(*dir, agent.Config{Server: *srvURL, Token: *token, Path: p}); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("已写入 %s\n", filepath.Join(*dir, "config.json"))
	case "probe":
		if c, err := agent.LoadConfig(*dir); err == nil && c.Path != "" {
			os.Setenv("PATH", c.Path)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		b, _ := json.MarshalIndent(agent.Probe(ctx), "", "  ")
		fmt.Println(string(b))
	case "run":
		a, err := agent.New(*dir)
		if err != nil {
			log.Fatal(err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		log.Printf("verdict agent %s 启动，数据目录 %s", protocol.Version, *dir)
		a.Run(ctx)
		// 给正在执行的测试一点时间写入结果。
		time.Sleep(2 * time.Second)
	default:
		usage()
	}
}
