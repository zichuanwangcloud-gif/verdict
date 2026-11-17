package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"verdict/internal/agent"
	"verdict/internal/api"
	"verdict/internal/store"
	"verdict/internal/verifier"
)

func main() {
	// 默认只听本机：验证器会在这台机器上执行命令，接口没有认证。
	addr := flag.String("addr", "127.0.0.1:8791", "监听地址")
	dbPath := flag.String("db", "data/verdict.db", "数据库文件")
	webDir := flag.String("web", "web/dist", "前端构建产物目录")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	srv := &api.Server{Store: st, Agent: agent.NewClaude(), Verifiers: verifier.Default()}
	mux := http.NewServeMux()
	mux.Handle("/api/", srv.Routes())
	if _, err := os.Stat(*webDir); err == nil {
		mux.Handle("/", http.FileServer(http.Dir(*webDir)))
	}

	log.Printf("verdict 已启动: http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
