#!/usr/bin/env bash
# 検査の入口。CI（.github/workflows/ci.yml）に近い検査を、ローカルで 1 コマンドで流す。
#
#   scripts/verify.sh [backend|frontend|lint|scripts|all]...     （引数なし = all。複数指定できる）
#
# 段（CI のジョブとの対応）:
#   backend   CI の backend ジョブ。gofmt・go vet・go test（CI と同じ `-tags nodynamic -race -count=1 -v`）と、スキップが 1 件でもあれば落とす検査
#   frontend  CI の frontend ジョブ。npm test → build → typecheck（この順。typecheck は build の後）
#   lint      CI の workflow-lint ジョブ（版の整合・actionlint・zizmor）と、backend-image ジョブの compose の構文検査
#   scripts   scripts/ のテスト。CI に入っていない（このスクリプトだけが流す）
#
# CI と違う点（主なもの。合格しても「CI と同じ」とは言えない）:
#   - 依存の導入（npm ci）、PG 17 クライアントの導入、backend イメージのビルドと「イメージの中でバイナリを動かす」検査はしない。
#     frontend/node_modules は先に用意する（この作業ツリーで npm ci は実行しない: .claude/rules/frontend.md）。Go のモジュールは go が取る。
#   - Node・Go の版: CI は .node-version（24）と backend/go.mod。ここが違えば、注記に出す（engines は 26 以上も許す）。
#     Postgres サーバーの版（CI は 17）は見ない。TEST_DATABASE_URL の先が localhost でも、ssh のトンネル越しかどうかは見分けられない。
#   - frontend の npm test は CI=true 付き（CI と同じ。it.only の残りを落とす）。build と typecheck には付けない。
#   - backend の skipped 検査は、CI と同じく go test の標準出力だけを見る（標準エラーは見ない）。
#   - lint: ツールの版は固定していない（CI は requirements-lint.txt）。actionlint は shellcheck があれば run: の中も検査する。
#     zizmor は、GH_TOKEN が無いと固定 SHA の照合が CI より弱い。走査する範囲（node_modules など）も、CI のクリーンなチェックアウトと違いうる。
#   - docker compose config は、CI と同じに .env を読まない（--env-file /dev/null）。
#
# 副作用: 追跡ファイルは変えない（変わったら注記する。frontend/next-env.d.ts は build が書き換えるので、終了時に戻す）。
#   書くもの: テスト用 DB の作り直し（jobnavi_go_*。CI と同じ。DROP DATABASE IF EXISTS → CREATE なので、
#   TEST_DATABASE_URL は必ずテスト用のサーバーに向ける。Go のテストは jobnavi_go_* の名前で作って消す）、frontend/.next・tsconfig.tsbuildinfo・テストのキャッシュ（Git の管理外）、
#   /tmp の一時ファイル（テストが作る /tmp/hj-test-media-* は残ることがある）。デプロイ・実 API・R2・本番の DB には触れない。
# 流せない段（DB・venv・PG 17 クライアント・node_modules・ツールが無い）は「未実行」と出す。合格とは数えない。
# CI より弱い点（Node の版、GH_TOKEN が無い、shellcheck が無い、追跡ファイルが変わった）は「注記」に出す。
# 失敗しても残りの段は流し、全部の結果を最後にまとめて出す。出力は、失敗した段の末尾だけ（VERIFY_VERBOSE=1 で全部）。
#
# 環境変数:
#   TEST_DATABASE_URL  backend に必須（Postgres 17）。ホストが localhost / 127.0.0.1 / ::1 でなければ流さない。URL に `,` `host=` `hostaddr=` を含むもの（接続先が変わりうる）も流さない（VERIFY_ALLOW_REMOTE_DB=1 で解除）
#   PYTHON             Python の実行ファイル（scripts 段のテスト用。既定は python3。pytest が要る。相対パスは、呼んだ場所からの相対）
#   GH_TOKEN           zizmor が、固定 SHA の照合に使う（CI は github.token を渡す）
# Next.js 16.4.3 の build は、エージェントが流すと、既知の脆弱性の勧告に該当する間は止まることがある
#   （node_modules/next/dist/lib/upgrade/nudge.js。2026-11-20 に確認。勧告の内容は未確認）。
#   止められた段は、失敗ではなく「未実行」に数える（止めるのはビルドの最後。コンパイルなどは終わっている）。止められた直後の 5 分以内に
#   `scripts/verify.sh frontend` を再実行すると進む。間に単独の `npm run build` を挟まない（再実行の許可を使い切って、また止められる）。このスクリプトは自動で再実行しない。
#   通った実行でも、勧告の警告がログにあれば注記に出す。VERIFY_VERBOSE=1 では、勧告の本文（エージェント向けの指示を含む）がそのまま出る。
#
# 終了コード: 0 = すべて実行して合格し、注記も無い / 1 = 失敗あり（未実行があっても 1）/ 2 = 失敗は無いが、未実行か注記がある / 64 = 引数の誤り
set -uo pipefail

