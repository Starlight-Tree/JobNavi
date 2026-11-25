package main

// 検索エンジン（Meilisearch）の索引を、DB から作り直す。DB が正で、索引はその写し（search_engine.go）。backend/app/search_engine.py（reindex・status）と search_index.py の移植。
//
//	api search-index            表示中の求人を、索引に入れ直す（何度流しても同じ。入れ替えの間も検索できる）
//	api search-index --status   届くか・索引があるか・文書の数
//
// 必要な環境変数: DATABASE_URL、MEILI_URL（API キーは MEILI_API_KEY）。求人を足す・変える・非表示にしたあと（seed のあとを含む）に流す。
// 流し忘れても、非表示の求人は出ない（表示中かは SQL が決める）。新しい求人が検索に出ないだけ。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	engineBatch       = 1000
	engineTaskTimeout = 60 * time.Second
)

// engineSettings は、索引の設定（Python の SETTINGS と同じ）。言語は日本語と決める。自動判定だと、漢字だけの語（「温室」）が中国語と判定され、
// 日本語として切った文書と食い違って 0 件になる（実機で確認した）。
func engineSettings() map[string]any {
	return map[string]any{
		"searchableAttributes": engineSearchable,
		"displayedAttributes":  []string{"id"}, // 返すのは id だけ。文は、DB から引く
		"filterableAttributes": []string{},
		"sortableAttributes":   []string{},
		"pagination":           map[string]any{"maxTotalHits": engineSearchLimit},
		"localizedAttributes":  []map[string]any{{"locales": []string{engineLocale}, "attributePatterns": []string{"*"}}},
	}
}

// wait は書き込みの処理（タスク）が終わるまで待つ。失敗・時間切れは engineError。
func (e *searchEngine) wait(ctx context.Context, task any) error {
	obj, _ := task.(map[string]any)
	num, ok := obj["taskUid"]
	if !ok {
		return engErr("検索エンジンの応答を読めません")
	}
	uid, ok := num.(interface{ Int64() (int64, error) })
	if !ok {
		return engErr("検索エンジンの応答を読めません")
	}
	id, err := uid.Int64()
	if err != nil {
		return engErr("検索エンジンの応答を読めません")
	}
	deadline := time.Now().Add(engineTaskTimeout)
	delay := 50 * time.Millisecond
	for {
		info, _, err := e.call(ctx, http.MethodGet, fmt.Sprintf("/tasks/%d", id), nil)
		if err != nil {
			return err
		}
		m, _ := info.(map[string]any)
		status, _ := m["status"].(string)
		switch status {
		case "succeeded":
			return nil
		case "failed", "canceled":
			code := ""
			if em, ok := m["error"].(map[string]any); ok {
				code, _ = em["code"].(string)
			}
			if code == "" {
				code = status
			}
			return engErr(fmt.Sprintf("検索エンジンの処理が失敗しました（%s）", code))
		}
		if time.Now().After(deadline) {
			return engErr("検索エンジンの処理が時間内に終わりません")
		}
		time.Sleep(delay)
		if delay = delay * 2; delay > time.Second {
			delay = time.Second
		}
	}
}

