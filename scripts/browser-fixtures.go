//go:build ignore

// 仅为浏览器回归准备历史摘要Token，不构建进生产程序。
package main

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	dir := flag.String("data-dir", "", "测试数据目录")
	key := flag.String("project-key", "", "测试项目key")
	flag.Parse()
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || len(scanner.Text()) != 67 {
		fmt.Fprintln(os.Stderr, "需要由标准输入提供测试Token")
		os.Exit(1)
	}
	raw := scanner.Text()
	digest := sha256.Sum256([]byte(raw))
	db, err := sql.Open("sqlite", filepath.Join(*dir, "platform.db"))
	if err != nil {
		panic("无法打开测试数据库")
	}
	defer db.Close()
	projectID := "fixture-" + *key
	tokenID := "token-" + *key
	project, _ := json.Marshal(map[string]any{"id": projectID, "key": *key, "display_name": "旧Token交接验证", "enabled": true, "revision": 1, "doc_revision": 1, "updated_at": time.Now().UTC(), "member_ids": []string{}})
	token, _ := json.Marshal(map[string]any{"id": tokenID, "project_id": projectID, "name": "历史 Agent", "prefix": raw[:11], "digest": hex.EncodeToString(digest[:]), "scopes": []string{"docs:read"}, "enabled": true, "created_at": time.Now().UTC()})
	tx, err := db.Begin()
	if err != nil {
		panic("测试事务无法开始")
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO projects VALUES(?,?)", projectID, string(project)); err != nil {
		panic("测试项目写入失败")
	}
	if _, err = tx.Exec("INSERT INTO access_tokens VALUES(?,?)", tokenID, string(token)); err != nil {
		panic("测试Token写入失败")
	}
	if err = tx.Commit(); err != nil {
		panic("测试事务提交失败")
	}
}