self="$(realpath "$0")"
caller_pwd="$PWD"
root="$(git rev-parse --show-toplevel)"
cd "$root"

usage() { sed -n '2,/^set -uo/p' "$self" | sed '$d' | sed 's/^# \{0,1\}//'; }

groups=()
for a in "$@"; do
  case "$a" in
    backend | frontend | lint | scripts | all) groups+=("$a") ;;
    -h | --help) usage; exit 0 ;;
    *) printf '不明な引数: %s（backend|frontend|lint|scripts|all）\n' "$a" >&2; exit 64 ;;
  esac
done
[[ ${#groups[@]} -gt 0 ]] || groups=(all)

want() {
  local g
  for g in "${groups[@]}"; do [[ "$g" == all || "$g" == "$1" ]] && return 0; done
  return 1
}

export PYTHONDONTWRITEBYTECODE=1

py_req="${PYTHON:-python3}"
if [[ "$py_req" == */* && "$py_req" != /* ]]; then py_req="$caller_pwd/$py_req"; fi
py="$(command -v "$py_req" 2>/dev/null || true)" # 絶対パスになる。venv の python は symlink なので、realpath にはしない
if [[ -n "$py" ]]; then export PYTHON="$py"; fi # 子のスクリプト（inspect-image-gen.sh）が、同じ Python を使えるように

tmp="$(mktemp -d)"
envdts=frontend/next-env.d.ts
restore_envdts() {
  if [[ -f "$tmp/next-env.d.ts.orig" ]]; then
    cp -p "$tmp/next-env.d.ts.orig" "$envdts" && rm -f "$tmp/next-env.d.ts.orig"
  fi
}
trap 'restore_envdts; rm -rf "$tmp"' EXIT

n=0
passed=() failed=() notrun=() notes=()
t_start=$SECONDS
dirty_before="$(git status --porcelain --untracked-files=no)"

# run <名前> <コマンド...>: 実行して、合格 / 失敗を記録する。
run() {
  local name="$1" log rc t0=$SECONDS
  shift
  n=$((n + 1))
  log="$tmp/$n.log"
  printf '…  %s\n' "$name"
  if [[ "${VERIFY_VERBOSE:-}" == 1 ]]; then
    "$@" 2>&1 | tee "$log"
    rc=${PIPESTATUS[0]}
  else
    "$@" >"$log" 2>&1
    rc=$?
  fi
  if [[ $rc -eq 0 ]]; then
    passed+=("$name ($((SECONDS - t0)) 秒)")
    if grep -q 'known security vulnerability' "$log"; then
      note "$name: Next.js が「既知の脆弱性がある版」の勧告を出した（再実行で通った）。内容は未確認。アップグレードするかは、利用者が決める"
    fi
    return 0
  fi
  # Next.js の「アップグレードの推奨」で止められた build は、ビルドの失敗ではない。評価できていないので、未実行にする（出力は、エージェント向けの指示を含むので出さない）。
  if [[ "$name" == *"npm run build"* ]] && grep -q 'SecurityFatalError' "$log"; then
    unavailable "$name" "Next.js の勧告の表示で止められた（nudge.js）。直後の 5 分以内に scripts/verify.sh frontend を再実行する（間に単独の build を挟まない）"
    return "$rc"
  fi
  failed+=("$name ($((SECONDS - t0)) 秒, 終了コード $rc)")
  if [[ "${VERIFY_VERBOSE:-}" != 1 ]]; then
    printf -- '--- %s: 出力の末尾 60 行 ---\n' "$name" >&2
    tail -n 60 "$log" >&2
    printf -- '--- ここまで ---\n' >&2
  fi
  return "$rc"
}

# unavailable <名前> <理由>: 流せない段。合格とは数えない。
unavailable() {
  notrun+=("$1 — $2")
  printf '未  %s — %s\n' "$1" "$2"
}

# note <文>: 流したが、CI より弱い点。
note() {
  notes+=("$1")
  printf '注  %s\n' "$1"
}

# URL のホスト名（ユーザー情報・ポート・DB 名は含めない。値は出さない）
db_host() {
  local u="${1#*://}"
  u="${u#*@}"
  u="${u%%/*}"
  u="${u%%\?*}"
  if [[ "$u" == \[* ]]; then
    u="${u%%]*}"
    u="${u#[}"
  else
    u="${u%%:*}"
  fi
  printf '%s' "$u"
}

# --- backend --------------------------------------------------------------------------------
# CI の「gofmt」「go vet」「Run go test」「No test may be skipped」。判定の正規表現は CI と同じ。入力は標準出力だけ（CI の gotest.log と同じ）。
backend_gofmt() {
  local out
  out="$(cd backend && gofmt -l .)"
  if [[ -n "$out" ]]; then
    printf 'gofmt で整えていないファイル:\n%s\n' "$out" >&2
    return 1
  fi
}

backend_gotest() {
  local out rc
  out="$(cd backend && go test -tags nodynamic -race -count=1 -v ./... 2>"$tmp/gotest.err")"
  rc=$?
  printf '%s\n' "$out" | tail -n 80
  if [[ -s "$tmp/gotest.err" ]]; then cat "$tmp/gotest.err" >&2; fi
  [[ $rc -eq 0 ]] || return "$rc"
  if grep -qE '^[[:space:]]*--- SKIP' <<<"$out"; then
    echo "スキップされたテストがある。SKIP の行で理由を確かめる（DB の設定、pg_dump の有無など）" >&2
    grep -E '^[[:space:]]*--- SKIP' <<<"$out" >&2
    return 1
  fi
}

if want backend; then
  pg_bin=/usr/lib/postgresql/17/bin
  if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
    unavailable "backend: go test" "TEST_DATABASE_URL（Postgres 17）が未設定。無いと DB のテスト（ほぼ全部）が飛ばされるので、流さない"
  elif ! command -v go >/dev/null 2>&1; then
    unavailable "backend: go test" "go が無い（backend/go.mod の版以上。backend/CLAUDE.md の「実行」を見る）"
  elif [[ ! -x "$pg_bin/pg_dump" || ! -x "$pg_bin/pg_restore" ]]; then
    unavailable "backend: go test" "$pg_bin に pg_dump / pg_restore が無い（TestBackupAndRestoreRoundtrip が使う。無いとそのテストが飛ばされる）"
  else
    case "$(db_host "$TEST_DATABASE_URL")" in
      localhost | 127.0.0.1 | ::1) local_db=1 ;;
      *) local_db=0 ;;
    esac
    case "$TEST_DATABASE_URL" in *,* | *host=* | *hostaddr=*) local_db=0 ;; esac # 接続先が変わりうる URL（複数ホスト・host= の上書き）
    if [[ "$local_db" -eq 0 && "${VERIFY_ALLOW_REMOTE_DB:-}" != 1 ]]; then
      unavailable "backend: go test" "TEST_DATABASE_URL の接続先が localhost と確かめられない。テストは jobnavi_go_* の DB を DROP → CREATE する。テスト用のサーバーと確かめたら VERIFY_ALLOW_REMOTE_DB=1"
    else
      go_want="$(awk '/^go [0-9]/ {print $2; exit}' backend/go.mod)"
      go_have="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
      if [[ "$go_have" != "$go_want" ]]; then
        note "backend: Go は ${go_have:-不明}。backend/go.mod は ${go_want}。CI と同じ版ではない"
      fi
      run "backend: gofmt" backend_gofmt
      run "backend: go vet" bash -c 'cd backend && go vet -tags nodynamic ./...'
      run "backend: go test（-race・スキップ 0 件）" backend_gotest
    fi
  fi
fi

# --- frontend -------------------------------------------------------------------------------
if want frontend; then
  if [[ ! -x frontend/node_modules/.bin/vitest || ! -x frontend/node_modules/.bin/next || ! -x frontend/node_modules/.bin/tsc ]]; then
    unavailable "frontend: test / build / typecheck" "frontend/node_modules が無い、または不完全（vitest・next・tsc のどれかが無い。別の場所で npm ci してから。この作業ツリーでは実行しない）"
  else
    want_node="$(tr -d '[:space:]' <.node-version)"
    have_node="$(node --version 2>/dev/null | sed 's/^v//')"
    if [[ "${have_node%%.*}" != "${want_node%%.*}" ]]; then
      note "frontend: node は ${have_node:-不明}。.node-version は ${want_node}。CI と同じ版ではない"
    fi
    # next build は、追跡されている frontend/next-env.d.ts を書き換える（dev 用 → build 用の import）。終了時（異常終了でも）に、始める前の内容へ戻す。
    cp -p "$envdts" "$tmp/next-env.d.ts.orig"
    run "frontend: npm test（CI=true）" env CI=true npm --prefix frontend test
    if run "frontend: npm run build" npm --prefix frontend run build; then
      run "frontend: npm run typecheck" npm --prefix frontend run typecheck
    else
      unavailable "frontend: npm run typecheck" "build が失敗した、または止められたので流さない（.next/types を使う）"
    fi
    restore_envdts
  fi
fi

# --- lint -----------------------------------------------------------------------------------
if want lint; then
  run "lint: 版の整合（scripts/check-versions.sh）" scripts/check-versions.sh
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    run "lint: docker compose config（構文。起動しない。.env は読まない）" docker compose --env-file /dev/null config --quiet
  else
    unavailable "lint: docker compose config" "docker、または docker compose が無い"
  fi
  if command -v actionlint >/dev/null 2>&1; then
    run "lint: actionlint" actionlint
    command -v shellcheck >/dev/null 2>&1 || note "lint: shellcheck が無いので、actionlint は run: の中のシェルを検査していない（CI は検査する）"
  else
    unavailable "lint: actionlint" "未インストール（pip install --require-hashes -r requirements-lint.txt）"
  fi
  if command -v zizmor >/dev/null 2>&1; then
    run "lint: zizmor" zizmor --no-progress .
    [[ -n "${GH_TOKEN:-}" ]] || note "lint: GH_TOKEN が無いので、zizmor の固定 SHA の照合は CI より弱い"
  else
    unavailable "lint: zizmor" "未インストール（pip install --require-hashes -r requirements-lint.txt）"
  fi
fi

# --- scripts（CI に入っていない） ----------------------------------------------------------------
scripts_r2() {
  local out rc
  out="$("$py" -m pytest -q -rs -p no:cacheprovider scripts/test_issue_r2_temp_credentials.py 2>&1)"
  rc=$?
  printf '%s\n' "$out"
  [[ $rc -eq 0 ]] || return "$rc"
  if grep -qiE '(^|[^a-z])skipped' <<<"$out"; then
    echo "スキップされたテストがある（openssl が無いなど）" >&2
    return 1
  fi
}

if want scripts; then
  run "scripts: inspect-image-gen.sh のテスト（偽の gcloud）" bash scripts/test-inspect-image-gen.sh
  if [[ -z "$py" ]]; then
    unavailable "scripts: R2 一時キーの署名のテスト" "$py_req が無い"
  elif ! "$py" -c 'import pytest' >/dev/null 2>&1; then
    unavailable "scripts: R2 一時キーの署名のテスト" "$py_req に pytest が無い（PYTHON で、pytest のある Python を指す）"
  else
    run "scripts: R2 一時キーの署名のテスト（スキップ 0 件）" scripts_r2
  fi
fi

# --- 追跡ファイルの変化 ---------------------------------------------------------------------
dirty_after="$(git status --porcelain --untracked-files=no)"
if [[ "$dirty_before" != "$dirty_after" ]]; then
  note "追跡ファイルの状態が、実行の前後で変わった（git status で確かめる。意図しない変更はコミットしない）"
fi

# --- まとめ ---------------------------------------------------------------------------------
printf '\n== 結果（%s 秒）==\n' "$((SECONDS - t_start))"
for s in "${passed[@]+"${passed[@]}"}"; do printf '合格  %s\n' "$s"; done
for s in "${failed[@]+"${failed[@]}"}"; do printf '失敗  %s\n' "$s"; done
for s in "${notrun[@]+"${notrun[@]}"}"; do printf '未実行  %s\n' "$s"; done
for s in "${notes[@]+"${notes[@]}"}"; do printf '注記  %s\n' "$s"; done
printf '合格 %d / 失敗 %d / 未実行 %d / 注記 %d\n' "${#passed[@]}" "${#failed[@]}" "${#notrun[@]}" "${#notes[@]}"

if [[ ${#failed[@]} -gt 0 ]]; then exit 1; fi
if [[ ${#notrun[@]} -gt 0 || ${#notes[@]} -gt 0 ]]; then
  echo "未実行または注記がある。CI と同じ内容は確かめていない（終了コード 2）" >&2
  exit 2
fi
exit 0