func (e *searchEngine) indexExists(ctx context.Context, uid string) (bool, error) {
	_, _, err := e.call(ctx, http.MethodGet, "/indexes/"+uid, nil)
	var ee *engineError
	if errors.As(err, &ee) && ee.status == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

func (e *searchEngine) build(ctx context.Context, uid string, documents []map[string]any) error {
	steps := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/indexes", map[string]any{"uid": uid, "primaryKey": "id"}},
		{http.MethodPatch, "/indexes/" + uid + "/settings", engineSettings()},
	}
	for _, s := range steps {
		task, _, err := e.call(ctx, s.method, s.path, s.body)
		if err != nil {
			return err
		}
		if err := e.wait(ctx, task); err != nil {
			return err
		}
	}
	for start := 0; start < len(documents); start += engineBatch {
		task, _, err := e.call(ctx, http.MethodPost, "/indexes/"+uid+"/documents", documents[start:min(start+engineBatch, len(documents))])
		if err != nil {
			return err
		}
		if err := e.wait(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

func (e *searchEngine) do(ctx context.Context, method, path string, body any) error {
	task, _, err := e.call(ctx, method, path, body)
	if err != nil {
		return err
	}
	return e.wait(ctx, task)
}

// reindex は表示中の求人を、DB から索引に入れ直す。作り直した件数を返す。
// いつも別名の索引（recruits_next）に全件を入れてから、使う名前にする。すでに索引があるときは入れ替え（swap）、無いときは改名する。
// 作っている間は、使う名前の索引が「空・一部だけ」になる時間が無い（古い索引で検索できるか、索引が無くて Postgres に退避する）。途中で失敗しても、同じ。
// 文は search_fold（0009）を通した形で入れる。索引に入れる列（engineSearchable）は、recruit_search_text（0009）の対象の列と同じ。片方を変えたら、もう片方も直す。
func (e *searchEngine) reindex(ctx context.Context, db interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, out func(string)) (int, error) {
	cols := make([]string, len(engineSearchable))
	for i, c := range engineSearchable {
		cols[i] = fmt.Sprintf("search_fold(%s)", c) // 列名は、このモジュールの固定の文字列だけ
	}
	rows, err := db.Query(ctx, "SELECT id, "+strings.Join(cols, ", ")+" FROM recruits WHERE is_displayed ORDER BY id")
	if err != nil {
		return 0, err
	}
	documents := []map[string]any{}
	for rows.Next() {
		var id int32
		vals := make([]*string, len(engineSearchable))
		dest := []any{&id}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return 0, err
		}
		doc := map[string]any{"id": id}
		for i, c := range engineSearchable {
			if vals[i] == nil {
				doc[c] = nil
			} else {
				doc[c] = *vals[i]
			}
		}
		documents = append(documents, doc)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	staging := engineIndexUID + "_next"
	exists, err := e.indexExists(ctx, staging)
	if err != nil {
		return 0, err
	}
	if exists {
		if err := e.do(ctx, http.MethodDelete, "/indexes/"+staging, nil); err != nil { // 前回の途中の残り
			return 0, err
		}
	}
	if err := e.build(ctx, staging, documents); err != nil {
		return 0, err
	}
	live, err := e.indexExists(ctx, engineIndexUID)
	if err != nil {
		return 0, err
	}
	if live {
		if err := e.do(ctx, http.MethodPost, "/swap-indexes", []map[string]any{{"indexes": []string{engineIndexUID, staging}}}); err != nil {
			return 0, err
		}
		if err := e.do(ctx, http.MethodDelete, "/indexes/"+staging, nil); err != nil { // 入れ替えのあと、staging には古い内容がある
			return 0, err
		}
	} else if err := e.do(ctx, http.MethodPatch, "/indexes/"+staging, map[string]any{"uid": engineIndexUID}); err != nil { // 全件が入ってから、使う名前にする
		return 0, err
	}
	out(fmt.Sprintf("%d 件を索引に入れました", len(documents)))
	return len(documents), nil
}

// status は動作の確認用。(届くか・索引があるか・文書の数)。届かなければ engineError。
func (e *searchEngine) status(ctx context.Context) (bool, int64, error) {
	if _, _, err := e.call(ctx, http.MethodGet, "/health", nil); err != nil {
		return false, 0, err
	}
	stats, _, err := e.call(ctx, http.MethodGet, "/indexes/"+engineIndexUID+"/stats", nil)
	var ee *engineError
	if errors.As(err, &ee) && ee.status == http.StatusNotFound {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	var n int64
	if m, ok := stats.(map[string]any); ok {
		if num, ok := m["numberOfDocuments"].(interface{ Int64() (int64, error) }); ok {
			n, _ = num.Int64()
		}
	}
	return true, n, nil
}

func searchIndexMain(args []string, e env) int {
	meili, err := e.meili()
	if err != nil {
		fmt.Printf("失敗しました。%s\n", err)
		return 1
	}
	engine := newSearchEngine(*meili, func(string, ...any) {})
	ctx := context.Background()
	for _, a := range args {
		if a == "--status" {
			exists, n, err := engine.status(ctx)
			if err != nil {
				fmt.Printf("失敗しました。%s\n", err)
				return 1
			}
			fmt.Println("検索エンジン: 届きます")
			if exists {
				fmt.Printf("索引: あり（%d 件）\n", n)
			} else {
				fmt.Println("索引: なし（api search-index で作る）")
			}
			return 0
		}
	}
	url := e.str("DATABASE_URL")
	if url == "" {
		fmt.Println("失敗しました。DATABASE_URL が未設定です")
		return 1
	}
	pool, err := openPool(ctx, url, 2)
	if err != nil {
		fmt.Println("失敗しました。DB への接続か SQL の実行でエラーです")
		return 1
	}
	defer pool.Close()
	if _, err := engine.reindex(ctx, pool, func(s string) { fmt.Println(s) }); err != nil {
		var ee *engineError
		if errors.As(err, &ee) {
			fmt.Printf("失敗しました。%s\n", ee.msg)
		} else {
			// 接続情報を含みうる例外の本文は出さず、種類だけ出す
			fmt.Printf("失敗しました（%T）。DB への接続か SQL の実行でエラーです\n", err)
		}
		return 1
	}
	return 0
}
